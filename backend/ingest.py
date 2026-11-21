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

from app import metadata_text
from settings import SETTINGS

DEFAULT_ENDPOINT = SETTINGS.oai_endpoint
DEFAULT_TARGET = SETTINGS.ingest_target
JSON_PATH = SETTINGS.ingest_json_path
INSERT_BATCH_SIZE = SETTINGS.insert_batch_size
EMBED_BATCH_SIZE = SETTINGS.embed_batch_size
MIN_DESCRIPTION_CHARS = SETTINGS.min_description_chars
COLLECTION_NAME = SETTINGS.collection_name
DB_PATH = SETTINGS.db_path
MODEL_NAME = SETTINGS.model_name
N_RESULTS = SETTINGS.n_results
THRESHOLD_DEFAULT = SETTINGS.threshold_default

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


def is_valid_record(metadata: dict) -> bool:
    """Skip administrative sheets and records without a real abstract."""
    if not metadata:
        return False
    title = _first(metadata.get("title"))
    description = _first(metadata.get("description"))
    url = next((u for u in _as_list(metadata.get("identifier")) if "http" in u), "")
    administrative = "penilaian sejawat" in title.lower() or "peer_review" in url.lower()
    return (
        bool(description)
        and len(description.strip()) > MIN_DESCRIPTION_CHARS
        and not administrative
    )


def record_to_item(metadata: dict) -> dict:
    identifiers = _as_list(metadata.get("identifier"))
    return {
        "title": _first(metadata.get("title")),
        "creators": _as_list(metadata.get("creator")),
        "description": _first(metadata.get("description")),
        "url": next((u for u in identifiers if "http" in u), ""),
        "date": _first(metadata.get("date"), "Tanggal tidak diketahui"),
    }


def _default_records(endpoint: str) -> Iterable:
    from sickle import Sickle  # imported lazily: only harvesting needs it

    return Sickle(endpoint).ListRecords(metadataPrefix="oai_dc")


# ---- harvest ---------------------------------------------------------------
def harvest(
    target: int = DEFAULT_TARGET,
    endpoint: str = DEFAULT_ENDPOINT,
    output: str | Path = JSON_PATH,
    records: Iterable | None = None,
) -> list[dict]:
    """Collect `target` valid records and write them as JSON.

    `records` is injectable for tests; by default it comes from OAI-PMH.
    """
    if target < 1:
        raise ValueError("target harus >= 1")

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
        if len(items) % 250 == 0:
            log.info("harvest: %d valid dari %d record dipindai", len(items), scanned)
        if len(items) >= target:
            break

    path = Path(output)
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as handle:
        json.dump(items, handle, ensure_ascii=False, indent=2)

    log.info("harvest selesai: %d valid dari %d dipindai -> %s", len(items), scanned, path)
    return items


# ---- index -----------------------------------------------------------------
def build_index(
    json_path: str | Path = JSON_PATH,
    db_path: str = DB_PATH,
    batch_size: int = INSERT_BATCH_SIZE,
    model=None,
    client=None,
) -> int:
    """(Re)build the Chroma collection from the harvested JSON.

    Deletes the existing collection first: re-running replaces all vectors,
    there is no incremental update path. `model` and `client` are injectable
    for tests.
    """
    import chromadb

    data = json.loads(Path(json_path).read_text(encoding="utf-8"))
    if not data:
        raise ValueError(f"{json_path} kosong: jalankan harvest dulu")

    if model is None:
        from sentence_transformers import SentenceTransformer

        model = SentenceTransformer(MODEL_NAME)

    documents, metadatas, ids = [], [], []
    for idx, item in enumerate(data):
        documents.append(f"{item['title']}. {item['description']}")
        metadatas.append(
            {
                "title": item["title"],
                "creators": ", ".join(_as_list(item["creators"])),
                "url": item["url"],
                "description": item["description"],
                "date": _first(item.get("date"), "Tanggal tidak diketahui"),
            }
        )
        ids.append(f"doc_{idx}")

    log.info("index: %d dokumen, membuat embedding...", len(ids))
    embeddings = model.encode(
        documents, batch_size=EMBED_BATCH_SIZE, show_progress_bar=False
    ).tolist()

    client = client or chromadb.PersistentClient(path=db_path)
    try:
        client.delete_collection(name=COLLECTION_NAME)
    except Exception:  # collection may not exist yet
        pass

    collection = client.get_or_create_collection(
        name=COLLECTION_NAME,
        metadata={"hnsw:space": "cosine", "embedding_model": MODEL_NAME},
    )

    for start in range(0, len(ids), batch_size):
        end = min(start + batch_size, len(ids))
        collection.add(
            ids=ids[start:end],
            embeddings=embeddings[start:end],
            metadatas=metadatas[start:end],
            documents=documents[start:end],
        )
        log.info("index: %d/%d tersimpan", end, len(ids))

    total = collection.count()
    log.info("index selesai: %d dokumen (%s)", total, collection.metadata)
    return total


# ---- query demo ------------------------------------------------------------
def query_demo(
    query: str,
    db_path: str = DB_PATH,
    min_similarity: float = THRESHOLD_DEFAULT,
    n_results: int = N_RESULTS,
    model=None,
    collection=None,
) -> list[dict]:
    """Sanity-check the index with the same semantics as POST /search."""
    import chromadb

    if model is None:
        from sentence_transformers import SentenceTransformer

        model = SentenceTransformer(MODEL_NAME)

    if collection is None:
        collection = chromadb.PersistentClient(path=db_path).get_collection(
            COLLECTION_NAME
        )
    raw = collection.query(
        query_embeddings=[model.encode(query).tolist()],
        n_results=n_results,
        include=["metadatas", "distances"],
    )

    results = []
    rank = 0
    for metadata, distance in zip(raw["metadatas"][0], raw["distances"][0]):
        similarity = round(1.0 - distance, 6)
        if similarity < min_similarity:
            continue
        rank += 1
        results.append(
            {
                "peringkat": rank,
                "skor": similarity,
                "judul": metadata_text(metadata, "title"),
                "penulis": metadata_text(metadata, "creators"),
                "deskripsi": metadata_text(metadata, "description"),
                "url": metadata_text(metadata, "url"),
                "date": metadata_text(metadata, "date"),
            }
        )
    return results


# ---- CLI -------------------------------------------------------------------
def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=(
            "Harvest eprints, build the Chroma index, or run a sample query. "
            "Run from backend/."
        )
    )
    parser.add_argument("--harvest", action="store_true", help="harvest OAI-PMH ke JSON")
    parser.add_argument("--index", action="store_true", help="bangun ulang indeks Chroma")
    parser.add_argument("--all", action="store_true", help="harvest lalu index")
    parser.add_argument("--query", metavar="TEXT", help="uji pencarian pada indeks")
    parser.add_argument("--target", type=int, default=DEFAULT_TARGET, help="jumlah record valid")
    parser.add_argument("--endpoint", default=DEFAULT_ENDPOINT, help="URL OAI-PMH")
    parser.add_argument("--json", default=JSON_PATH, help="path JSON hasil harvest")
    parser.add_argument("--db", default=DB_PATH, help="path direktori Chroma")
    parser.add_argument(
        "--min-similarity", type=float, default=THRESHOLD_DEFAULT, help="ambang query demo"
    )
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    logging.basicConfig(
        level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s"
    )
    parser = build_parser()
    args = parser.parse_args(argv)

    if not (args.harvest or args.index or args.all or args.query):
        parser.error("pilih --harvest, --index, --all, atau --query")

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
                f"Tidak ada dokumen dengan kemiripan >= {args.min_similarity:.2f}.",
                file=sys.stderr,
            )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
