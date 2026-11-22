# Undip Research Semantic Search API

A small FastAPI service that answers free-text questions over ~1,000 harvested
Universitas Diponegoro eprints. It embeds the query, searches a local ChromaDB
collection by cosine similarity, returns the matching papers, and — when a
Gemini key is configured — adds an AI summary whose claims are cited against the
papers that were actually retrieved.

> **Status: prototype.** Built for a project demo, not a production deployment.
> See [Known limitations](#known-limitations) before relying on it.

## How it works

```mermaid
sequenceDiagram
    participant C as Client
    participant A as FastAPI
    participant E as SentenceTransformer
    participant D as ChromaDB
    participant G as Gemini

    C->>A: POST /search {query, threshold}
    A->>E: encode(query)
    E-->>A: 384-dim vector
    A->>D: query(vector, n_results=100)
    D-->>A: metadatas + cosine distances
    A->>A: similarity = 1 - distance, keep >= threshold
    alt nothing passes the threshold
        A-->>C: status=no_results + arahan
    else at least one paper
        A->>G: prompt(query, top 5 papers)
        alt summary ok
            G-->>A: text with [n] citations
            A-->>C: status=full
        else error or 15 s timeout
            A-->>C: status=list_only + ringkasan_error
        end
    end
```

The service never fails a search because the LLM failed: the worst case is
`status=list_only`, which still returns the ranked papers.

## Quick start (local)

**Prerequisites**

- Python 3.11+
- The vector database in `./data/vector_db` — **the app refuses to start
  without it**. See [docs/DATA.md](docs/DATA.md) for how to build it.
- `GEMINI_API_KEY` and `GEMINI_MODEL` in `.env` (optional; without them the API
  still works and returns `status=list_only`).

```bash
cp .env.example .env                      # fill in both Gemini values
pip install -r requirements.txt
python ingest.py --all                    # only if data/vector_db/ is missing
uvicorn app:app --reload --port 8000 --env-file .env
```

`--env-file` is required locally: the app reads plain environment variables and
does not load `.env` by itself (Docker Compose injects the file for you). Without
it, searches still work but every response is `list_only`.

`CHROMA_DB_PATH` is relative, so always start uvicorn from this directory
(`backend/`).

**Verify it works**

```bash
curl http://localhost:8000/health
# {"status":"ok","documents":1000,"llm_configured":true,"metric":"cosine"}
# documents = however many records your harvest produced

curl -X POST http://localhost:8000/search \
  -H "Content-Type: application/json" \
  -d '{"query": "faktor risiko stunting balita", "threshold": 0.53}'
```

Interactive docs are served at `http://localhost:8000/docs`; the machine-readable
schema is at `/openapi.json`.

## Quick start (Docker)

```bash
cp .env.example .env   # docker compose fails without this file
APP_UID=$(id -u) docker compose up -d --build --wait
```

This builds and serves `app.py` on <http://localhost:8000>, and returns only once
the container is healthy. Override the published port with `API_PORT` (Compose
maps `${API_PORT:-8000}:8000`) and the first-start grace period with
`HEALTH_START_PERIOD` (default `120s`).

The Compose project is `aisen`, so the container is `aisen-api`, the network
`aisen_default`, and the model cache volume `aisen_model_cache`. Those names are
deliberately shared with `frontend/docker-compose.yml`, which proxies `/api` to
`http://aisen-api:8000` — so the UI reaches this container by name, without CORS
and without publishing the API port to the browser's machine.

`APP_UID` makes the container run as your own user, so it can write the
bind-mounted `./data/vector_db`. On PowerShell:
`$env:APP_UID=1000; docker compose up -d --build --wait`.

If the UID does not match, `docker-entrypoint.sh` stops the container before
ChromaDB is opened and prints the owner UID, the container UID, and the exact
command to fix it — instead of the raw `attempt to write a readonly database`
traceback. The default is `10001`, so a plain `docker compose up` always starts
the guard path rather than depending on the host UID.

The collection has to exist before the server starts: build it on the host with
`python ingest.py --all`, or use the Docker-only path below.

The embedding model is cached in the `model_cache` volume, prefixed with the
Compose project name (`aisen_model_cache`). Renaming the project starts from an
empty cache and redownloads the ~470 MB model once. Changing `APP_UID` leaves
that volume owned by the previous user; either hand it to the new one, which
keeps the download —

```bash
docker compose down
docker run --rm -v aisen_model_cache:/model-cache busybox chown -R "$(id -u):$(id -g)" /model-cache
```

— or delete it with `docker volume rm aisen_model_cache` (equivalently
`docker compose down -v`) and pay the download again.

### Ingesting without installing Python on the host

The serving image does not contain `ingest.py`. A second Compose service,
`tools`, builds the same Dockerfile with `INGEST=1` — so the heavy torch layer is
reused rather than downloaded twice — and runs the script with the repository
mounted:

```bash
docker compose --profile tools run --rm tools ingest.py --all
docker compose --profile tools run --rm tools ingest.py --query "analisis kualitas air sungai"
```

`--profile tools` is needed because the service is hidden by default, so a plain
`docker compose up` still starts only the API. This is the Docker-only path: no
`pip install`, and `--query` uses the same `rank_results()` as `POST /search`,
so its scores match the API exactly.

For a demo, running uvicorn directly is simpler and has fewer moving parts.

## Configuration

Everything is optional: with an empty `.env` the service runs on the defaults
below, which are the values that were measured and shipped. Set only what you
need. `.env.example` is the full inventory with the reasoning for each value.

| Variable | Default | Meaning |
|---|---|---|
| `GEMINI_API_KEY` | — | Google Gemini API key. Without it summaries are disabled |
| `GEMINI_MODEL` | — | Model name, e.g. `gemini-2.5-flash`. A wrong name does not break startup — `/search` returns `list_only` pointing at this variable |
| `EMBEDDING_MODEL` | `sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2` | Must match the model the collection was built with. A mismatch is logged at startup |
| `CHROMA_DB_PATH` | `./data/vector_db` | ChromaDB directory, relative to `backend/` |
| `CHROMA_COLLECTION` | `undip_research_search` | Collection name |
| `SEARCH_N_RESULTS` | `100` | Candidates fetched per query; also the maximum result count |
| `THRESHOLD_MIN` / `THRESHOLD_MAX` | `0.1` / `0.9` | Accepted range for the request's `threshold` |
| `THRESHOLD_DEFAULT` | `0.53` | Applied when a request omits the threshold |
| `SUMMARY_TOP_K` | `5` | Results handed to the LLM; must be ≤ `SEARCH_N_RESULTS` |
| `LLM_TIMEOUT_SECONDS` | `15` | Gemini budget before falling back to `list_only` |
| `CORS_ALLOW_ORIGINS` | `*` | Comma-separated allowed origins |
| `LOG_LEVEL` | `INFO` | `DEBUG`, `INFO`, `WARNING`, `ERROR`, `CRITICAL` |
| `OAI_ENDPOINT` | `https://eprints2.undip.ac.id/cgi/oai2` | Harvest source |
| `INGEST_TARGET` | `1000` | Valid records to harvest |
| `INGEST_JSON_PATH` | `data/documents.json` | Harvest output |
| `INSERT_BATCH_SIZE` | `1000` | Records per ChromaDB insert (cap ≈ 5,461) |
| `EMBED_BATCH_SIZE` | `64` | Documents per embedding batch |
| `MIN_DESCRIPTION_CHARS` | `50` | Harvest filter: skip shorter abstracts |

Ingestion flags (`--target`, `--endpoint`, `--json`, `--db`) still override the
corresponding variables for a single run.

Invalid combinations stop the process at import time with a message naming the
offending variable — for example `THRESHOLD_DEFAULT` outside `MIN..MAX`,
`SUMMARY_TOP_K > SEARCH_N_RESULTS`, or an unknown `LOG_LEVEL`. Nothing starts
half-configured.

Variables are read once at import. Locally `.env` is loaded by
`uvicorn --env-file .env`; Docker Compose loads it via `env_file:`. Editing the
file requires a restart either way. `.env` is git-ignored; never commit it.

## API at a glance

| Endpoint | Purpose |
|---|---|
| `POST /search` | Semantic search over the indexed papers, with optional AI summary |
| `GET /health` | Liveness plus `documents`, `llm_configured`, `metric` |

Full contract — every field, every status, every error, and the
distance-vs-similarity rule that trips everyone up — is in
[docs/API.md](docs/API.md).

The frontend still sends the older field name `max_threshold`. It is accepted as
an alias for `threshold` (same **minimum similarity** scale) and mirrored in the
response, so both names always agree.

## Why the default is 0.53

`threshold` is a **minimum similarity**, not a distance: higher means stricter.
It is a relevance gate, not a knob for "make sure something comes back". An
empty result is a valid answer.

The default (`0.53`, `THRESHOLD_DEFAULT` in `app.py`) was measured on the 1,000-record
collection, not guessed. The method: score a set of phrasal queries that should
match and a set of off-topic queries that should not, then take the midpoint
**only if the two bands separate**. On the shipped corpus they do.

Measured on the 1,000-record collection with the real model:

| Query shape | Best score |
|---|---|
| full phrase (`pengaruh media sosial terhadap perilaku remaja`) | 0.73 |
| full phrase (`analisis kualitas air sungai`) | 0.66 |
| shorter phrase (`energi terbarukan`) | 0.58 |
| one word (`stunting`) | 0.47 |
| off-topic (`resep rendang padang`) | 0.46 |

Noise tops out at 0.46 and relevant phrases start at 0.58, so `0.53` sits in the
gap. Single-word queries fall below it on purpose: they are too vague to be
called relevant. Ask for what you want in a phrase instead of lowering the bar —
and accept an empty list when the corpus genuinely has nothing.

If you change the corpus, the model, or the ingestion code, re-measure before
trusting the constant: run `python ingest.py --query "<phrase>"` for a few
relevant and off-topic phrases and compare the reported similarities. If the
bands overlap, fix retrieval rather than inventing a threshold.

## Project structure

```
app.py                     the entire service: lifespan, schemas, /search, /health, Gemini
settings.py                every configurable value, read once from the environment
ingest.py                  the only ingestion path: OAI-PMH -> JSON -> embeddings -> Chroma
data/                      generated, git-ignored (vector_db/ and documents.json)
tests/test_app.py          API tests; fake model and fake collection, no downloads
tests/test_ingest.py       ingestion tests; fake records, model, and Chroma client
tests/test_settings.py     config parsing and validation
docs/API.md                API reference
docs/DATA.md               ingestion pipeline and metadata contract
docs/DEMO.md               pre-meeting checklist and demo runbook
.env.example               full configuration inventory
Dockerfile                 CPU-only image, non-root user, INGEST arg for the tools image
docker-entrypoint.sh       UID/writability guard, checked before the server starts
docker-compose.yml         aisen-api, the `tools` ingest profile, and the model cache volume
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `RuntimeError: ChromaDB collection 'undip_research_search' was not found` | Database missing, or uvicorn started from the wrong directory | Build the DB ([docs/DATA.md](docs/DATA.md)) and run from `backend/` |
| Startup hangs at "Waiting for application startup" with no further output | The repo sits on a network path (`\\wsl.localhost\...`, a share); SQLite blocks on it | Run uvicorn inside WSL, or copy the repo and the DB to a local disk |
| Startup takes minutes | First-time embedding-model download (~470 MB) | Wait once; the HuggingFace cache (or `model_cache` volume) is reused |
| `/health` shows `"documents": 0` | Empty collection | Run `python ingest.py --all` |
| `docker compose up` logs `the ChromaDB directory is not writable by this container` | Bind-mounted DB owned by a different UID than `APP_UID` | Rebuild with the printed fix: `APP_UID=$(id -u) docker compose up -d --build`, or `chmod -R a+rwX data/vector_db` as a stopgap |
| `/health` shows `"metric": null` or something other than `cosine` | Collection built without `hnsw:space=cosine` | Rebuild the collection with `python ingest.py --index`; scores are meaningless otherwise |
| `status=list_only`, message "belum dikonfigurasi" | Gemini variables empty — often uvicorn started without `--env-file .env` | Set them, restart with `--env-file .env` |
| `status=list_only`, message about quota | Gemini 429 / rate limit | Wait, or raise the quota |
| `status=list_only`, message "Model LLM '...' tidak ditemukan" | Wrong `GEMINI_MODEL` | Use a currently available model name |
| `status=list_only`, message "tidak merespons tepat waktu" | Gemini was slower than 15 s | Retry; if it happens often, check quota and network |
| `status=no_results` on a one-word query | Short queries score below the default (0.47 vs 0.58–0.73 for phrases) | Ask in a phrase with more context; do not lower the threshold just to fill the list |
| `status=no_results` every time | Threshold too strict, or the corpus genuinely has no match | Score a phrase with `python ingest.py --query "<phrase>"`; if nothing is close, the retrieval is the problem, not the threshold |
| `status=no_results`, `arahan` says "Belum ada dokumen" | Collection is empty | Run `python ingest.py --all` |
| `docker compose logs -f` shows an old traceback after a successful restart | `-f` replays the whole history, including the previous failed attempt | Use `docker compose logs --tail 40 aisen-api`, and confirm with `docker inspect aisen-api --format '{{.State.Health.Status}}'` |
| `422 Unprocessable Content` | Empty/whitespace `query`, or `threshold` outside 0.1–0.9 | Fix the request body |
| Browser CORS error | Should not happen (`allow_origins=["*"]`) | Confirm the response carries `access-control-allow-origin: *` |

## Known limitations

- **Sample, not corpus.** `ingest.py` stops at the first 1,000 valid records in
  OAI order (5,035 records scanned on the live feed), so the indexed set is an
  early-page sample rather than a random one.
- **Truncated abstracts.** The embedding model truncates at 128 tokens, so
  stored vectors effectively represent the title plus the opening of the
  abstract.
- **Top 100 only.** The service always fetches 100 candidates and filters them in
  Python; there is no pagination or `offset`.
- **Threshold measured on one sample.** `0.53` separates relevant from off-topic
  on this 1,000-record sample; re-measure on a different corpus. See
  [Why the default is 0.53](#why-the-default-is-053).
- **No authentication or rate limiting.** Anyone who can reach the port can spend
  Gemini quota.
- **No incremental ingestion.** Re-running `ingest.py --index` deletes and
  recreates the collection.
- **One process, one worker.** The model is loaded once per process.
- **No CI, linter, or lockfile.** Dependencies are range-pinned only.

## Data source and credits

Paper metadata comes from the Universitas Diponegoro eprints2 repository via
OAI-PMH (`https://eprints2.undip.ac.id/cgi/oai2`, `metadataPrefix=oai_dc`). Embeddings
use
[`sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2`](https://huggingface.co/sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2).
Summaries are generated by Google Gemini.
