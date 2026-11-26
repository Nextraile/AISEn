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
import hashlib
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
import numpy as np

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
        "records_skipped": counts.get("skipped"),
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
    stats: dict | None = None,
) -> list[dict]:
    """Collect `target` valid records and write them as JSON.

    The run is resumable. Every page is appended to `data/temp/harvest.jsonl`
    and fsynced, then the cursor of the page just read is stored in
    `data/temp/harvest_state.json`; an interrupted harvest therefore loses at
    most the page in flight and the next run continues from that cursor instead
    of re-fetching the corpus.

    A record that cannot be parsed is skipped and logged, not fatal: one bad
    record must not throw away a stage that has already read 40,000 good ones.
    `stats`, when given, is filled with `skipped` and `scanned` so the run
    summary can report them.

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
        discarded, _ = _read_journal(journal) if journal.exists() else ([], set())
        for path in (journal, state_path):
            path.unlink(missing_ok=True)
        if discarded:
            log.info(
                "harvest journal discarded",
                extra={"extra_data": {"records": len(discarded)}},
            )

    items, seen = _read_journal(journal)
    state = _load_harvest_state(state_path) or {}
    token = state.get("next_token") or None
    scanned = int(state.get("scanned") or 0)
    resumed_at = len(items)
    skipped = 0

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
                "skipped": skipped,
                "resumed_from": resumed_at,
                "out": out,
            }},
        )
        if stats is not None:
            stats.update({"scanned": scanned, "skipped": skipped})
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
                continue  # a deleted record carries no metadata: not an error

            identifier = getattr(getattr(record, "header", None), "identifier", "")

            # Both the filter and the mapping read fields that a broken record
            # can leave malformed (a non-list <dc:creator>, a value that is not
            # a string), so both are guarded: one bad record must not abort a
            # 48k-record harvest. Skipping is safe because the cursor is
            # page-granular, so the record will not be fetched again; the
            # warning is the only trace, which is why it carries the identifier.
            try:
                if not is_valid_record(metadata):
                    continue
                item = record_to_item(metadata)
            except Exception as error:  # noqa: BLE001 - any failure is one bad record
                skipped += 1
                log.warning(
                    "harvest skipped record",
                    extra={"extra_data": {
                        "id": identifier or "-",
                        "reason": "{}: {}".format(type(error).__name__, error),
                    }},
                )
                continue

            if identifier and identifier in seen:
                continue

            if identifier:
                seen.add(identifier)

            items.append(item)
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
            "skipped": skipped,
            "resumed_from": resumed_at,
            "out": out,
        }},
    )
    if stats is not None:
        stats.update({"scanned": scanned, "skipped": skipped})
    return items


# ---- index -----------------------------------------------------------------
def index_state_path(temp_dir: str | Path | None = None) -> Path:
    """Where an interrupted index build leaves its phase marker and count."""
    return Path(temp_dir or SETTINGS.temp_dir) / "index_state.json"


def _corpus_fingerprint(documents: Sequence[str]) -> str:
    """Identify the corpus a build was started from by the text it embeds.

    A resumed build must not mix vectors from two different harvests: the ids
    are positional (`doc_<index>`), so re-harvesting can rename what `doc_7`
    means. Only the embedded text goes in, in corpus order, so a change to
    `creators`, `date` or `url` alone does not invalidate the index while any
    change to what a vector was computed from does.

    The text is hashed with NUL separators (`\\x00` cannot occur inside a
    title or abstract), so the hash covers both the strings and their order and
    one string cannot shift into the next. An identical re-harvest therefore
    compares equal on purpose: the bytes of `data/documents.json` may differ
    (mtime always does), but what the index would contain does not.
    """
    digest = hashlib.sha256()
    for text in documents:
        digest.update(text.encode("utf-8"))
        digest.update(b"\x00")
    return digest.hexdigest()


def _load_index_state(path: Path) -> dict | None:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None


def _write_index_state(path: Path, payload: dict) -> None:
    _atomic_write(path, json.dumps(payload, indent=2) + "\n")


def _resumable_stored_ids(
    client,
    *,
    fingerprint: str,
    state: dict | None,
    fresh: bool,
    model_name: str,
) -> set[str]:
    """The ids already in the collection, if this build may continue from them.

    Returns an empty set when the collection must be rebuilt from scratch, and
    in that case also deletes it, so the caller only has to add what it is
    told is missing. Resuming is legitimate only when a previous build of the
    *same* corpus with the *same* model was interrupted: any other state means
    the collection is a complete answer to a different question.
    """
    if fresh or state is None or state.get("phase") != "building":
        try:
            client.delete_collection(name=SETTINGS.collection_name)
        except chromadb.errors.NotFoundError:
            pass  # first build: there is nothing to replace
        return set()

    if state.get("fingerprint") != fingerprint or state.get("model") != model_name:
        log.info(
            "index restart",
            extra={"extra_data": {
                "reason": "corpus or model changed since the partial build",
                "started_with": state.get("fingerprint"),
                "now": fingerprint,
            }},
        )
        try:
            client.delete_collection(name=SETTINGS.collection_name)
        except chromadb.errors.NotFoundError:
            pass
        return set()

    collection = client.get_or_create_collection(
        name=SETTINGS.collection_name,
        metadata={
            "hnsw:space": "cosine",
            "embedding_model": SETTINGS.model_name,
            "corpus_hash": fingerprint,
        },
    )
    stored = set(collection.get(include=[])["ids"])
    log.info(
        "index resume",
        extra={"extra_data": {
            "stored": len(stored),
            "target": state.get("target"),
        }},
    )
    return stored


def _existing_collection(client):
    """The collection if it is already there, else `None`.

    Reading it back is not the same as `get_or_create_collection`: asking for
    the metadata must not create an empty collection as a side effect, or a
    first build would look like it had something to resume from.
    """
    try:
        return client.get_collection(name=SETTINGS.collection_name)
    except chromadb.errors.NotFoundError:
        return None


def _encode_in_chunks(
    model,
    documents: Sequence[str],
    chunk_size: int | None = None,
    cache_path: Path | None = None,
) -> list[list[float]]:
    """Embed `documents` in chunks, logging after each one.

    A single `model.encode(...)` over ~48k abstracts is tens of minutes with no
    output, which is indistinguishable from a hang. Encoding in chunks also
    bounds peak memory: the full matrix as Python floats is hundreds of MB.

    Encoding is the longest stage and the one worth resuming, so a build may
    pass `cache_path`: finished chunks are appended there (see `_append_vectors`)
    and a rerun skips straight past them. The cache is keyed by caller to one
    corpus and one model, and it is deleted once the rows are in the collection.
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
        if cache_path is not None:
            _append_vectors(cache_path, vectors[len(vectors) - len(chunk) :])
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


def _append_vectors(cache_path: Path, rows: Sequence[Sequence[float]]) -> None:
    """Append embedded rows to the binary cache, readable by row count alone.

    `float32` is what the model produces anyway, and the row length is stored
    once so a resumed run can tell how many rows survived a kill: a partial
    tail is truncated to the last whole row rather than reused as a vector.
    """
    width = len(rows[0]) if rows else 0
    if not width:
        return
    cache_path.parent.mkdir(parents=True, exist_ok=True)
    header = cache_path.parent / (cache_path.name + ".width")
    if not header.exists():
        header.write_text(str(width), encoding="utf-8")
    matrix = np.asarray(rows, dtype=np.float32)
    with cache_path.open("ab") as handle:
        handle.write(matrix.tobytes())
        handle.flush()
        os.fsync(handle.fileno())


def _cached_vectors(cache_path: Path, expected_rows: int) -> list[list[float]]:
    """Read the rows that reached the disk, dropping any partial tail row."""
    try:
        width = int((cache_path.parent / (cache_path.name + ".width")).read_text())
        raw = np.fromfile(cache_path, dtype=np.float32)
    except (OSError, ValueError):
        return []
    if not width or raw.size < width:
        return []
    rows = int(raw.size // width)
    if rows > expected_rows:
        rows = expected_rows
    return raw[: rows * width].reshape(rows, width).tolist()


def _clear_vectors(cache_path: Path) -> None:
    cache_path.unlink(missing_ok=True)
    (cache_path.parent / (cache_path.name + ".width")).unlink(missing_ok=True)


def build_index(
    json_path: str | Path = SETTINGS.ingest_json_path,
    db_path: str = SETTINGS.db_path,
    batch_size: int = SETTINGS.insert_batch_size,
    model=None,
    client=None,
    embed_chunk_size: int | None = None,
    fresh: bool = False,
    temp_dir: str | Path | None = None,
) -> int:
    """(Re)build the Chroma collection from the harvested JSON.

    Resumable, in two phases recorded in `data/temp/index_state.json`:

    * `encoding` -- the vectors are being produced. Finished chunks are kept in
      `data/temp/index_vectors.npy`, so an interrupted embedding run continues
      from the last complete chunk instead of starting the model again;
    * `building` -- the vectors are being inserted. Rows already in the
      collection are read back and skipped, so an interrupted insert resumes
      without re-embedding anything.

    A resume is only allowed when the state names the same corpus (hash of the
    embedded titles and abstracts) and the same model. Anything else --
    including `--fresh`, which is the only way to force a rebuild -- deletes the
    collection and starts over: `doc_<index>` ids are positional, so mixing
    vectors from two harvests would silently attach the wrong abstract to a
    vector.

    A completed build leaves no resume state behind, so the collection's own
    `corpus_hash` metadata answers "which corpus is in here". When it matches
    and the row count is the whole corpus, the build returns without loading
    the model at all: re-harvesting unchanged data no longer re-embeds 48k
    abstracts.

    `model` and `client` are injectable for tests.
    """
    source = Path(json_path)
    data = json.loads(source.read_text(encoding="utf-8"))
    if not data:
        raise ValueError(f"{json_path} is empty: run --harvest first")

    model_name = SETTINGS.model_name
    state_path = index_state_path(temp_dir)
    cache_path = state_path.parent / "index_vectors.npy"

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

    # Built once, then both hashed and embedded: `documents` is exactly what the
    # vectors are computed from, and the fingerprint only needs that.
    fingerprint = _corpus_fingerprint(documents)

    previous = None if fresh else _load_index_state(state_path)
    if fresh:
        _clear_vectors(cache_path)
        state_path.unlink(missing_ok=True)

    document_count = len(ids)

    # Phase 2 decides what still has to happen, so ask it before encoding: on a
    # `building`-phase resume the rows already in the collection never need a
    # vector again, and encoding them would waste the longest stage.
    client = client or chromadb.PersistentClient(path=db_path)

    # A finished build leaves no resume state, only the collection, so the
    # durable record of "which corpus is in here" is the collection's own
    # metadata. Same corpus and the same row count means there is nothing to do
    # at all: the expensive stage is skipped without loading the model.
    collection = _existing_collection(client)
    # Chroma returns `None` metadata for a collection created without any.
    metadata = (collection.metadata if collection is not None else None) or {}
    if (
        collection is not None
        and not fresh
        and metadata.get("hnsw:space") == "cosine"
        and metadata.get("embedding_model") == model_name
        and metadata.get("corpus_hash") == fingerprint
        and collection.count() == document_count
    ):
        log.info(
            "index up to date",
            extra={"extra_data": {"documents": document_count}},
        )
        # A crashed build can leave resume state behind while the collection
        # already matches; drop it so a later run cannot resume against a
        # build that is in fact complete.
        _clear_vectors(cache_path)
        state_path.unlink(missing_ok=True)
        return document_count

    stored = _resumable_stored_ids(
        client,
        fingerprint=fingerprint,
        state=previous,
        fresh=fresh,
        model_name=model_name,
    )
    todo = [index for index, doc_id in enumerate(ids) if doc_id not in stored]

    log.info(
        "embedding",
        extra={"extra_data": {
            "documents": len(todo),
            "chunk": embed_chunk_size or SETTINGS.embed_chunk_size,
            "batch": SETTINGS.embed_batch_size,
        }},
    )

    # The vector cache is valid whenever it was written for this exact corpus
    # and model, whichever phase was interrupted: an insert-phase kill leaves a
    # *complete* cache behind, and re-encoding those rows would throw away the
    # cheapest resume the system has. Anything else is wiped, because appending
    # new rows onto a foreign cache would misalign it against `doc_<index>`.
    cached: list[list[float]] = []
    state_matches = previous is not None and (
        previous.get("fingerprint"),
        previous.get("model"),
    ) == (fingerprint, model_name)
    if state_matches:
        cached = _cached_vectors(cache_path, document_count)
        if cached:
            log.info(
                "embed resume",
                extra={"extra_data": {
                    "cached": len(cached),
                    "of": document_count,
                }},
            )
    else:
        _clear_vectors(cache_path)

    if model is None:
        from sentence_transformers import SentenceTransformer

        model = SentenceTransformer(model_name)

    # Phase 1: produce the vectors that are still missing.
    #
    # The cache always holds a prefix of the corpus (`doc_0`, `doc_1`, ...), so
    # it composes with `todo`: rows in the cache but already stored are dropped,
    # and rows past the cache are encoded. That keeps one rule for both kinds of
    # resume instead of two.
    cached_rows = len(cached)
    vectors: dict[int, list[float]] = {
        index: cached[index] for index in todo if index < cached_rows
    }
    wanted = [index for index in todo if index >= cached_rows]

    if wanted:
        _write_index_state(
            state_path,
            {
                "phase": "encoding",
                "fingerprint": fingerprint,
                "model": model_name,
                "target": document_count,
                "updated_at": _utc_now(),
            },
        )
        encoded = _encode_in_chunks(
            model, [documents[index] for index in wanted], embed_chunk_size, cache_path
        )
        vectors.update(dict(zip(wanted, encoded)))
    embeddings = vectors

    # Phase 2: insert. Creating the collection is done before the phase is
    # recorded, so a crash between the two reads as "nothing to resume from"
    # rather than as a build whose rows are claimed to exist.
    collection = client.get_or_create_collection(
        name=SETTINGS.collection_name,
        metadata={
            "hnsw:space": "cosine",
            "embedding_model": model_name,
            "corpus_hash": fingerprint,
        },
    )
    _write_index_state(
        state_path,
        {
            "phase": "building",
            "fingerprint": fingerprint,
            "model": model_name,
            "target": document_count,
            "updated_at": _utc_now(),
        },
    )

    if stored:
        log.info(
            "index resume",
            extra={"extra_data": {"stored": len(stored), "remaining": len(todo)}},
        )

    for start in range(0, len(todo), batch_size):
        window = todo[start : start + batch_size]
        collection.add(
            ids=[ids[index] for index in window],
            embeddings=[embeddings[index] for index in window],
            metadatas=[metadatas[index] for index in window],
            documents=[documents[index] for index in window],
        )
        log.info(
            "storing",
            extra={"extra_data": {"done": "{}/{}".format(len(stored) + start + len(window), document_count)}},
        )

    total = collection.count()
    # The corpus is in the collection now: the cache and the marker have done
    # their job, and leaving them behind would let a later run resume against a
    # build that already finished.
    _clear_vectors(cache_path)
    state_path.unlink(missing_ok=True)
    log.info("index complete", extra={"extra_data": {"documents": total, "metadata": collection.metadata}})
    return total


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
    parser.add_argument("--index", action="store_true", help="build or resume the Chroma index")
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
                harvest_stats: dict[str, int] = {}
                counts["records"] = len(
                    harvest(
                        target=args.target,
                        endpoint=args.endpoint,
                        output=args.json,
                        progress_every=args.progress_every,
                        fresh=args.fresh,
                        stats=harvest_stats,
                    )
                )
                counts["skipped"] = int(harvest_stats.get("skipped") or 0)
                log.info(
                    "stage complete",
                    extra={"extra_data": {
                        "stage": "harvest",
                        "records": counts["records"],
                        "skipped": counts["skipped"],
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
                    fresh=args.fresh,
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
