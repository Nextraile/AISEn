"""Single ingestion path for the Undip semantic search index.

Harvests OAI-PMH records from the Undip eprints repository, writes a clean
JSON file, and builds the Chroma collection the API serves.

Run from `backend/` so the relative paths and the package imports resolve:

    python -m ingestion.ingest --all
    python -m ingestion.ingest --harvest --target 1000
    python -m ingestion.ingest --harvest --target 48000   # resumes, tops up
    python -m ingestion.ingest --harvest --fresh          # discards resume state
    python -m ingestion.ingest --index
    python -m ingestion.ingest --query "analisis kualitas air sungai"

The API and this script read the same `Settings` object (see `common/config.py`),
so stored vectors and query vectors cannot drift apart on the model, the database
path, or the collection name. Environment defaults are listed in `.env.example`;
the CLI flags below still win over them.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
import time
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterable, Iterator, Sequence

import chromadb

from service.app import rank_results
from common.log import configure_logging
from common.config import SETTINGS

log = logging.getLogger("aisen.ingest")

# Stored in document metadata when the repository has no date. It is data, not
# a message, and it is displayed to users, so it stays Indonesian.
UNKNOWN_DATE = "Tanggal tidak diketahui"


def _utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _format_duration(seconds: float) -> str:
    total = int(max(0.0, seconds))
    hours, remainder = divmod(total, 3600)
    minutes, secs = divmod(remainder, 60)
    if hours:
        return f"{hours}h{minutes:02d}m"
    if minutes:
        return f"{minutes}m{secs:02d}s"
    return f"{secs}s"


class IngestLocked(RuntimeError):
    """Another ingest already holds the single-writer lock."""


@contextmanager
def ingest_lock(path: Path | None = None) -> Iterator[None]:
    """Refuse to run two ingests against the same directory.

    The collection is rewritten in place (delete, then re-add), so two processes
    running at once interleave a delete with the other's inserts and the winne
    is undefined. That is worth a hard stop, not a warning.
    """
    lock_path = path or Path(SETTINGS.temp_dir) / ".ingest.lock"
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    handle = lock_path.open("a+", encoding="utf-8")

    try:
        import fcntl  # POSIX only; Docker and WSL both have it
    except ImportError:
        log.debug("fcntl unavailable on this platform; skipping the ingest lock")
        yield
        handle.close()
        return

    try:
        fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        holder = lock_path.read_text(encoding="utf-8").strip()
        handle.close()
        raise IngestLocked(
            "another ingest is already running"
            + (f" ({holder})" if holder else "")
            + f"; wait for it to finish, or remove {lock_path} if it is stale"
        ) from None

    handle.seek(0)
    handle.truncate()
    handle.write(f"pid {os.getpid()} started {_utc_now()}")
    handle.flush()
    try:
        yield
    finally:
        fcntl.flock(handle, fcntl.LOCK_UN)
        handle.close()


def _write_run_summary(
    *,
    status: str,
    stage: str,
    started_at: str,
    duration_s: float,
    counts: dict[str, int],
    error: str | None = None,
    resume_from: dict | None = None,
) -> None:
    """Record how the last run ended, next to the data it produced.

    A removed `--rm` container takes its logs with it; this file is what answers
    "did the last ingest work?" from the host or from `/ready`.

    `data/history.json` is append-only JSONL: one line per *successful* run, so
    a resumed harvest leaves a record of every run that moved the corpus
    forward instead of overwriting the previous one. Failures are logged and
    reported by the CLI but not appended — a failed run produced no data, and
    a half-written line must never be readable as a successful one.

    `/ready` reads the last line, so nothing else needs to change to keep its
    contract: same keys, same shape, one entry.
    """
    path = Path(SETTINGS.ingest_json_path).parent / "history.json"
    payload = {
        "status": status,
        "stage": stage,
        "started_at": started_at,
        "finished_at": _utc_now(),
        "duration_s": round(duration_s, 1),
        "records_harvested": counts.get("records"),
        "documents_indexed": counts.get("documents"),
        "target": SETTINGS.ingest_target,
        "model": SETTINGS.model_name,
        "collection": SETTINGS.collection_name,
        "error": error,
        # None on a run that started from scratch; otherwise what this run
        # inherited, so a short run in the history is explainable.
        "resume_from": resume_from,
    }
    path.parent.mkdir(parents=True, exist_ok=True)
    line = json.dumps(payload, ensure_ascii=False) + "\n"
    # Journal-first, fsync per entry: an interrupted append leaves at worst a
    # torn final line, and `read_history` drops an unparseable one.
    with path.open("a", encoding="utf-8") as handle:
        handle.write(line)
        handle.flush()
        os.fsync(handle.fileno())


# ---- helpers ---------------------------------------------------------------
def _as_list(value) -> list[str]:
    """OAI fields are usually lists, but tolerate a bare string/None.

    Empty entries are dropped. A record can carry an empty ``<dc:creator/>``,
    which arrives as ``None`` inside the list; ``", ".join(...)`` on that raises
    TypeError and aborts the whole index build.
    """
    if value is None:
        return []
    values = [value] if isinstance(value, str) else list(value)

    cleaned: list[str] = []
    for entry in values:
        text = "" if entry is None else str(entry).strip()
        if text:
            cleaned.append(text)
    return cleaned


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


def _default_records(endpoint: str, token: str | None = None) -> Iterable:
    """OAI-PMH record stream, optionally resumed from a `resumptionToken`."""
    from sickle import Sickle  # lazy: only harvesting needs this dependency

    params = {"metadataPrefix": "oai_dc"}
    if token:
        params["resumptionToken"] = token
    # `ignore_deleted=False` (sickle's default) is what the first full harvest
    # used: one page carries 39 headers flagged deleted that `is_valid_record`
    # then drops. Switching it changes page contents, so it is never inferred at
    # resume time — a resumed run must see the same stream the first one did.
    return Sickle(endpoint).ListRecords(**params)


def _resume_snapshot() -> dict | None:
    """What a run is about to inherit, or None if it starts from scratch.

    Read before the harvest stage runs, so the history entry says "resumed at
    200" instead of leaving the reader to infer it from a target of 500. Only
    the readable numbers are recorded: the cursor itself is an opaque, URL-
    encoded resume detail and would be noise in a history file.
    """
    paths = harvest_paths()
    try:
        records = sum(
            1
            for line in paths["journal"].read_text(encoding="utf-8").splitlines()
            if line.strip()
        )
    except OSError:
        return None
    if not records:
        return None
    state = _load_harvest_state(paths["state"]) or {}
    return {"records": records, "scanned": state.get("scanned")}


# ---- harvest state ---------------------------------------------------------
# Everything a resumed harvest needs lives in data/temp/. The final output stays
# data/documents.json so the index build and the docs keep pointing at one file.
def harvest_paths(temp_dir: str | Path | None = None) -> dict[str, Path]:
    """Locations of the harvest journal, its cursor, and the final output."""
    base = Path(temp_dir or SETTINGS.temp_dir)
    return {
        "journal": base / "harvest.jsonl",
        "state": base / "harvest_state.json",
        "output": Path(SETTINGS.ingest_json_path),
    }


_OAI_NS = "{http://www.openarchives.org/OAI/2.0/}"


def _page_cursor(stream) -> str | None:
    """The resumption token that names the page currently being consumed.

    `stream.resumption_token` is NOT this value. sickle stores the token of the
    *next* response before it yields the first record of the current one, so
    committing it skips a page on resume. Measured against the live endpoint:
    it read `offset=215` while records 108..214 of the `offset=108` page were
    still being handed out, and a resumed run then lost 12 records.

    The response's own `<request resumptionToken="...">` element names the page
    being read, which is exactly what a resume must send back. The first page is
    requested without a token, so the attribute is absent and the cursor is
    None: a crash there restarts from the top, which is correct and cheap.
    """
    xml = getattr(getattr(stream, "oai_response", None), "xml", None)
    if xml is None:
        return None
    request = xml.find(f"{_OAI_NS}request")
    if request is None:
        return None
    return request.attrib.get("resumptionToken") or None


def _page_record_count(stream) -> int | None:
    """How many records the response currently being consumed contains.

    `None` for an injected iterable, which has no OAI response behind it. That
    is what tells `harvest()` whether a page boundary can be trusted at all.
    """
    xml = getattr(getattr(stream, "oai_response", None), "xml", None)
    if xml is None:
        return None
    return len(xml.findall(f".//{_OAI_NS}record"))


def _atomic_write(path: Path, text: str) -> None:
    """Replace `path` in one step, so a crash never leaves it half-written."""
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    with tmp.open("w", encoding="utf-8") as handle:
        handle.write(text)
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(tmp, path)


def _load_harvest_state(path: Path) -> dict | None:
    """Read the cursor written by the previous page, or None on a fresh start."""
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None


def _read_journal(path: Path) -> tuple[list[dict], set[str]]:
    """Rebuild the record list from the journal, dropping duplicated identifiers.

    A page is appended before the cursor advances, so an interrupted run can
    leave a page that the cursor will fetch again. Dedup by the OAI identifier
    makes replay harmless and keeps the document order identical.
    """
    items: list[dict] = []
    seen: set[str] = set()
    try:
        handle = path.open("r", encoding="utf-8")
    except OSError:
        return items, seen
    with handle:
        for line in handle:
            line = line.strip()
            if not line:
                continue
            try:
                entry = json.loads(line)
            except ValueError:
                # A torn final line from a hard kill: the cursor is authoritative.
                continue
            identifier = entry.pop("_identifier", "")
            if identifier and identifier in seen:
                continue
            seen.add(identifier)
            items.append(entry)
    return items, seen


def harvest(
    target: int = SETTINGS.ingest_target,
    endpoint: str = SETTINGS.oai_endpoint,
    output: str | Path = SETTINGS.ingest_json_path,
    records: Iterable | None = None,
    progress_every: int | None = None,
    fresh: bool = False,
    temp_dir: str | Path | None = None,
) -> list[dict]:
    """Collect `target` valid records and write them as JSON.

    The run is resumable. Every page is appended to `data/temp/harvest.jsonl`
    and fsynced, then the cursor of the page just read is stored in
    `data/temp/harvest_state.json`; an interrupted harvest therefore loses at
    most the page in flight and the next run continues from that cursor instead
    of re-fetching the corpus.

    `records` is injectable for tests. An injected iterable has no OAI response
    behind it, so there is no cursor and no page boundary: everything is
    flushed once at the end.
    """
    if target < 1:
        raise ValueError(f"--target must be >= 1 (got {target})")

    every = progress_every or SETTINGS.progress_every
    paths = harvest_paths(temp_dir)
    journal, state_path, out = paths["journal"], paths["state"], Path(output)

    if fresh:
        for path in (journal, state_path):
            path.unlink(missing_ok=True)

    items, seen = _read_journal(journal)
    state = _load_harvest_state(state_path) or {}
    token = state.get("next_token") or None
    scanned = int(state.get("scanned") or 0)
    resumed_at = len(items)

    if resumed_at:
        log.info(
            "harvest resume",
            extra={"extra_data": {
                "valid": resumed_at,
                "target": target,
                "token": token or "-",
            }},
        )

    if len(items) >= target:
        # Already at the target: rewriting the output would be the only work,
        # and appending one more record would break the "stop at target" rule.
        log.info(
            "harvest complete",
            extra={"extra_data": {
                "valid": len(items),
                "scanned": scanned,
                "resumed_from": resumed_at,
                "out": out,
            }},
        )
        return items

    journal.parent.mkdir(parents=True, exist_ok=True)
    stream = records if records is not None else _default_records(endpoint, token)

    from sickle.oaiexceptions import NoRecordsMatch

    # The harvester walks one page at a time so a page can be committed before
    # the next request. With an injected iterable there is no page boundary to
    # commit at, which is why `records` is only used by tests.
    page: list[tuple[str, dict]] = []

    def commit_page(scanned_so_far: int) -> None:
        """Append the buffered page, fsync it, then move the cursor."""
        if not page:
            return
        # Journal first: if the process dies between the two writes the next run
        # refetches this page and dedups it, which is recoverable. The opposite
        # order would skip records permanently.
        with journal.open("a", encoding="utf-8") as handle:
            for identifier, item in page:
                payload = dict(item)
                payload["_identifier"] = identifier
                handle.write(json.dumps(payload, ensure_ascii=False) + "\n")
            handle.flush()
            os.fsync(handle.fileno())
        page.clear()
        _atomic_write(out, json.dumps(items, ensure_ascii=False, indent=2))
        _atomic_write(
            state_path,
            json.dumps(
                {
                    "next_token": _page_cursor(stream),
                    "valid": len(items),
                    "scanned": scanned_so_far,
                    "target": target,
                    "endpoint": endpoint,
                    "updated_at": _utc_now(),
                },
                indent=2,
            )
            + "\n",
        )

    try:
        iterator = iter(stream)
        while True:
            try:
                record = next(iterator)
            except StopIteration:
                break
            except NoRecordsMatch:
                # The repository answers an exhausted offset with an <error>.
                log.info("harvest reached the end of the list")
                break

            scanned += 1
            try:
                metadata = record.metadata
            except AttributeError:
                continue  # deleted/broken record
            if not is_valid_record(metadata):
                continue

            identifier = getattr(getattr(record, "header", None), "identifier", "")
            if identifier and identifier in seen:
                continue
            if identifier:
                seen.add(identifier)

            items.append(record_to_item(metadata))
            page.append((identifier, items[-1]))

            if len(items) % every == 0:
                log.info(
                    "harvesting",
                    extra={"extra_data": {"valid": len(items), "scanned": scanned}},
                )
            if len(items) >= target:
                break
            # Commit when the response that produced the buffered records has
            # been fully consumed. `_page_cursor` is None for an injected
            # iterable, so tests fall through to the single flush in `finally`.
            records_in_page = _page_record_count(stream)
            if records_in_page is not None and len(page) >= records_in_page:
                commit_page(scanned)
    finally:
        commit_page(scanned)

    _atomic_write(out, json.dumps(items, ensure_ascii=False, indent=2))

    log.info(
        "harvest complete",
        extra={"extra_data": {
            "valid": len(items),
            "scanned": scanned,
            "resumed_from": resumed_at,
            "out": out,
        }},
    )
    return items


# ---- index -----------------------------------------------------------------
def _encode_in_chunks(
    model, documents: Sequence[str], chunk_size: int | None = None
) -> list[list[float]]:
    """Embed `documents` in chunks, logging after each one.

    A single `model.encode(...)` over ~48k abstracts is tens of minutes with no
    output, which is indistinguishable from a hang. Encoding in chunks also
    bounds peak memory: the full matrix as Python floats is hundreds of MB.
    """
    total = len(documents)
    size = max(1, chunk_size or SETTINGS.embed_chunk_size)
    vectors: list[list[float]] = []
    started = time.monotonic()

    for start in range(0, total, size):
        chunk = list(documents[start : start + size])
        vectors.extend(
            model.encode(
                chunk,
                batch_size=SETTINGS.embed_batch_size,
                show_progress_bar=False,
            ).tolist()
        )
        done = len(vectors)
        elapsed = time.monotonic() - started
        rate = done / elapsed if elapsed > 0 else 0.0
        eta = (total - done) / rate if rate > 0 else 0.0
        log.info(
            "embedding",
            extra={"extra_data": {
                "done": "{}/{}".format(done, total),
                "pct": "{:.0f}%".format(100.0 * done / total),
                "rate": "{:.1f}/s".format(rate),
                "eta": _format_duration(eta),
            }},
        )

    return vectors


def build_index(
    json_path: str | Path = SETTINGS.ingest_json_path,
    db_path: str = SETTINGS.db_path,
    batch_size: int = SETTINGS.insert_batch_size,
    model=None,
    client=None,
    embed_chunk_size: int | None = None,
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

    log.info(
        "embedding",
        extra={"extra_data": {
            "documents": len(ids),
            "chunk": embed_chunk_size or SETTINGS.embed_chunk_size,
            "batch": SETTINGS.embed_batch_size,
        }},
    )
    embeddings = _encode_in_chunks(model, documents, embed_chunk_size)

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
        log.info("storing", extra={"extra_data": {"done": "{}/{}".format(end, len(ids))}})

    total = collection.count()
    log.info("index complete", extra={"extra_data": {"documents": total, "metadata": collection.metadata}})
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
        "--progress-every",
        type=int,
        default=SETTINGS.progress_every,
        help="log a harvest progress line every N valid records",
    )
    parser.add_argument(
        "--embed-chunk-size",
        type=int,
        default=SETTINGS.embed_chunk_size,
        help="embed and log in chunks of this many documents",
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
    parser.add_argument(
        "--fresh",
        action="store_true",
        help=(
            "throw away the resume state in TEMP_DIR and start over "
            "(without it, --harvest resumes and tops up to --target)"
        ),
    )
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    configure_logging("aisen.ingest")
    parser = build_parser()
    args = parser.parse_args(argv)

    if not (args.harvest or args.index or args.all or args.query):
        parser.error("choose one of --harvest, --index, --all, --query")

    started_at = _utc_now()
    started = time.monotonic()
    counts: dict[str, int] = {}
    stage = "harvest" if (args.all or args.harvest) else ("index" if args.index else "query")
    # What this run inherited, so a history entry for a 500-record run that
    # started at 200 is explainable instead of looking like a shrunk corpus.
    resume_from: dict | None = None

    def elapsed() -> float:
        return time.monotonic() - started

    try:
        with ingest_lock():
            if args.all or args.harvest:
                stage = "harvest"
                log.info(
                    "ingest start",
                    extra={"extra_data": {"stage": "harvest", "target": args.target, "endpoint": args.endpoint}},
                )
                before = _resume_snapshot()
                if before:
                    resume_from = before
                counts["records"] = len(
                    harvest(
                        target=args.target,
                        endpoint=args.endpoint,
                        output=args.json,
                        progress_every=args.progress_every,
                        fresh=args.fresh,
                    )
                )
                log.info(
                    "stage complete",
                    extra={"extra_data": {
                        "stage": "harvest",
                        "records": counts["records"],
                        "took": _format_duration(elapsed()),
                    }},
                )
            if args.all or args.index:
                stage = "index"
                log.info(
                    "stage start",
                    extra={"extra_data": {"stage": "index", "source": args.json, "db": args.db}},
                )
                counts["documents"] = build_index(
                    json_path=args.json,
                    db_path=args.db,
                    embed_chunk_size=args.embed_chunk_size,
                )
                log.info(
                    "stage complete",
                    extra={"extra_data": {
                        "stage": "index",
                        "documents": counts["documents"],
                        "took": _format_duration(elapsed()),
                    }},
                )
            if args.query:
                stage = "query"
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
    except IngestLocked as error:
        log.error("INGEST REFUSED", extra={"extra_data": {"reason": error}})
        return 2
    except Exception as error:  # noqa: BLE001 - the last line must state the cause
        log.error(
            "INGEST FAILED",
            extra={"extra_data": {
                "stage": stage,
                "after": _format_duration(elapsed()),
                "error": "{}: {}".format(type(error).__name__, error),
            }},
            exc_info=True,
        )
        # Deliberately no history entry: `data/history.json` records runs that
        # moved the corpus forward. A failure produced nothing to point at, and
        # an entry claiming one would let `/ready` report stale success or a
        # half-finished run as if it were data.
        return 1

    _write_run_summary(
        status="ok",
        stage=stage,
        started_at=started_at,
        duration_s=elapsed(),
        counts=counts,
        resume_from=resume_from,
    )
    log.info(
        "ingest complete",
        extra={"extra_data": {
            "took": _format_duration(elapsed()),
            **{name: value for name, value in counts.items()},
        }},
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
