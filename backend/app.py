"""HTTP API for the Undip semantic search demo.

Conventions in this file:

- Wire format is Indonesian (`peringkat`, `skor`, `judul`, ...) because the
  frontend reads those exact keys; internal names stay English.
- Anything a student can read (summary, `arahan`, `ringkasan_error`) is written
  in Indonesian. Everything a developer reads (logs, comments) is English.
"""

import asyncio
import json
import os
import re
import time
import uuid
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Literal

import chromadb
from fastapi import FastAPI, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import JSONResponse
from pydantic import AliasChoices, BaseModel, ConfigDict, Field, computed_field
from starlette.concurrency import run_in_threadpool

from logging_setup import configure_logging
from settings import SETTINGS

# Threshold bounds are captured by `SearchRequest` when the class is created,
# so they must be module-level names rather than read from `SETTINGS` inline.
THRESHOLD_MIN = SETTINGS.threshold_min
THRESHOLD_MAX = SETTINGS.threshold_max
THRESHOLD_DEFAULT = SETTINGS.threshold_default

APP_VERSION = "0.3.0"

GUIDANCE = [
    "Coba kalimat yang lebih spesifik.",
    "Perluas topik pencarian.",
]

log = configure_logging("aisen.search")

LAST_INGEST_PATH = Path(SETTINGS.ingest_json_path).parent / "last_ingest.json"


def _last_ingest() -> dict | None:
    """Read the summary the ingestion CLI wrote, if it is reachable.

    The container mounts `./data`, so this is the same file `ingest.py` wrote on
    the host. A missing file just means "no ingest recorded yet".
    """
    try:
        return json.loads(LAST_INGEST_PATH.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None


def _uptime_seconds(target_app: FastAPI) -> float | None:
    started = getattr(target_app.state, "started_at", None)
    return None if started is None else round(time.monotonic() - started, 1)


def _log_configuration() -> None:
    log.info(
        "config: model=%s collection=%s db=%s n_results=%d threshold=%.2f "
        "llm_timeout=%ss log_level=%s log_format=%s",
        SETTINGS.model_name,
        SETTINGS.collection_name,
        SETTINGS.db_path,
        SETTINGS.n_results,
        SETTINGS.threshold_default,
        SETTINGS.llm_timeout_seconds,
        SETTINGS.log_level,
        SETTINGS.log_format,
    )


def _open_collection(target_app: FastAPI) -> None:
    """Open the Chroma collection and warn about anything that would make scores meaningless."""
    # Log before touching the DB: on a network share (e.g. \\wsl.localhost from
    # Windows) SQLite can hang silently, and this line marks where it stopped.
    log.info("Opening ChromaDB at '%s'...", SETTINGS.db_path)
    client = chromadb.PersistentClient(path=SETTINGS.db_path)

    try:
        target_app.state.collection = client.get_collection(
            name=SETTINGS.collection_name
        )
    except chromadb.errors.NotFoundError as exc:
        raise RuntimeError(
            f"ChromaDB collection '{SETTINGS.collection_name}' was not found in "
            f"'{SETTINGS.db_path}'. Check CHROMA_DB_PATH and run "
            "`python ingest.py --all` first."
        ) from exc

    collection = target_app.state.collection
    total_documents = collection.count()
    log.info(
        "ChromaDB ready: %d documents in collection '%s' (%s)",
        total_documents,
        SETTINGS.collection_name,
        SETTINGS.db_path,
    )
    if total_documents == 0:
        log.warning(
            "Collection '%s' is EMPTY: every search will return 'no_results' "
            "even though the startup checks pass. Build the index with "
            "`python ingest.py --all` on the host, or from Docker with "
            "`docker compose --profile tools run --rm tools ingest.py --all`.",
            SETTINGS.collection_name,
        )

    metadata = collection.metadata or {}
    if metadata.get("hnsw:space") != "cosine":
        log.warning(
            "Collection '%s' does not use hnsw:space=cosine: similarity scores "
            "are meaningless (a threshold on the 0.1-0.9 scale has no meaning).",
            SETTINGS.collection_name,
        )

    # Stored vectors and query vectors must come from the same model. Changing
    # EMBEDDING_MODEL without `ingest.py --index` still returns results, but the
    # scores are computed in two different spaces.
    recorded_model = metadata.get("embedding_model")
    if recorded_model and recorded_model != SETTINGS.model_name:
        log.warning(
            "Collection '%s' was built with model '%s' but EMBEDDING_MODEL is "
            "now '%s'. Re-run `python ingest.py --index`.",
            SETTINGS.collection_name,
            recorded_model,
            SETTINGS.model_name,
        )


def _load_model(target_app: FastAPI) -> None:
    """Create the embedding model and pick up the optional LLM configuration."""
    log.info(
        "Loading embedding model '%s' (the first download can take a while)...",
        SETTINGS.model_name,
    )

    # Imported here so tests that inject a fake model never pay for the torch
    # import, and so uvicorn logs the DB check above before the slow step.
    from sentence_transformers import SentenceTransformer

    target_app.state.model = SentenceTransformer(SETTINGS.model_name)
    log.info("Embedding model ready.")
    target_app.state.summarize = summarize_with_llm

    gemini_model = os.getenv("GEMINI_MODEL")
    if not (os.getenv("GEMINI_API_KEY") and gemini_model):
        log.warning(
            "GEMINI_API_KEY / GEMINI_MODEL are not set: AI summaries are "
            "disabled and the API returns the journal list only."
        )
    else:
        log.info("AI summaries enabled with Gemini model '%s'.", gemini_model)


@asynccontextmanager
async def lifespan(app: FastAPI):
    app.state.started_at = time.monotonic()
    _log_configuration()
    _open_collection(app)
    _load_model(app)
    yield


app = FastAPI(
    title="Undip Research Semantic Search API",
    description="Semantic search over the harvested Undip eprints2 repository.",
    version=APP_VERSION,
    lifespan=lifespan,
)

app.add_middleware(
    CORSMiddleware,
    allow_origins=list(SETTINGS.cors_allow_origins),
    allow_credentials=False,
    allow_methods=["*"],
    allow_headers=["*"],
)


@app.middleware("http")
async def request_context(request: Request, call_next):
    """Tag every request with an id and log one line per completed request.

    The id is echoed back in `X-Request-ID` and appears in the 500 body, so a
    user can quote one string that ties their report to this log.
    """
    request_id = request.headers.get("X-Request-ID") or uuid.uuid4().hex[:12]
    request.state.request_id = request_id
    started = time.perf_counter()

    try:
        response = await call_next(request)
    except Exception:  # noqa: BLE001 - logged with the id, re-raised to the handle
        log.exception(
            "request failed request_id=%s %s %s",
            request_id,
            request.method,
            request.url.path,
        )
        raise

    duration_ms = (time.perf_counter() - started) * 1000
    response.headers["X-Request-ID"] = request_id
    log.info(
        "request request_id=%s method=%s path=%s status=%d duration_ms=%.0f",
        request_id,
        request.method,
        request.url.path,
        response.status_code,
        duration_ms,
    )
    return response


@app.exception_handler(Exception)
async def unhandled_exception(request: Request, exc: Exception) -> JSONResponse:
    """Turn an unexpected error into a JSON 500 that carries the request id."""
    request_id = getattr(request.state, "request_id", "-")
    log.exception("unhandled error request_id=%s", request_id, exc_info=exc)
    return JSONResponse(
        status_code=500,
        content={
            "detail": "Terjadi kesalahan internal. Coba lagi.",
            "request_id": request_id,
        },
        headers={"X-Request-ID": request_id},
    )


class SearchRequest(BaseModel):
    model_config = ConfigDict(str_strip_whitespace=True)

    query: str = Field(
        ..., min_length=1, description="Free-text search query (Indonesian or English)."
    )
    threshold: float = Field(
        default=THRESHOLD_DEFAULT,
        validation_alias=AliasChoices("threshold", "max_threshold"),
        ge=THRESHOLD_MIN,
        le=THRESHOLD_MAX,
        description=(
            "Minimum similarity (1 - cosine distance). Higher = stricter. "
            "`max_threshold` is accepted as a deprecated alias."
        ),
    )


class SearchResult(BaseModel):
    peringkat: int  # 1-based position after threshold filtering
    skor: float  # similarity, higher = close
    judul: str
    penulis: str
    deskripsi: str
    url: str
    date: str


class SearchResponse(BaseModel):
    query: str
    threshold: float
    count: int
    results: list[SearchResult]
    # full       : AI summary + journal list
    # list_only  : LLM failed, journal list only (reason in `ringkasan_error`)
    # no_results : nothing passed the threshold, see `arahan`
    status: Literal["full", "list_only", "no_results"]
    ringkasan: str | None = None  # citations are [n], n = `peringkat`
    ringkasan_error: str | None = None  # message for the user; they retry manually
    # Best similarity found even though it missed the threshold. It tells apart
    # "threshold is very strict" from "the database is empty".
    skor_tertinggi: float | None = None
    arahan: list[str] = Field(default_factory=list)

    # `type: ignore` below is the known mypy/Pydantic ordering quirk around
    # `@computed_field` on a property, not a suppressed bug.
    @computed_field  # type: ignore[prop-decorator]
    @property
    def max_threshold(self) -> float:
        """Legacy alias for `threshold` used by the frontend. Always equal to it."""
        return self.threshold


class LLMError(Exception):
    """The message is shown to users, so write it in Indonesian."""


class SummaryError(Exception):
    """Internal signal: the summary step failed, mapped to `ringkasan_error`."""


PROMPT_TEMPLATE = (
    "Anda adalah asisten riset. Tulis ringkasan singkat dalam bahasa Indonesia "
    "tentang isi, temuan utama, atau pola yang muncul dari jurnal yang disajikan. "
    "Gunakan HANYA sumber di bawah ini; jangan menambah fakta atau angka. "
    "Panjang maksimal 3 paragraf dan 180 kata. Utamakan poin paling relevan, "
    "hindari pengantar panjang dan jangan mengulang judul jurnal. "
    "Setiap klaim faktual wajib memiliki sitasi [n] sesuai nomor sumber. "
    "Tulis sitasi satu per satu, misalnya [1] [2] [3], jangan digabung "
    "menjadi satu kurung seperti [1, 2, 3]. "
    "Gunakan Markdown sederhana hanya untuk penekanan penting (**teks**) bila perlu. "
    "Jika sumber tidak cukup, katakan secara singkat.\n\n"
    "Pertanyaan pengguna: {query}\n\nSumber jurnal:\n{sources}"
)


def build_prompt(query: str, results: list[SearchResult]) -> str:
    sources = "\n\n".join(f"[{r.peringkat}] {r.judul}\n{r.deskripsi}" for r in results)
    return PROMPT_TEMPLATE.format(query=query, sources=sources)


_CITATION = re.compile(r"\[(\d+(?:\s*,\s*\d+)*)\]")


def sanitize_citations(text: str, valid: set[int]) -> str:
    """Drop citations whose number is not among the sources sent to the LLM.

    Combined citations such as [1, 2, 3] are split into [1] [2] [3] so each
    number can be clicked separately in the UI.
    """

    def keep(match: re.Match) -> str:
        numbers = [int(n) for n in match.group(1).split(",")]
        kept = [n for n in numbers if n in valid]
        dropped = [n for n in numbers if n not in valid]
        if dropped:
            log.warning("Dropped invalid citations: %s", dropped)
        return " ".join(f"[{n}]" for n in kept)

    return _CITATION.sub(keep, text)


def summarize_with_llm(query: str, results: list[SearchResult]) -> str:
    """Call Gemini. No automatic retry: failures become `LLMError` and the user retries."""
    api_key = os.getenv("GEMINI_API_KEY")
    model = os.getenv("GEMINI_MODEL")
    if not (api_key and model):
        raise LLMError("Ringkasan AI belum dikonfigurasi.")

    from google import genai
    from google.genai import errors as genai_errors

    try:
        client = genai.Client(api_key=api_key)
        resp = client.models.generate_content(
            model=model, contents=build_prompt(query, results)
        )
    except genai_errors.APIError as exc:
        if exc.code == 429:
            raise LLMError(
                "Kuota atau batas permintaan LLM tercapai. Coba lagi sebentar lagi."
            ) from exc
        if exc.code == 404:
            # Most often GEMINI_MODEL is wrong or the model was retired.
            raise LLMError(
                f"Model LLM '{model}' tidak ditemukan. Periksa nilai GEMINI_MODEL."
            ) from exc
        raise LLMError(
            f"Layanan LLM mengembalikan error ({exc.code}). Coba lagi."
        ) from exc

    text = (resp.text or "").strip()
    if not text:
        raise LLMError("LLM tidak menghasilkan ringkasan. Coba lagi.")
    return sanitize_citations(text, {r.peringkat for r in results})


@app.get("/health")
def health() -> dict:
    """Liveness probe: is the process up and serving HTTP?

    Deliberately does no database work, so the Compose healthcheck cannot put a
    `COUNT` over 48k rows on a 30-second timer, and a broken index never causes
    a restart loop. Use `/ready` for "can it actually answer searches?".
    """
    return {
        "status": "ok",
        "version": APP_VERSION,
        "uptime_s": _uptime_seconds(app),
    }


@app.get("/ready")
def ready() -> JSONResponse:
    """Readiness probe: is there a usable index behind this process?

    Returns 503 with a `problems` list when the collection is missing, empty,
    not cosine, or built with a different model. `last_ingest` is the summary
    the CLI wrote, so "the last ingest failed" is visible without reading logs.
    """
    problems: list[str] = []
    documents = 0
    metadata: dict = {}

    try:
        collection = app.state.collection
        documents = collection.count()
        metadata = collection.metadata or {}
    except Exception as exc:  # noqa: BLE001 - readiness must report, not raise
        problems.append(f"collection unavailable: {exc}")

    if not problems:
        if documents == 0:
            problems.append(
                "collection is empty: run `python ingest.py --all` (or the tools "
                "profile) before serving searches"
            )
        if metadata.get("hnsw:space") != "cosine":
            problems.append(
                "collection does not use hnsw:space=cosine: similarity scores "
                "are meaningless"
            )
        recorded_model = metadata.get("embedding_model")
        if recorded_model and recorded_model != SETTINGS.model_name:
            problems.append(
                f"collection was built with model '{recorded_model}' but "
                f"EMBEDDING_MODEL is '{SETTINGS.model_name}': re-run ingest.py --index"
            )

    last_ingest = _last_ingest()
    if last_ingest and last_ingest.get("status") == "failed":
        problems.append(
            f"last ingest failed: {last_ingest.get('error') or 'unknown error'}"
        )

    payload = {
        "status": "ok" if not problems else "not_ready",
        "documents": documents,
        "metric": metadata.get("hnsw:space"),
        "model": SETTINGS.model_name,
        "version": APP_VERSION,
        "uptime_s": _uptime_seconds(app),
        "llm_configured": bool(
            os.getenv("GEMINI_API_KEY") and os.getenv("GEMINI_MODEL")
        ),
        "last_ingest": last_ingest,
        "problems": problems,
    }
    return JSONResponse(status_code=200 if not problems else 503, content=payload)


def metadata_text(metadata: dict | None, key: str) -> str:
    """Read one metadata field as a string, never raising.

    The metadata shape belongs to `ingest.py`; a single document missing a key,
    or carrying a non-string value, must not turn the whole search into a 500.
    """
    if not metadata:  # None or {} (document stored without metadata)
        return ""
    value = metadata.get(key)
    return "" if value is None else str(value)


def rank_results(
    metadatas: list, distances: list, min_similarity: float
) -> list[SearchResult]:
    """Filter by similarity and build the ranked result list.

    Shared with `ingest.query_demo()`, so `--query` reports exactly what
    POST /search would return.
    """
    results: list[SearchResult] = []
    for metadata, distance in zip(metadatas, distances):
        similarity = round(1.0 - distance, 6)  # rounded so comparisons are stable
        if similarity < min_similarity:
            continue
        results.append(
            SearchResult(
                peringkat=len(results) + 1,
                skor=similarity,
                judul=metadata_text(metadata, "title"),
                penulis=metadata_text(metadata, "creators"),
                deskripsi=metadata_text(metadata, "description"),
                url=metadata_text(metadata, "url"),
                date=metadata_text(metadata, "date"),
            )
        )
    return results


def _no_results_hints(
    threshold: float, distances: list
) -> tuple[float | None, list[str]]:
    """Explain an empty result set without ever suggesting a lower threshold."""
    if not distances:
        return None, ["Belum ada dokumen terindeks di basis data."]
    best = round(1.0 - distances[0], 6)
    return best, [
        f"Tidak ada jurnal yang mencapai ambang kemiripan {threshold:.2f}.",
        f"Kemiripan tertinggi yang ditemukan: {best:.2f}.",
        *GUIDANCE,
    ]


async def _summarize(query: str, results: list[SearchResult]) -> str:
    """Run the summary step with a timeout, translating failures for the user."""
    try:
        return await asyncio.wait_for(
            run_in_threadpool(
                app.state.summarize, query, results[: SETTINGS.summary_top_k]
            ),
            timeout=SETTINGS.llm_timeout_seconds,
        )
    except asyncio.TimeoutError:
        log.warning("LLM timed out after %ss", SETTINGS.llm_timeout_seconds)
        raise SummaryError("LLM tidak merespons tepat waktu. Coba lagi.") from None
    except LLMError as exc:
        log.warning("LLM failed: %s", exc)
        raise SummaryError(str(exc)) from exc
    except Exception:
        log.exception("LLM failed (unexpected error)")
        raise SummaryError("Ringkasan gagal dibuat. Coba lagi.") from None


@app.post("/search", response_model=SearchResponse)
async def search(payload: SearchRequest, request: Request) -> SearchResponse:
    started = time.perf_counter()

    embedding = await run_in_threadpool(app.state.model.encode, payload.query)
    raw = await run_in_threadpool(
        app.state.collection.query,
        query_embeddings=[embedding.tolist()],
        n_results=SETTINGS.n_results,
        include=["metadatas", "distances"],
    )

    metadatas = raw["metadatas"][0] if raw["metadatas"] else []
    distances = raw["distances"][0] if raw["distances"] else []
    results = rank_results(metadatas, distances, payload.threshold)

    ringkasan = None
    ringkasan_error = None
    skor_tertinggi = None
    arahan: list[str] = []

    if not results:
        status = "no_results"
        skor_tertinggi, arahan = _no_results_hints(payload.threshold, distances)
    else:
        try:
            ringkasan = await _summarize(payload.query, results)
            status = "full"
        except SummaryError as exc:
            status = "list_only"
            ringkasan_error = str(exc)

    log.info(
        "search request_id=%s status=%s count=%d threshold=%.2f duration_ms=%.0f",
        getattr(request.state, "request_id", "-"),
        status,
        len(results),
        payload.threshold,
        (time.perf_counter() - started) * 1000,
    )

    return SearchResponse(
        query=payload.query,
        threshold=payload.threshold,
        count=len(results),
        results=results,
        status=status,
        ringkasan=ringkasan,
        ringkasan_error=ringkasan_error,
        skor_tertinggi=skor_tertinggi,
        arahan=arahan,
    )
