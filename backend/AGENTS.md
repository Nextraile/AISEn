# AGENTS.md

## What this repo is

One small service: FastAPI semantic search over harvested Universitas Diponegoro (Undip) eprints. Flat layout, no packages, no CI, no lockfile. Tests live in `tests/` (pytest, fake model/collection, no model download). Python 3.11.

- `app.py` — the service (also what the Dockerfile serves: `CMD uvicorn app:app`): async endpoint, embedding/query offloaded via `run_in_threadpool`, explicit `RuntimeError` at startup when the Chroma collection is missing, Gemini summary with citations and fallback to list-only.
- `ingest.py` — the only data ingestion path: OAI-PMH harvest → `data/documents.json` → embeddings → Chroma. Not run by the app, not in the Docker image (`Dockerfile` copies only `app.py`).

## Data prerequisites (the #1 startup failure)

- `./data/vector_db` is gitignored and required at startup. If absent, the app fails to boot (`RuntimeError` in `app.py`).
- Ingest from `backend/` with `python ingest.py --all`: harvests OAI-PMH (`https://eprints2.undip.ac.id/cgi/oai2`, `metadataPrefix=oai_dc`, target 1000 valid records) → writes `data/documents.json` → embeds `"{title}. {description}"` and inserts into Chroma. `--harvest` / `--index` run one stage.
- `ingest.py` **deletes and recreates** collection `undip_research_search` with `{"hnsw:space": "cosine"}`. Re-running wipes existing vectors; there is no incremental update path.
- Inserts are batched at 1000 because Chroma rejects larger batches (~5461 cap).
- `sickle` is in `requirements.txt` and imported lazily inside `harvest()`, so serving does not need it at import time.
- Embedding model (`sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2`) downloads at startup. Docker sets `HF_HOME=/model-cache` backed by the named `model_cache` volume so restarts don't re-download; local runs use the default HF cache.
- Ingestion and the app must use the same model, or stored vectors won't match query vectors. `ingest.py` imports `MODEL_NAME`, `DB_PATH`, `COLLECTION_NAME`, and `THRESHOLD_DEFAULT` from `app.py` to prevent drift.

## Commands

```bash
pip install -r requirements.txt
python ingest.py --all                     # only when data/vector_db/ is missing
uvicorn app:app --reload --port 8000 --env-file .env   # local dev; run from backend/
pip install -r requirements-dev.txt && pytest   # tests (no model download needed)
cp .env.example .env                       # then fill GEMINI_API_KEY and GEMINI_MODEL
docker compose up --build                  # serves app.py on :8000 (needs .env)
APP_UID=$(id -u) docker compose up -d --build --wait   # same, writable bind mount, waits for health
```

- Run uvicorn from `backend/`: `DB_PATH = "./data/vector_db"` is relative to the CWD.
- `pytest` is the only configured tool (`pytest.ini`, tests in `tests/`). No linter, formatter, or typechecker is configured; verify before assuming `ruff` etc. exist.
- LLM: Google Gemini via `google-genai`, configured by env `GEMINI_API_KEY` and `GEMINI_MODEL`. Never commit `.env` (it is gitignored). If either is empty the API still works and returns the list only (`status=list_only`). No automatic retry: failures surface in `ringkasan_error` and the user retries manually.
- `requirements.txt` is range-pinned (`chromadb>=1.5,<2`, `sentence-transformers>=6,<7`); code depends on Chroma 1.x APIs. `docker-compose.yml` bind-mounts `./data/vector_db`, so the host DB is what the container reads; build with `APP_UID=$(id -u)` so the container can write it. LLM budget is `LLM_TIMEOUT_SECONDS = 15` (measured: real calls 1–7 s, tail past 8 s).

## API contract

- `POST /search`. Request `{query: str, min_length=1 (whitespace is stripped), threshold: float = 0.53, THRESHOLD_MIN <= threshold <= THRESHOLD_MAX}`. `max_threshold` is accepted as a deprecated alias for the existing frontend; it uses the same minimum-similarity scale, never a distance.
- `threshold` is a **minimum similarity** (`1 - cosine distance`); higher = stricter (matches the UI slider). Bounds are constants in `app.py` (0.1 / 0.9; the client may loosen or tighten inside that range). `THRESHOLD_DEFAULT = 0.53` is measured, not guessed: relevant phrases score 0.58-0.73 and noise <= 0.46 on the shipped corpus (see README). Empty results are a valid answer — never recommend lowering the threshold just to fill the list.
- Response `{query, threshold, max_threshold, count, results, status, ringkasan, ringkasan_error, skor_tertinggi, arahan}`. `max_threshold` is a computed mirror of `threshold` for legacy clients. `status` is `full` (AI summary + list), `list_only` (LLM failed or not configured; reason in `ringkasan_error`) or `no_results` (nothing passed the threshold; `skor_tertinggi` is the best similarity found, and `arahan` states the threshold and that best score plus generic suggestions — it does not tell the user to lower the threshold; the empty-DB case gets its own message). Citations in `ringkasan` are `[n]` where n = `peringkat`; combined `[1, 2, 3]` is split into `[1] [2] [3]` and invalid numbers are stripped.
- Each result has **Indonesian keys**: `peringkat`, `skor`, `judul`, `penulis`, `deskripsi`, `url`, `date`. `skor` is a similarity (higher = closer), and `peringkat` is a dense rank computed after filtering. Only the top `SUMMARY_TOP_K` results are sent to the LLM.
- `GET /health` returns `{status, documents, llm_configured, metric}`. `metric` should be `cosine`; anything else means stored vectors were built without `hnsw:space=cosine` and `skor` is not a similarity.
- Always fetches `N_RESULTS = 100` then filters in Python. There is no pagination or `offset`.
- Result construction reads Chroma metadata through `metadata_text()`, which tolerates a missing key or `metadata=None` and returns `""`. `ingest.py` still owns the metadata shape; change both together.
