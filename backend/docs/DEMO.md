# Demo Runbook

Everything you need to walk into the meeting with a working demo and a plan for
when something does not cooperate. Written for the prototype as it exists today.

## The day before

- [ ] `cp .env.example .env`, then fill `GEMINI_API_KEY` and `GEMINI_MODEL`.
  Nothing else is required; every other variable has a working default.
  Leave the rest commented or unchanged unless the demo needs it.
  Confirm the model name is currently available — a retired name does not break
  startup, it silently downgrades every answer to `list_only`.
- [ ] Confirm `./data/vector_db` exists on **the laptop you will present
  from**. If you built it on another machine, copy the whole directory.
- [ ] Start the app once so the embedding model downloads:
  ```bash
  uvicorn service.app:app --reload --port 8000 --env-file .env
  ```
  `--env-file` is what actually feeds `GEMINI_API_KEY` / `GEMINI_MODEL` to a local
  run — without it the API quietly degrades to `list_only`.
  The first start takes minutes; later starts take seconds. Do not clear the
  HuggingFace cache. If you demo via Docker, use the same machine and keep the
  `model_cache` volume.
- [ ] If you demo via Docker, build with your own UID so the bind-mounted DB is
  writable:
  ```bash
  APP_UID=$(id -u) docker compose -f docker/docker-compose.yml --env-file .env up -d --build --wait
  ```
  Without `APP_UID` the container runs as UID 10001 and the entrypoint stops
  with the owner UID and the container UID. After changing it, hand the model
  cache to the new user instead of redownloading it:
  ```bash
  docker compose -f docker/docker-compose.yml --env-file .env down
  docker run --rm -v aisen_model_cache:/model-cache busybox chown -R "$(id -u):$(id -g)" /model-cache
  ```
  (`docker compose -f docker/docker-compose.yml --env-file .env down -v` also unblocks it, but pays the ~470 MB download.)
- [ ] If the audience sees the UI rather than Swagger, start it from `frontend/`
  and confirm the nginx proxy reaches the API:
  ```bash
  cd ../frontend && docker compose up -d --build --wait   # http://localhost:3000
  curl -X POST http://localhost:3000/api/search \
    -H "Content-Type: application/json" \
    -d '{"query":"analisis kualitas air sungai"}'
  ```
  The UI needs no CORS setup: the browser calls `/api` on its own origin and
  nginx proxies it to `aisen-api:8000` on the shared `aisen_default` network.
- [ ] Sanity-check the relevance gate (there is no separate calibration script):
  ```bash
  python -m ingestion.ingest --query "analisis kualitas air sungai"
  ```
  Without Python on the host, the `tools` profile runs the same script:
  ```bash
  docker compose -f docker/docker-compose.yml --env-file .env --profile tools run --rm tools --query "analisis kualitas air sungai"
  ```
- [ ] Check every readiness fact in one call:
  ```bash
  curl http://localhost:8000/ready
  ```
  Expect `"status": "ok"`, `"documents"` equal to whatever your harvest produced,
  `"llm_configured": true`, `"metric": "cosine"`, and `"problems": []`.
- [ ] Sanity-check the relevance gate (there is no separate calibration script):
  ```bash
  python -m ingestion.ingest --query "analisis kualitas air sungai"
  ```
  Expect results around 0.66. Then try an off-topic phrase such as
  `resep rendang padang`; it should score about 0.46. If relevant and off-topic
  scores overlap, fix retrieval rather than changing `THRESHOLD_DEFAULT`.
  (If you changed it in `.env`, remember it must stay between `THRESHOLD_MIN`
  and `THRESHOLD_MAX`, otherwise the service refuses to start.)
- [ ] Run one full query end to end. A response with
  `"status": "full"` and a non-empty `ringkasan` is the only proof that the LLM
  path works.
- [ ] Take a screenshot of a successful `full` response.
- [ ] Decide what the audience sees: Swagger UI at `/docs` (zero setup, shows the
  whole contract, good for a technical audience) or the UI at
  `http://localhost:3000` (what the project is actually about).

## On the day

- [ ] Start the server:
  ```bash
  uvicorn service.app:app --port 8000 --env-file .env
  ```
  Prefer this over `docker compose` — fewer moving parts and the model cache is
  already warm. If you are showing the UI, start it too, in another terminal:
  ```bash
  cd frontend && docker compose up -d --build --wait
  ```
  then open `http://localhost:3000`.
- [ ] Warm up with one request before anyone is watching (first request loads
  nothing new, but it shakes out any environment surprise):
  ```bash
  curl -X POST http://localhost:8000/search \
    -H "Content-Type: application/json" \
    -d '{"query":"analisis kualitas air sungai"}'
  ```
- [ ] Keep a terminal with the expected curls ready; do not type JSON live.

## Suggested queries

| Purpose | Query | Expected |
|---|---|---|
| Main demo (Indonesian) | `analisis kualitas air sungai` | `full`, ~13 papers, summary with `[1]` citations |
| Cross-language (English) | `coral reef ecosystem` | `full`, ~5 papers — shows the model is multilingual |
| Deliberate `no_results` | `faktor risiko stunting` with `"threshold": 0.85` | `no_results`, `skor_tertinggi` ~0.47, `arahan` states the threshold and best score without telling you to lower it |

All three were verified against the 1,000-record corpus. Avoid long, very specific
English phrases such as `machine learning for sentiment analysis`: this repository
is weighted toward Indonesian-language papers, so such queries land around 0.47
and return `no_results` at the default.

Show the third one on purpose: it demonstrates that the API explains itself
instead of looking broken, and that an empty answer is correct when nothing
clears the relevance bar.

## If something goes wrong

| Situation | What to say / do | Fallback |
|---|---|---|
| Model starts downloading | "It caches the model on first run" | Wait; it is a one-time cost |
| `status=list_only` with a quota message | "The search still works; the AI layer is rate-limited" | Show the ranked list — that is the fallback design, not a failure |
| `status=list_only` with "belum dikonfigurasi" | `.env` not loaded — usually a missing `--env-file .env` | Restart with the flag |
| `status=list_only` with "tidak merespons tepat waktu" | Gemini was slower than 15 s | Say "the model is slow, here is the list", then retry the query |
| Empty results on everything | DB is empty, or every query is genuinely off-topic | Check `/ready` first; do not lower the threshold just to fill the list |
| Server will not start (`collection ... was not found`) | Wrong directory or missing DB | Start uvicorn from `backend/`; if the DB is missing, switch to screenshots |
| Network is down | Gemini is unreachable | Screenshots of a `full` response; the retrieval path works offline |

## Q&A crib sheet

Answers that are honest and short. Do not overclaim.

- **How many papers?** 1,000 indexed, selected from about 5,035 records scanned in
  the OAI feed — an early-page sample, not a random one.
- **How does search work?** The query and each paper (title + abstract) are
  embedded with the same multilingual model; we rank by cosine similarity and
  apply a minimum-similarity threshold.
- **Why do you truncate abstracts?** The model handles 128 tokens, so vectors
  represent the title plus the start of the abstract. A production version would
  chunk or summarize first.
- **Why is the threshold 0.60?** Measured on the full 47,913-record corpus:
  relevant phrases put the best hit at 0.74–0.84, while off-topic queries top
  out at 0.559. `0.60` sits in that gap, so it keeps real matches and rejects
  noise. You can reproduce the scores with
  `python -m ingestion.ingest --query "<phrase>" --min-similarity 0`.
- **Why did "stunting" return nothing?** One word is too vague: it scores 0.53,
  under the bar. Ask for `faktor risiko stunting pada balita` (0.74). An
  empty result is the intended behaviour when the corpus has nothing clear —
  lower the slider only if you accept noisier matches. The `arahan` field
  reports both the threshold and the best score found.
- **Why did the default change from 0.53?** It was measured on a 1,000-record
  sample. On the full corpus that number let food-adjacent noise through — a
  *mochi* thesis scored 0.538 for `resep rendang padang` — while a legitimate
  one-word query sat just below it. Both bands rise with corpus size; the
  constant had to follow.
- **What if Gemini fails?** The response degrades to `list_only` with a reason in
  `ringkasan_error`; searching never depends on the LLM. There is no automatic
  retry — the user retries.
- **Can the summary invent sources?** Every claim is supposed to carry `[n]`
  matching the retrieved paper's rank, and citations pointing outside the
  retrieved set are stripped before the response is returned.
- **Why no login / rate limit?** Prototype scope; it is a local demo, and the
  service has no user accounts or stored data.
- **What would you do next?** Index the full corpus, add pagination, pin the
  model revision, and add auth before exposing it.
