# AISEn frontend

Search UI for **AISEn**: type a question in Indonesian or English, adjust the
relevance gate, and read ranked Undip eprints — with a Gemini summary when the
backend is configured for one.

React 19 + TypeScript + Vite 6 + Tailwind 4. One screen, one endpoint
(`POST /search`).

## Quick start (Docker)

```bash
cd frontend
docker compose up -d --build --wait
```

Open <http://localhost:3000>. The backend must be running (`cd ../backend &&
docker compose up -d`), but start order does not matter: the UI loads either way
and `/api/*` returns 502 until the API is up.

`VITE_*` values are compiled into the bundle, so they are **build args**, not
runtime variables — see [Configuration](#configuration). The one runtime knob is
`API_UPSTREAM`, the address nginx proxies to.

## Quick start (dev server)

```bash
cd frontend
npm ci
npm run dev        # http://localhost:5173
```

The dev server calls the backend cross-origin using `VITE_API_BASE_URL` from
`.env` (default `http://localhost:8000`). CORS is open on the backend by
default, so no proxy is needed.

> **UNC paths:** npm scripts fail when the checkout is reached through
> `\\wsl.localhost\...`. Run this from inside WSL, or use the Docker path above —
> the container builds with a Linux toolchain and sidesteps the problem.

## Configuration

Vite replaces `import.meta.env.VITE_*` with string literals during `vite build`,
so changing any `VITE_*` value requires a **rebuild**, not a restart. Compose
passes them as build args, and `frontend/.env` is excluded from the build context
by `.dockerignore` — the committed `.env` is for the dev server only.

| Variable | Dev default | Docker default | Meaning |
|---|---|---|---|
| `VITE_API_BASE_URL` | `http://localhost:8000` | `/api` | Backend base URL. In Docker the UI calls its own origin and nginx proxies. |
| `VITE_API_TIMEOUT_MS` | `15000` | `15000` | Request timeout. Keep above the backend's `LLM_TIMEOUT_SECONDS` (15 s). |
| `VITE_DEFAULT_THRESHOLD` | `0.60` | `0.60` | Minimum similarity sent when none is chosen. Must match `THRESHOLD_DEFAULT`. |
| `VITE_RESULTS_PER_PAGE` | `10` | `10` | Result cards per page. |
| `VITE_SEARCH_DEBOUNCE_MS` | `350` | `350` | Delay after typing before a request fires. |
| `VITE_RELEVANCE_LOW` | `0.4` | `0.4` | Gentle preset. Must stay inside the backend's `THRESHOLD_MIN..MAX`. |
| `VITE_RELEVANCE_MEDIUM` | `0.5` | `0.5` | Default preset. |
| `VITE_RELEVANCE_HIGH` | `0.6` | `0.6` | Strict preset. |

Runtime only, read by nginx at container start:

| Variable | Default | Meaning |
|---|---|---|
| `API_UPSTREAM` | `http://aisen-api:8000` | Where `/api/*` is proxied. Point it at `http://host.docker.internal:8000` to use a backend outside this Compose project, with no rebuild. |

`src/app/config.ts` is the only place `import.meta.env` is read; it applies the
fallbacks above and clamps the presets to the backend's `0.1..0.9` range.

## How the container is built

`Dockerfile` is a two-stage build: `node:22-alpine` runs `npm ci && npm run
build` (`tsc -b && vite build`), then `nginxinc/nginx-unprivileged:1.27-alpine`
serves `dist/` on port 8080 as uid 101 — no root, no privileged port.

`nginx.conf.template` is rendered at start by the image's envsubst step and
provides:

- `/api/*` → `API_UPSTREAM`, stripping the `/api` prefix.
- SPA fallback (`try_files $uri $uri/ /index.html`), so `/search` and future
  routes survive a refresh instead of returning 404.
- Hashed assets cached for a year, `index.html` never cached.
- `GET /healthz` for the compose healthcheck.

The upstream is held in a variable so nginx resolves it per request. A literal
`proxy_pass` would make nginx refuse to boot when the backend is missing.

## Scripts

| Command | Does |
|---|---|
| `npm run dev` | Vite dev server on :5173 with HMR |
| `npm run build` | `tsc -b && vite build` → `dist/` |
| `npm run lint` | ESLint |
| `npm run preview` | Serve the built bundle locally |

## Structure

```
src/app/config.ts               the only environment reader (fallbacks + clamping)
src/app/api/                    fetch wrapper: timeouts, abort, typed errors (see its README)
src/features/search/            SearchBar, SearchResults, cards, useSearchDocuments, searchApi
src/pages/SearchPage.tsx        the screen
src/App.tsx                     routing: "/" and "/search" both render SearchPage
src/app/router.tsx              unused second router; App.tsx owns routing
```

`src/app/router.tsx` defines a `createBrowserRouter` that nothing imports. It is
harmless but do not extend it — add routes to `App.tsx`.

## Notes

- The UI sends `max_threshold`, the deprecated alias the backend still accepts.
  It is a **minimum similarity**: higher is stricter, and an empty result is a
  valid answer, not an error to work around.
- The relevance slider is clamped to the backend's advertised range
  (`THRESHOLD_MIN`/`MAX`, default `0.1..0.9`) so the UI cannot send a request the
  API will reject.
