# AGENTS.md

## What this repo is

One small service: FastAPI semantic search over harvested Universitas Diponegoro (Undip) eprints. Modules are grouped by role (`common/`, `service/`, `ingestion/`, `docker/`) with top-level imports, no CI, no lockfile. Tests live in `tests/` (pytest, fake model/collection, no model download). Python 3.11.

- `service/app.py` — the service (also what the Dockerfile serves: `CMD uvicorn service.app:app`): async endpoint, embedding/query offloaded via `run_in_threadpool`, explicit `RuntimeError` at startup when the Chroma collection is missing, Gemini summary with citations and fallback to list-only. Adds a request-id middleware and a catch-all 500 handler.
- `common/config.py` — the single source of configuration: a frozen `Settings` dataclass built by `from_env()`, with `validate()` rejecting impossible combinations at import time. Defaults equal the previously hardcoded constants. `GEMINI_API_KEY` / `GEMINI_MODEL` are intentionally *not* part of it — they are optional and read per call so tests can set and clear them.
- `common/logging_setup.py` — `configure_logging(service)` installs one stream handler (text or JSON via `LOG_FORMAT`) and an optional file handler (`LOG_FILE`). Text lines are a frame, a phrase, then the data: `[07:44:54.123] [INFO] [ingest]: harvest complete | valid=47945, scanned=55575`. Three tight cells (`[INFO]`, never `[ INFO ]`), one space between them, `: ` before the phrase, ` | ` before the data, `, ` between pairs; nothing is padded to a column, so the `:` is the anchor and the phrase starts a few characters later on a `[CRITICAL]` line — a deliberate trade for a shorter line. Call sites pass the data as `log.info(phrase, extra={"extra_data": {...}})`; the formatter joins the pairs, so a call site cannot forget a comma or drift from the shape. Punctuation is classified by zone: the frame and the three dividers are structure, `key=value` is data, and a value is passed byte-for-byte unless it holds a space, quote or newline (then `json.dumps`, which is also what keeps an unquoted ` | ` unambiguous). Third-party DEBUG is indented and truncated. Both entry points use it; it is idempotent so importing `service.app` twice in tests does not double lines. The level is applied on every call, not only the first.
- `ingestion/ingest.py` — the only data ingestion path: OAI-PMH harvest → `data/documents.json` → embeddings → Chroma. Not run by the app and not in the serving image: the `tools` Compose profile rebuilds the same Dockerfile with `INGEST=1` to include it, so ingestion reuses the cached torch layer instead of downloading it twice. Takes an `flock` (`data/.ingest.lock`) so two runs cannot interleave, encodes in chunks with progress lines, and writes `data/last_ingest.json`.
- `service/watch.py` — stdlib-only `/ready` poller run as the `watch` Compose service; logs state changes and optionally POSTs to `ALERT_WEBHOOK_URL`.

## Data prerequisites (the #1 startup failure)

- `./data/vector_db` is gitignored and required at startup. If absent, the app fails to boot (`RuntimeError` in `service/app.py`). `backend/data/` itself is tracked via `data/.gitkeep` (`.gitignore` uses `/data/*` plus `!/data/.gitkeep`): a missing bind-mount source would be created as root by Docker and then be unwritable by `APP_UID`. Do not remove that placeholder or revert the ignore rules to `/data/`.
- Ingest from `backend/` with `python -m ingestion.ingest --all`: harvests OAI-PMH (`https://eprints2.undip.ac.id/cgi/oai2`, `metadataPrefix=oai_dc`, target 1000 valid records) → writes `data/documents.json` → embeds `"{title}. {description}"` and inserts into Chroma. `--harvest` / `--index` run one stage.
- `ingestion/ingest.py` **deletes and recreates** collection `undip_research_search` with `{"hnsw:space": "cosine"}`. Re-running wipes existing vectors; there is no incremental update path.
- Inserts are batched at 1000 because Chroma rejects larger batches (~5461 cap).
- `sickle` is in `requirements.txt` and imported lazily inside `harvest()`, so serving does not need it at import time.
- Embedding model (`sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2`) downloads at startup. Docker sets `HF_HOME=/model-cache` backed by the named `model_cache` volume so restarts don't re-download; local runs use the default HF cache.
- Ingestion and the app must use the same model, or stored vectors won't match query vectors. Both read one `common.config.SETTINGS` object. The collection records `embedding_model` in its metadata and `service/app.py` warns at startup when it differs from `EMBEDDING_MODEL`.

## Commands

```bash
pip install -r requirements.txt
python -m ingestion.ingest --all                     # only when data/vector_db/ is missing
uvicorn service.app:app --reload --port 8000 --env-file .env   # local dev; run from backend/
pip install -r requirements-dev.txt && pytest   # tests (no model download needed)
cp .env.example .env                       # then fill GEMINI_API_KEY and GEMINI_MODEL
docker compose -f docker/docker-compose.yml up --build        # serves service/app.py on :8000 (needs .env)
APP_UID=$(id -u) docker compose -f docker/docker-compose.yml up -d --build --wait   # writable mount, waits for health
docker compose -f docker/docker-compose.yml --profile tools run --rm tools --all      # harvest + embed, no local Python
docker compose -f docker/docker-compose.yml --profile tools run --rm tools --query "..."  # same ranking as POST /search
```

- Run uvicorn from `backend/`: `CHROMA_DB_PATH` defaults to `./data/vector_db`, relative to the CWD.
- `pytest` is the only configured tool (`pyproject.toml`, tests in `tests/`). No linter, formatter, or typechecker is configured; verify before assuming `ruff` etc. exist. To run the suite without local Python, use the `tools` service: `docker compose -f docker/docker-compose.yml --profile tools run --rm --entrypoint python tools -m pytest` (pytest is not in the image, so `pip install pytest` first inside that container). Shell quoting through `wsl.exe` from PowerShell mangles `sh -c "…"` one-liners — put multi-step commands in a script file instead.
- LLM: Google Gemini via `google-genai`, configured by env `GEMINI_API_KEY` and `GEMINI_MODEL`. Never commit `.env` (it is gitignored). If either is empty the API still works and returns the list only (`status=list_only`). No automatic retry: failures surface in `ringkasan_error` and the user retries manually.
- `requirements.txt` is range-pinned (`chromadb>=1.5,<2`, `sentence-transformers>=6,<7`); code depends on Chroma 1.x APIs. `docker/docker-compose.yml` bind-mounts `../data` (ChromaDB needs write access; `/ready` reads `last_ingest.json` from the same directory), so the host data is what the container reads; build with `APP_UID=$(id -u)` so the container can write it. The bind-mount source always exists because `data/.gitkeep` is committed — Docker-created directories are root-owned and break `APP_UID`. LLM budget is `LLM_TIMEOUT_SECONDS` (default 15; measured: real calls 1-7 s, tail past 8 s).

## Observability

- Both entry points call `common.logging_setup.configure_logging`, so `LOG_LEVEL` (previously ignored by `ingestion/ingest.py`), `LOG_FORMAT=text|json`, and `LOG_FILE` apply everywhere.
- Every request gets an `X-Request-ID` (accepted or generated), echoed in the response and included in the middleware line, the `search` line, and the JSON body of an unexpected 500. The frontend sends its own id on every request.
- Ingest cadence is configurable: `PROGRESS_EVERY` for harvest lines, `EMBED_CHUNK_SIZE` for encode lines (`--progress-every` / `--embed-chunk-size` override per run). Encode reports `done=/pct=/rate=/eta=`.
- `ingest_lock()` is a hard single-writer guard via `flock` on `data/.ingest.lock`; a second run exits with code 2 and name of the holder. Never bypass it by deleting the lock while a run is live.
- `main()` wraps the stages: the last line is `ingest complete | took=…` or `INGEST FAILED | stage=…, error="<Type>: <msg>"`, and `data/last_ingest.json` records status/stage/timings/counts/error. `/ready` surfaces that file.
- The `watch` Compose service polls `/ready` every 30 s and logs only transitions; `ALERT_WEBHOOK_URL` adds a webhook notification.
- Both Compose services cap logs with `json-file` `max-size: 10m`, `max-file: 3`.

## API contract

- `POST /search`. Request `{query: str, min_length=1 (whitespace is stripped), threshold: float = THRESHOLD_DEFAULT, THRESHOLD_MIN <= threshold <= THRESHOLD_MAX}`. `max_threshold` is accepted as a deprecated alias for the existing frontend; it uses the same minimum-similarity scale, never a distance.
- `threshold` is a **minimum similarity** (`1 - cosine distance`); higher = stricter (matches the UI slider). The bounds and default are env settings (`THRESHOLD_MIN` / `THRESHOLD_MAX` / `THRESHOLD_DEFAULT`, defaults 0.1 / 0.9 / 0.60) validated in `common/config.py`, so a client may loosen or tighten inside whatever the deployment allows. `THRESHOLD_DEFAULT = 0.60` is measured, not guessed: on the full 47,913-record corpus relevant phrases score 0.74-0.84 and off-topic noise <= 0.559 (see README). It was 0.53 on the old 1,000-record sample; that value leaked food-adjacent noise on the full corpus. Empty results are a valid answer - never recommend lowering the threshold just to fill the list.
- Response `{query, threshold, max_threshold, count, results, status, ringkasan, ringkasan_error, skor_tertinggi, arahan}`. `max_threshold` is a computed mirror of `threshold` for legacy clients. `status` is `full` (AI summary + list), `list_only` (LLM failed or not configured; reason in `ringkasan_error`) or `no_results` (nothing passed the threshold; `skor_tertinggi` is the best similarity found, and `arahan` states the threshold and that best score plus generic suggestions — it does not tell the user to lower the threshold; the empty-DB case gets its own message). Citations in `ringkasan` are `[n]` where n = `peringkat`; combined `[1, 2, 3]` is split into `[1] [2] [3]` and invalid numbers are stripped.
- Each result has **Indonesian keys**: `peringkat`, `skor`, `judul`, `penulis`, `deskripsi`, `url`, `date`. `skor` is a similarity (higher = closer), and `peringkat` is a dense rank computed after filtering. Only the top `SUMMARY_TOP_K` results are sent to the LLM.
- `GET /health` is liveness only: `{status, version, uptime_s}`, no database call, so the Compose healthcheck cannot restart-loop on an empty/broken index.
- `GET /ready` is readiness: 200 with `{status, documents, metric, model, version, uptime_s, llm_configured, last_ingest, problems}` when the collection is open, non-empty, `hnsw:space=cosine`, and the recorded model matches `EMBEDDING_MODEL`; otherwise 503 with a `problems` list. It also fails readiness when `last_ingest.json` says the last run failed.
- Always fetches `SEARCH_N_RESULTS` (default 100) then filters in Python. There is no pagination or `offset`.
- Result construction reads Chroma metadata through `metadata_text()`, which tolerates a missing key or `metadata=None` and returns `""`. `ingestion/ingest.py` still owns the metadata shape; change both together.
