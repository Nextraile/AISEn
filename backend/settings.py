"""Environment-backed configuration shared by the API and the ingestion script.

Every value defaults to what used to be hardcoded in `app.py` / `ingest.py`, so
an empty environment reproduces the previous behaviour exactly. The module is
read once at import (`SETTINGS`), which is what keeps the API and `ingest.py`
from disagreeing about the model, the database path, or the threshold.

Run the service from `backend/` and pass the file with `uvicorn --env-file .env`
(Docker Compose reads the same file via `env_file`). Every variable is listed in
`.env.example`.
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Mapping

DEFAULT_EMBEDDING_MODEL = "sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2"
DEFAULT_DB_PATH = "./data/vector_db"
DEFAULT_COLLECTION = "undip_research_search"
DEFAULT_N_RESULTS = 100
DEFAULT_SUMMARY_TOP_K = 5
DEFAULT_LLM_TIMEOUT_SECONDS = 15.0
DEFAULT_LOG_LEVEL = "INFO"
DEFAULT_THRESHOLD_MIN = 0.1
DEFAULT_THRESHOLD_MAX = 0.9
DEFAULT_THRESHOLD_DEFAULT = 0.53
DEFAULT_CORS_ALLOW_ORIGINS = ("*",)

DEFAULT_OAI_ENDPOINT = "https://eprints2.undip.ac.id/cgi/oai2"
DEFAULT_INGEST_TARGET = 1000
DEFAULT_INGEST_JSON_PATH = "data/documents.json"
DEFAULT_INSERT_BATCH_SIZE = 1000
DEFAULT_EMBED_BATCH_SIZE = 64
DEFAULT_MIN_DESCRIPTION_CHARS = 50

LOG_LEVELS = ("DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL")
CHROMA_MAX_BATCH = 5461 # ChromaDB rejects insert batches above roughly 5,461 records.


def _text(env: Mapping[str, str], name: str, default: str) -> str:
    """Read a string, treating an empty value as 'not set'."""
    value = env.get(name)
    if value is None or value.strip() == "":
        return default
    return value.strip()


def _number(env: Mapping[str, str], name: str, default, cast):
    raw = env.get(name)
    if raw is None or raw.strip() == "":
        return default
    try:
        value = cast(raw)
    except (TypeError, ValueError):
        raise ValueError(
            f"{name}={raw!r} tidak valid: harus {cast.__name__}."
        ) from None
    return value


def _int(env: Mapping[str, str], name: str, default: int) -> int:
    return _number(env, name, default, int)


def _float(env: Mapping[str, str], name: str, default: float) -> float:
    return _number(env, name, default, float)


def _csv(env: Mapping[str, str], name: str, default: tuple[str, ...]) -> tuple[str, ...]:
    """Comma-separated list; an empty value keeps the default."""
    raw = env.get(name)
    if raw is None or raw.strip() == "":
        return default
    items = tuple(part.strip() for part in raw.split(",") if part.strip())
    return items or default


@dataclass(frozen=True)
class Settings:
    """Runtime configuration. Build with `Settings.from_env()`, never directly."""

    model_name: str = DEFAULT_EMBEDDING_MODEL
    db_path: str = DEFAULT_DB_PATH
    collection_name: str = DEFAULT_COLLECTION
    n_results: int = DEFAULT_N_RESULTS
    summary_top_k: int = DEFAULT_SUMMARY_TOP_K
    llm_timeout_seconds: float = DEFAULT_LLM_TIMEOUT_SECONDS
    log_level: str = DEFAULT_LOG_LEVEL
    threshold_min: float = DEFAULT_THRESHOLD_MIN
    threshold_max: float = DEFAULT_THRESHOLD_MAX
    threshold_default: float = DEFAULT_THRESHOLD_DEFAULT
    cors_allow_origins: tuple[str, ...] = DEFAULT_CORS_ALLOW_ORIGINS

    oai_endpoint: str = DEFAULT_OAI_ENDPOINT
    ingest_target: int = DEFAULT_INGEST_TARGET
    ingest_json_path: str = DEFAULT_INGEST_JSON_PATH
    insert_batch_size: int = DEFAULT_INSERT_BATCH_SIZE
    embed_batch_size: int = DEFAULT_EMBED_BATCH_SIZE
    min_description_chars: int = DEFAULT_MIN_DESCRIPTION_CHARS

    def validate(self) -> "Settings":
        """Fail fast on combinations the service cannot honour.

        Called by `from_env()`, so a bad `.env` stops startup with a clear
        message instead of surfacing as a 422 or a silently useless threshold.
        """
        problems: list[str] = []

        if not (0.0 <= self.threshold_min < self.threshold_max <= 1.0):
            problems.append(
                "THRESHOLD_MIN/THRESHOLD_MAX harus 0 <= min < max <= 1 "
                f"(sekarang {self.threshold_min} dan {self.threshold_max})."
            )
        if not (self.threshold_min < self.threshold_default < self.threshold_max):
            problems.append(
                "THRESHOLD_DEFAULT harus di antara THRESHOLD_MIN dan "
                f"THRESHOLD_MAX (sekarang {self.threshold_default})."
            )
        if self.n_results < 1:
            problems.append("SEARCH_N_RESULTS harus >= 1.")
        if self.summary_top_k < 1 or self.summary_top_k > self.n_results:
            problems.append(
                "SUMMARY_TOP_K harus >= 1 dan <= SEARCH_N_RESULTS "
                f"(sekarang {self.summary_top_k})."
            )
        if self.llm_timeout_seconds <= 0:
            problems.append("LLM_TIMEOUT_SECONDS harus > 0.")
        if self.log_level not in LOG_LEVELS:
            problems.append(
                f"LOG_LEVEL harus salah satu dari {', '.join(LOG_LEVELS)}."
            )
        if not 1 <= self.insert_batch_size <= CHROMA_MAX_BATCH:
            problems.append(
                f"INSERT_BATCH_SIZE harus 1..{CHROMA_MAX_BATCH} (batas ChromaDB)."
            )
        if self.embed_batch_size < 1:
            problems.append("EMBED_BATCH_SIZE harus >= 1.")
        if self.ingest_target < 1:
            problems.append("INGEST_TARGET harus >= 1.")
        if self.min_description_chars < 0:
            problems.append("MIN_DESCRIPTION_CHARS tidak boleh negatif.")

        if problems:
            raise ValueError("Konfigurasi tidak valid:\n- " + "\n- ".join(problems))
        return self

    @classmethod
    def from_env(cls, env: Mapping[str, str] | None = None) -> "Settings":
        """Build settings from a mapping (defaults to `os.environ`)."""
        source = os.environ if env is None else env
        return cls(
            model_name=_text(source, "EMBEDDING_MODEL", DEFAULT_EMBEDDING_MODEL),
            db_path=_text(source, "CHROMA_DB_PATH", DEFAULT_DB_PATH),
            collection_name=_text(source, "CHROMA_COLLECTION", DEFAULT_COLLECTION),
            n_results=_int(source, "SEARCH_N_RESULTS", DEFAULT_N_RESULTS),
            summary_top_k=_int(source, "SUMMARY_TOP_K", DEFAULT_SUMMARY_TOP_K),
            llm_timeout_seconds=_float(
                source, "LLM_TIMEOUT_SECONDS", DEFAULT_LLM_TIMEOUT_SECONDS
            ),
            log_level=_text(source, "LOG_LEVEL", DEFAULT_LOG_LEVEL).upper(),
            threshold_min=_float(source, "THRESHOLD_MIN", DEFAULT_THRESHOLD_MIN),
            threshold_max=_float(source, "THRESHOLD_MAX", DEFAULT_THRESHOLD_MAX),
            threshold_default=_float(
                source, "THRESHOLD_DEFAULT", DEFAULT_THRESHOLD_DEFAULT
            ),
            cors_allow_origins=_csv(
                source, "CORS_ALLOW_ORIGINS", DEFAULT_CORS_ALLOW_ORIGINS
            ),
            oai_endpoint=_text(source, "OAI_ENDPOINT", DEFAULT_OAI_ENDPOINT),
            ingest_target=_int(source, "INGEST_TARGET", DEFAULT_INGEST_TARGET),
            ingest_json_path=_text(
                source, "INGEST_JSON_PATH", DEFAULT_INGEST_JSON_PATH
            ),
            insert_batch_size=_int(
                source, "INSERT_BATCH_SIZE", DEFAULT_INSERT_BATCH_SIZE
            ),
            embed_batch_size=_int(
                source, "EMBED_BATCH_SIZE", DEFAULT_EMBED_BATCH_SIZE
            ),
            min_description_chars=_int(
                source, "MIN_DESCRIPTION_CHARS", DEFAULT_MIN_DESCRIPTION_CHARS
            ),
        ).validate()


SETTINGS = Settings.from_env()
