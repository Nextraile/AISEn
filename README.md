# AISEn — AI Search Engine for Undip

Semantic search over research published at Universitas Diponegoro (Undip).
Ask a question in plain Indonesian or English; the backend embeds it, ranks
~1,000 harvested eprints by cosine similarity, and optionally summarizes the
matches with citations. The frontend is the search UI: query box, relevance
slider, result cards, and the AI summary.

> **Status: prototype.** Built for a project demo, not a production deployment.
> No authentication, no rate limiting, no pagination. See
> [Known limitations](backend/README.md#known-limitations).

## Layout

```
backend/      FastAPI service: embedding, ChromaDB search, Gemini summary, ingestion
frontend/     React 19 + TypeScript + Vite UI (Tailwind 4)
AGENTS.md     monorepo-level rules for contributors and agents
```

Each service is self-contained. Run every command from inside its own
directory — paths such as `DB_PATH = "./data/vector_db"` and
`--env-file .env` are relative to `backend/`.

## Requirements

| Tool | Version | Needed for |
|---|---|---|
| Python | 3.11+ | backend |
| Node.js | 20+ | frontend |
| Docker | any recent | optional, backend only |
| Gemini API key | — | optional; without it the API returns results without a summary |

## Quick start

Two terminals, backend first.

**1. Backend — `http://localhost:8000`**

```bash
cd backend
pip install -r requirements.txt
cp .env.example .env                     # fill GEMINI_API_KEY and GEMINI_MODEL
python ingest.py --all                   # only when data/vector_db/ is missing
uvicorn app:app --reload --port 8000 --env-file .env
```

```bash
curl http://localhost:8000/health
# {"status":"ok","documents":1000,"llm_configured":true,"metric":"cosine"}
```

**2. Frontend — `http://localhost:5173`**

```bash
cd frontend
npm ci
npm run dev
```

`frontend/.env` sets `VITE_API_BASE_URL=http://localhost:8000`. Point it at
another host if the API runs elsewhere. CORS is open on the backend, so the Vite
dev server works as-is.

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
| [backend/README.md](backend/README.md) | architecture, configuration, why the default threshold is 0.53, known limitations |
| [backend/docs/API.md](backend/docs/API.md) | request/response contract, all three statuses, errors |
| [backend/docs/DATA.md](backend/docs/DATA.md) | OAI-PMH harvest, embedding, metadata contract, rebuild steps |
| [backend/docs/DEMO.md](backend/docs/DEMO.md) | pre-demo checklist and talking points |
| [backend/AGENTS.md](backend/AGENTS.md) | backend internals and invariants |
| [frontend/src/app/api/README.md](frontend/src/app/api/README.md) | the frontend's HTTP client wrapper |

## Data is not in the repository

`backend/data/vector_db/` and `backend/data/documents.json` are
git-ignored. A fresh clone has no index: run `python ingest.py --all` in
`backend/` to harvest the Undip OAI-PMH feed and build the collection.
