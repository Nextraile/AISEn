# AGENTS.md

## What this repo is

Monorepo root for **AISEn** (AI Search Engine for Undip): semantic search over
research published at Universitas Diponegoro. Two self-contained services:

```
backend/          FastAPI + ChromaDB: embedding, search, Gemini summary, ingestion
                  code in common/ service/ ingestion/, container files in docker/
                  (project `aisen`, container `aisen-api`)
frontend/         React 19 + TypeScript + Vite UI (Tailwind 4)
                  Dockerfile, nginx.conf.template, docker-compose.yml (container `aisen-web`)
README.md         monorepo overview and quick start
AGENTS.md         this file (monorepo-level rules)
```

Backend instructions (architecture, ingestion, API contract, data prerequisites)
are in [`backend/AGENTS.md`](backend/AGENTS.md). Read that before changing
anything under `backend/`. The frontend has no separate instruction file; it is
a plain Vite app, so the rules below apply as-is.

## Where to run commands

Each service assumes its own directory as the working directory.

Backend — `common/config.py` (generated: `./data/vector_db`, `data/documents.json`),
`pyproject.toml` (`pythonpath = ["."], testpaths = ["tests"]`), and
`--env-file .env` are all relative to `backend/`. Compose lives in `backend/docker/`,
so it must be named with `-f` and its paths are relative to that directory:

```bash
cd backend
pip install -r requirements.txt
python -m ingestion.ingest --all                     # only when data/vector_db/ is missing
uvicorn service.app:app --reload --port 8000 --env-file .env
pytest
APP_UID=$(id -u) docker compose -f docker/docker-compose.yml up -d --build --wait
```

Frontend:

```bash
cd frontend
npm ci
npm run dev        # http://localhost:5173
npm run build      # tsc -b && vite build
npm run lint
```

Docker — the same Compose project (`aisen`) for both, so `aisen-web` reaches
`aisen-api` by name on `aisen_default`:

```bash
cd backend
APP_UID=$(id -u) docker compose -f docker/docker-compose.yml --profile tools run --rm tools --all
APP_UID=$(id -u) docker compose -f docker/docker-compose.yml up -d --build --wait      # API on :8000

cd ../frontend
docker compose up -d --build --wait                       # UI on :3000
```

`ingestion/ingest.py` is only in the `tools`-profile image (`INGEST=1`); the serving image
does not contain it.

Backend commands fail from the monorepo root: `service/app.py` will not be
importable and the vector DB will not be found. npm commands fail from the root
because there is no root `package.json`, and fail from the UNC path
`\\wsl.localhost\...` — run them inside WSL, or use Docker.

## Conventions

- Do not commit secrets or generated data. `backend/.env`, `frontend/.env`, and
  `backend/data/` are git-ignored; `backend/.env.example` and
  `frontend/.env.example` document every variable and are the files to copy from.
- `frontend/.env` is **not** committed. It holds no secrets, but it is still
  per-checkout configuration (API base URL, request timeout, default threshold,
  page size, debounce delay, relevance presets), so a fresh clone starts from
  `src/app/config.ts` fallbacks until you run `cp .env.example .env`. Everything
  genuinely secret belongs in `backend/.env`. Vite inlines these values at build
  time, so a change needs a dev-server restart or a rebuild.
- Docker builds ignore `frontend/.env`: `frontend/docker-compose.yml` passes the
  same names as build args, and `.dockerignore` keeps `.env` out of the context
  so a dev value cannot be baked into the image. Changing any `VITE_*` value
  needs a rebuild; `API_UPSTREAM` is the one runtime variable (read by nginx, not
  the bundle).
- Line endings are LF, enforced by `.gitattributes` (`* text=auto eol=lf`).
  Git for Windows sets `core.autocrlf=true` system-wide while WSL git does not,
  so without that file the two clients disagree and WSL reports the whole tree as
  modified. Do not commit those phantom diffs, do not delete the file, and do not
  reintroduce CRLF: a shell script with CRLF fails in a container with
  `bad interpreter`.
- Commit `frontend/package-lock.json`. Never commit `frontend/node_modules/` or
  `frontend/dist/`; both are git-ignored.
- Keep each service self-contained in its own directory. Root-level files are for
  cross-service concerns only (this file, `README.md`).
