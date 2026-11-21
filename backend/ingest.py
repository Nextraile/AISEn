"""Single ingestion path for the Undip semantic search index.

Harvests OAI-PMH records from the Undip eprints repository, writes a clean
JSON file, and builds the Chroma collection the API serves.

Run from `backend/` so the relative paths and the `app` import resolve:

    python ingest.py --all
    python ingest.py --harvest --target 1000
    python ingest.py --index
    python ingest.py --query "analisis kualitas air sungai"

The API and this script read the same `Settings` object (see `settings.py`), so
stored vectors and query vectors cannot drift apart on the model, the database
path, or the collection name. Environment defaults are listed in `.env.example`;
the CLI flags below still win over them.
"""

from __future__ import annotations

import argparse
import json
import logging
import sys
from pathlib import Path
from typing import Iterable, Sequence

import chromadb

from app import rank_results
from settings import SETTINGS

# Log a progress line every N valid records; harvesting a thousand records is
# slow enough that silence looks like a hang.
PROGRESS_EVERY = 250

# Stored in document metadata when the repository has no date. It is data, not
# a message, and it is displayed to users, so it stays Indonesian.
UNKNOWN_DATE = "Tanggal tidak diketahui"

log = logging.getLogger("aisen.ingest")


# ---- helpers ---------------------------------------------------------------
def _as_list(value) -> list[str]:
    """OAI fields are usually lists, but tolerate a bare string/None."""
    if value is None:
        return []
    if isinstance(value, str):
        return [value]
    return list(value)


def _first(value, default: str = "") -> str:
    values = _as_list(value)
    return values[0] if values else default


def _record_url(metadata: dict) -> str:
    """First `identifier` that looks like a URL (the record's own page or PDF)."""
    return next(
        (u for u in _as_list(metadata.get("identifier")) if "http" in u), ""
    )


def is_valid_record(metadata: dict) -> bool:
    """Skip administrative sheets and records without a real abstract."""
    if not metadata:
        return False
    title = _first(metadata.get("title"))
    description = _first(metadata.get("description"))
    administrative = (
        "penilaian sejawat" in title.lower() or "peer_review" in _record_url(metadata).lower()
    )
    return (
        bool(description)
        and len(description.strip()) > SETTINGS.min_description_chars
        and not administrative
    )


def record_to_item(metadata: dict) -> dict:
    return {
        "title": _first(metadata.get("title")),
        "creators": _as_list(metadata.get("creator")),
        "description": _first(metadata.get("description")),
        "url": _record_url(metadata),
        "date": _first(metadata.get("date"), UNKNOWN_DATE),
    }


def _default_records(endpoint: str) -> Iterable:
    from sickle import Sickle  # lazy: only harvesting needs this dependency

    return Sickle(endpoint).ListRecords(metadataPrefix="oai_dc")


# ---- harvest ---------------------------------------------------------------
def harvest(
    target: int = SETTINGS.ingest_target,
    endpoint: str = SETTINGS.oai_endpoint,
    output: str | Path = SETTINGS.ingest_json_path,
    records: Iterable | None = None,
) -> list[dict]:
    """Collect `target` valid records and write them as JSON.

    `records` is injectable for tests; by default it comes from OAI-PMH.
    """
    if target < 1:
        raise ValueError(f"--target must be >= 1 (got {target})")

    items: list[dict] = []
    scanned = 0
    stream = records if records is not None else _default_records(endpoint)

    for record in stream:
        scanned += 1
        try:
            metadata = record.metadata
        except AttributeError:
            continue  # deleted/broken record
        if not is_valid_record(metadata):
            continue

        items.append(record_to_item(metadata))
        if len(items) % PROGRESS_EVERY == 0:
            log.info(
                "harvest: %d valid of %d records scanned", len(items), scanned
            )
        if len(items) >= target:
            break

    path = Path(output)
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as handle:
        json.dump(items, handle, ensure_ascii=False, indent=2)

    log.info(
        "harvest done: %d valid of %d scanned -> %s", len(items), scanned, path
    )
    return items


# ---- index -----------------------------------------------------------------
def build_index(
    json_path: str | Path = SETTINGS.ingest_json_path,
    db_path: str = SETTINGS.db_path,
    batch_size: int = SETTINGS.insert_batch_size,
    model=None,
    client=None,
) -> int:
    """(Re)build the Chroma collection from the harvested JSON.

    Deletes the existing collection first: re-running replaces all vectors,
    there is no incremental update path. `model` and `client` are injectable
    for tests.
    """
    data = json.loads(Path(json_path).read_text(encoding="utf-8"))
    if not data:
        raise ValueError(f"{json_path} is empty: run --harvest first")

    if model is None:
        from sentence_transformers import SentenceTransformer

        model = SentenceTransformer(SETTINGS.model_name)

    documents, metadatas, ids = [], [], []
    for idx, item in enumerate(data):
        documents.append(f"{item['title']}. {item['description']}")
        metadatas.append(
            {
                "title": item["title"],
                "creators": ", ".join(_as_list(item["creators"])),
                "url": item["url"],
                "description": item["description"],
                "date": _first(item.get("date"), UNKNOWN_DATE),
            }
        )
        ids.append(f"doc_{idx}")

    log.info("index: %d documents, creating embeddings...", len(ids))
    embeddings = model.encode(
        documents,
        batch_size=SETTINGS.embed_batch_size,
        show_progress_bar=False,
    ).tolist()

    client = client or chromadb.PersistentClient(path=db_path)
    try:
        client.delete_collection(name=SETTINGS.collection_name)
    except chromadb.errors.NotFoundError:
        pass  # first build: there is nothing to replace

    collection = client.get_or_create_collection(
        name=SETTINGS.collection_name,
        metadata={"hnsw:space": "cosine", "embedding_model": SETTINGS.model_name},
    )

    for start in range(0, len(ids), batch_size):
        end = min(start + batch_size, len(ids))
        collection.add(
            ids=ids[start:end],
            embeddings=embeddings[start:end],
            metadatas=metadatas[start:end],
            documents=documents[start:end],
        )
        log.info("index: %d/%d stored", end, len(ids))

    total = collection.count()
    log.info("index done: %d documents (%s)", total, collection.metadata)
    return total


# ---- query demo ------------------------------------------------------------
def query_demo(
    query: str,
    db_path: str = SETTINGS.db_path,
    min_similarity: float = SETTINGS.threshold_default,
    n_results: int = SETTINGS.n_results,
    model=None,
    collection=None,
) -> list[dict]:
    """Sanity-check the index with the same ranking code as POST /search."""
    if model is None:
        from sentence_transformers import SentenceTransformer

        model = SentenceTransformer(SETTINGS.model_name)

    if collection is None:
        client = chromadb.PersistentClient(path=db_path)
        collection = client.get_collection(SETTINGS.collection_name)

    raw = collection.query(
        query_embeddings=[model.encode(query).tolist()],
        n_results=n_results,
        include=["metadatas", "distances"],
    )
    metadatas = raw["metadatas"][0] if raw["metadatas"] else []
    distances = raw["distances"][0] if raw["distances"] else []
    return [
        result.model_dump()
        for result in rank_results(metadatas, distances, min_similarity)
    ]


# ---- CLI -------------------------------------------------------------------
def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=(
            "Harvest eprints, build the Chroma index, or run a sample query. "
            "Run from backend/."
        )
    )
    parser.add_argument("--harvest", action="store_true", help="harvest OAI-PMH into JSON")
    parser.add_argument("--index", action="store_true", help="rebuild the Chroma index")
    parser.add_argument("--all", action="store_true", help="harvest, then index")
    parser.add_argument("--query", metavar="TEXT", help="run a query against the index")
    parser.add_argument(
        "--target",
        type=int,
        default=SETTINGS.ingest_target,
        help="number of valid records to collect",
    )
    parser.add_argument(
        "--endpoint", default=SETTINGS.oai_endpoint, help="OAI-PMH endpoint URL"
    )
    parser.add_argument(
        "--json",
        default=SETTINGS.ingest_json_path,
        help="path of the harvested JSON file",
    )
    parser.add_argument(
        "--db", default=SETTINGS.db_path, help="path of the Chroma directory"
    )
    parser.add_argument(
        "--min-similarity",
        type=float,
        default=SETTINGS.threshold_default,
        help="minimum similarity for --query",
    )
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    logging.basicConfig(
        level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s"
    )
    parser = build_parser()
    args = parser.parse_args(argv)

    if not (args.harvest or args.index or args.all or args.query):
        parser.error("choose one of --harvest, --index, --all, --query")

    if args.all or args.harvest:
        harvest(target=args.target, endpoint=args.endpoint, output=args.json)
    if args.all or args.index:
        build_index(json_path=args.json, db_path=args.db)
    if args.query:
        results = query_demo(
            args.query, db_path=args.db, min_similarity=args.min_similarity
        )
        for item in results:
            print(f"[{item['peringkat']}] {item['skor']:.4f}  {item['judul']}")
        if not results:
            print(
                f"No document reached similarity >= {args.min_similarity:.2f}.",
                file=sys.stderr,
            )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
