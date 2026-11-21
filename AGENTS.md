# AGENTS.md

## What this repo is

Monorepo root for **AISEn** (AI Search Engine for Undip): semantic search over
research published at Universitas Diponegoro. Two self-contained services:

```
backend/          FastAPI + ChromaDB: embedding, search, Gemini summary, ingestion
frontend/         React 19 + TypeScript + Vite UI (Tailwind 4)
README.md         monorepo overview and quick start
AGENTS.md         this file (monorepo-level rules)
```

Backend instructions (architecture, ingestion, API contract, data prerequisites)
are in [`backend/AGENTS.md`](backend/AGENTS.md). Read that before changing
anything under `backend/`. The frontend has no separate instruction file; it is
a plain Vite app, so the rules below apply as-is.

## Where to run commands

Each service assumes its own directory as the working directory.

Backend — `DB_PATH = "./data/vector_db"`, `pytest.ini` (`pythonpath = .`),
and `--env-file .env` are all relative to `backend/`:

```bash
cd backend
pip install -r requirements.txt
python ingest.py --all                     # only when data/vector_db/ is missing
uvicorn app:app --reload --port 8000 --env-file .env
pytest
APP_UID=$(id -u) docker compose up -d --build --wait
```

Frontend:

```bash
cd frontend
npm ci
npm run dev        # http://localhost:5173
npm run build      # tsc -b && vite build
npm run lint
```

Backend commands fail from the monorepo root: `app.py` will not be
importable and the vector DB will not be found. npm commands fail from the root
because there is no root `package.json`.

## Conventions

- Do not commit secrets or generated data. `backend/.env`,
  `backend/data/vector_db/` and `backend/data/documents.json` are
  git-ignored; `backend/.env.example` documents the required variables.
- `frontend/.env` is committed on purpose: it holds a single non-secret value,
  `VITE_API_BASE_URL`. Everything secret belongs in the backend.
- Commit `frontend/package-lock.json`. Never commit `frontend/node_modules/` or
  `frontend/dist/`; both are git-ignored.
- Keep each service self-contained in its own directory. Root-level files are for
  cross-service concerns only (this file, `README.md`).
