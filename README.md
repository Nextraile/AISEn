# AISEn — AI Search Engine for Undip

Semantic search over research published at Universitas Diponegoro (Undip).
Ask a question in plain Indonesian or English; the backend embeds it, ranks
47,993 harvested eprints by cosine similarity, and optionally summarizes the
matches with citations. The frontend is the search UI: query box, relevance
slider, result cards, and the AI summary.

> **Status: prototype.** Built for a project demo, not a production deployment.
> No authentication, no rate limiting, no pagination. See
> [Known limitations](backend/README.md#known-limitations).

## Layout

```
backend/      FastAPI service: embedding, ChromaDB search, Gemini summary, ingestion
frontend/     React 19 + TypeScript + Vite UI (Tailwind 4)
```

Each service is self-contained. Run every command from inside its own
directory — paths such as `CHROMA_DB_PATH` (default `./data/vector_db`) and
`--env-file .env` are relative to `backend/`. Compose for the backend lives in
`backend/docker/`, so those commands name the file:
`docker compose -f docker/docker-compose.yml --env-file .env ...`.

## Requirements

| Tool | Version | Needed for |
|---|---|---|
| Docker | recent, with Compose v2 | **both services** — the recommended path |
| Python | 3.11+ | backend, without Docker |
| Node.js | 22+ (20+ also works) | frontend, without Docker |
| Gemini API key | — | optional; without it the API returns results without a summary |
| NVIDIA GPU + driver | any CUDA 12-capable card | optional; only makes **ingestion** ~6× faster, everything else runs on the CPU |

## Quick start (Docker)

Each service has its own `docker-compose.yml` and its own working directory, and
both declare the same Compose project (`aisen`), so they share one network.

```powershell
# 1. Build the index — first run only: harvests and embeds up to 1,000
#    records (that 1,000 is the default --target)
cd backend
$env:APP_UID='1000'                       # your uid (`id -u` in WSL); persists in this window
docker compose -f docker/docker-compose.yml --env-file .env --profile tools run --rm tools --all

# 2. API -> http://localhost:8000  (returns once healthy)
docker compose -f docker/docker-compose.yml --env-file .env up -d --build --wait
curl.exe -s http://localhost:8000/ready

# 3. UI -> http://localhost:3000
cd ..\frontend
cp .env.example .env     # needed: the fallback is dev-only
docker compose up -d --build --wait
```

The image ships CPU-only torch by default, so by default the index is built on
the CPU. To use a card, set one value in `backend/.env`:

```bash
CUDA_TAG=cu126    # the only tag that reaches back to older cards
```

then build the tools image once and run step 1 with the GPU overlay — it gives
the container the card and makes a CPU fallback an error rather than a slow run:

```powershell
docker compose -f docker/docker-compose.yml -f docker/docker-compose.gpu.yml --env-file .env --profile tools build tools
docker compose -f docker/docker-compose.yml -f docker/docker-compose.gpu.yml --env-file .env --profile tools run --rm tools --all
```

The `build` matters: `run` reuses whatever image is already there, so changing
`CUDA_TAG` and running straight away keeps the old image — and only the
`embedding device | device=cpu` log line would say so. Check that line; it is
the one honest answer for how a build actually went.

Measured on a GTX 1050 with `torch 2.14.1+cu126`, on real documents:

| device | 2,000 documents | docs/s | full 47,993 |
| --- | --- | --- | --- |
| CPU | 85.6 s | 23.4 | ~40 min |
| GTX 1050 | 13.0 s | 154 | ~5 min |

Two things worth knowing before you reach for it. The CUDA image is ~2.5 GB of
extra wheels, so it only pays off when you ingest more than the 1,000-record
default (`--target`, or `INGEST_TARGET` in `.env`) or re-index repeatedly. And
on this card `torch.cuda.get_arch_list()` reports no `sm_61` even though the
encode works — the `sm_60` kernels cover Pascal — so trust the run, not that
list. Ranking is identical either way: the same query returns the same titles
with the same scores to four decimals. Full detail is in
[`backend/README.md`](backend/README.md#gpu).

`EMBED_DEVICE` in `.env` does **not** steer Docker ingestion — the Compose files
own that (`cpu` for `tools`, `cuda` under the overlay), so one `.env` cannot
pick the wrong device. It applies to host runs and, pinned, to the `api`
service. Two knobs, two jobs: `CUDA_TAG` is the torch wheel at build time,
`EMBED_DEVICE` is the device at run time.

`APP_UID` lets the container write the bind-mounted `./data`. On Linux/WSL
prefix the command instead: `APP_UID=$(id -u) docker compose -f
docker/docker-compose.yml up -d --build --wait`. If it does not match, `docker-entrypoint.sh` stops the container and
prints the owner UID and the container UID, rather than a SQLite traceback.

Log lines are stamped in local time, and the containers default to `TZ=Asia/Jakarta`
(WIB) so those stamps match your wall clock instead of sitting 7 hours behind in
UTC. Set `TZ` in `backend/.env` to change it; an unknown name fails startup
rather than quietly logging in UTC. Recorded timestamps in
`data/history.json` stay UTC, because `/ready` and any comparison read them as
machine values rather than display.

`backend/data/` is committed as an empty directory (via `data/.gitkeep`) for a
reason: Docker creates a missing bind-mount source as **root**, which a
container running as `APP_UID` then cannot write. Keeping the directory in git
means a fresh clone already owns it, so there is nothing to fix before the
first `up`.

The browser never calls the API cross-origin: it requests `/api/...` from the
same origin and nginx proxies that to `http://aisen-api:8000` over the shared
`aisen_default` network. No CORS, and the API port need not be reachable from
the machine showing the UI.

Stop both with `docker compose down` in `frontend/` and `backend/`. Do not add
`-v` in `backend/` unless you want to redownload the ~470 MB embedding model.

## Quick start (without Docker)

Two terminals, backend first.

**1. Backend — `http://localhost:8000`**

```bash
cd backend
pip install -r requirements.txt
cp .env.example .env                     # fill GEMINI_API_KEY and GEMINI_MODEL
python -m ingestion.ingest --all                   # only when data/vector_db/ is missing
uvicorn service.app:app --reload --port 8000 --env-file .env
```

To ingest on a GPU here, install a CUDA torch wheel first — `requirements.txt`
pulls the CPU build, and `EMBED_DEVICE` (default `auto`) then picks the card up
on its own; set it to `cuda` to make a CPU fallback an error instead of a slow
build:

```bash
pip install torch --index-url https://download.pytorch.org/whl/cu126   # match your driver
EMBED_DEVICE=auto python -m ingestion.ingest --all
```

```bash
curl http://localhost:8000/ready
# {"status":"ok","documents":47993,"metric":"cosine","problems":[],...}
```

`GET /health` is liveness only; `GET /ready` is what confirms the index can
actually answer searches. Compose also runs a `watch` service that polls
`/ready` and logs state changes.

**2. Frontend — `http://localhost:5173`**

```bash
cd frontend
cp .env.example .env     # needed: the fallback is dev-only, see below
npm ci
npm run dev
```

`frontend/.env` sets `VITE_API_BASE_URL=http://localhost:8000` plus the UI
tuning values; `.env.example` documents each. Vite inlines them at build time,
so changes need a rebuild. Point the base URL at another host if the API runs
elsewhere. CORS is open on the backend by default, so the Vite dev server works
as-is.

npm scripts fail from a UNC path (`\\wsl.localhost\...`): run them inside WSL,
or use the Docker path above.

## How the two halves meet

`POST /search` is the only endpoint the UI uses. The frontend sends
`max_threshold` (its slider values are `0.4` / `0.5` / `0.6`) and the API treats
it as a **minimum similarity** — higher is stricter — accepting it as a
deprecated alias for `threshold`. Every response carries both field names, so
the UI keeps working while new clients use `threshold`.

An empty result is a valid answer: when nothing clears the threshold the API
returns `status: "no_results"` plus the best score it found, and the UI shows
guidance instead of a list.

## Documentation

| Document | Covers |
|---|---|
| [backend/README.md](backend/README.md) | architecture, configuration, why the default threshold is 0.60, known limitations |
| [backend/docs/API.md](backend/docs/API.md) | request/response contract, all three statuses, errors |
| [backend/docs/DATA.md](backend/docs/DATA.md) | OAI-PMH harvest, embedding, metadata contract, rebuild steps |
| [backend/docs/DEMO.md](backend/docs/DEMO.md) | pre-demo checklist and talking points |
| [backend/AGENTS.md](backend/AGENTS.md) | backend internals and invariants |
| [frontend/README.md](frontend/README.md) | UI structure, configuration, the nginx container |
| [frontend/src/app/api/README.md](frontend/src/app/api/README.md) | the frontend's HTTP client wrapper |
| [backend/.env.example](backend/.env.example) | every backend setting, with defaults |
| [frontend/.env.example](frontend/.env.example) | every frontend setting, with defaults |

## Data is not in the repository

`backend/data/vector_db/`, `backend/data/documents.json`,
`backend/data/history.json`, and `backend/data/temp/` are git-ignored, but
`backend/data/` itself is
committed (an empty directory via `data/.gitkeep`) so Docker never has to
create it as root. A fresh clone has no index. Build it with either path:

```bash
# local
cd backend && python -m ingestion.ingest --all

# Docker (no Python on the host): ingestion ships only in the `tools` profile
cd backend && docker compose -f docker/docker-compose.yml --env-file .env --profile tools run --rm tools --all
```

Both harvest the Undip OAI-PMH feed, embed the records, and create the
collection. Both stages are resumable: rerun the same command after an
interruption and it continues from where it stopped (a harvest tops up to
`--target`), keeping its progress in `data/temp/`. Add `--fresh` to throw that
state away and rebuild from scratch.
