# API Reference

Complete contract for the Undip Research Semantic Search API. Every field,
status, and error below matches `service/app.py` (app version `0.3.0`).

- Base URL (local): `http://localhost:8000`
- Content type: `application/json` for requests and responses
- Interactive docs: `/docs` · schema: `/openapi.json`

## Contents

- [Scoring: similarity, not distance](#scoring-similarity-not-distance)
- [`POST /search`](#post-search)
- [`GET /health`](#get-health)
- [`GET /ready`](#get-ready)
- [Errors](#errors)
- [CORS](#cors)
- [Limits and timing](#limits-and-timing)
- [Contract changelog](#contract-changelog)

## Scoring: similarity, not distance

ChromaDB returns a **cosine distance**. The API exposes a **similarity**:

```
skor = round(1 - cosine_distance, 6)
```

Higher `skor` means closer. `threshold` is a **minimum** similarity — the opposite
of the distance semantics used in the first version of this API (and in the
retired ingestion notebook).

| distance | skor | passes `threshold: 0.5`? |
|---|---|---|
| 0.10 | 0.90 | yes — very close match |
| 0.50 | 0.50 | yes — exactly on the boundary |
| 0.90 | 0.10 | no — filtered out |

The comparison uses the rounded value, so a distance of `0.5` passes at
`threshold: 0.5` rather than failing on floating-point noise.

**Field names are Indonesian**, matching the product language. They are quoted
verbatim throughout this document:

| Key | Meaning |
|---|---|
| `peringkat` | rank (dense, 1-based, after threshold filtering) |
| `skor` | similarity score |
| `judul` | title |
| `penulis` | authors |
| `deskripsi` | abstract / description |
| `ringkasan` | AI summary |
| `ringkasan_error` | reason the summary is missing |
| `arahan` | guidance for the user |
| `skor_tertinggi` | highest similarity found (even if below threshold) |

---

## `POST /search`

### Request body

| Field | Type | Required | Constraints | Notes |
|---|---|---|---|---|
| `query` | string | yes | at least 1 character **after** trimming | Leading/trailing whitespace is stripped, so `"   "` is rejected. No maximum length. Indonesian and English both work. |
| `threshold` | number | no | `0.1 ≤ threshold ≤ 0.9` by default | Default `0.60`. Minimum similarity; higher is stricter. The accepted range and default are deployment settings: `THRESHOLD_MIN` / `THRESHOLD_MAX` / `THRESHOLD_DEFAULT` (see [Configuration](../README.md#configuration)). The default is measured on the shipped corpus; see [Why the default is 0.60](../README.md#why-the-default-is-060). |
| `max_threshold` | number | no | same as `threshold` | **Deprecated alias** kept for the existing frontend. Same minimum-similarity scale, not a distance. If both are sent, `threshold` wins. |

There is no `offset`, `page`, or `limit` parameter.

```bash
curl -X POST http://localhost:8000/search \
  -H "Content-Type: application/json" \
  -d '{"query": "faktor risiko stunting balita", "threshold": 0.60}'
```

### Response body

Always `200 OK` for a valid request — LLM failures do not produce error statuses.

| Field | Type | Meaning |
|---|---|---|
| `query` | string | Echo of the trimmed request query |
| `threshold` | number | Echo of the applied threshold |
| `max_threshold` | number | Deprecated alias of `threshold`; always equal. Remove once the frontend migrates. |
| `count` | integer | `len(results)`, always |
| `results` | array | Matching papers, best first; may be empty |
| `status` | enum | `full`, `list_only`, or `no_results` |
| `ringkasan` | string \| null | AI summary with `[n]` citations; `null` unless `status` is `full` |
| `ringkasan_error` | string \| null | User-facing reason the summary is missing; `null` unless `status` is `list_only` |
| `skor_tertinggi` | number \| null | Best similarity found when nothing passed the threshold; `null` otherwise (and `null` when the collection is empty) |
| `arahan` | array of string | For `no_results`: the threshold applied, the best similarity found, and generic suggestions. It never tells the user to lower the threshold. |

Every key is always present. `null` and `[]` are meaningful — see the status
matrix.

### `results[]` item

| Field | Type | Meaning |
|---|---|---|
| `peringkat` | integer | Dense rank after filtering: `1..n`, no gaps. **Citation numbers in `ringkasan` refer to this value.** |
| `skor` | number | Similarity, rounded to 6 decimals. Descending across the array. |
| `judul` | string | Paper title |
| `penulis` | string | Authors, comma-separated as stored during ingestion |
| `deskripsi` | string | Abstract |
| `url` | string | Paper URL; may be `""` if metadata lacked one |
| `date` | string | Publication date as stored (free text, e.g. `2023-06-01`) |

Missing metadata never raises an error: absent fields are returned as `""`.

### Status matrix

| `status` | `results` | `ringkasan` | `ringkasan_error` | `skor_tertinggi` | `arahan` |
|---|---|---|---|---|---|
| `full` | ≥ 1 | summary text | `null` | `null` | `[]` |
| `list_only` | ≥ 1 | `null` | error reason | `null` | `[]` |
| `no_results` (nothing passed) | `[]` | `null` | `null` | best score | threshold + best score + generic hints |
| `no_results` (empty DB) | `[]` | `null` | `null` | `null` | "nothing indexed" hints |

`no_results` is a legitimate outcome, not a failure: the corpus has nothing above
the relevance bar the client asked for. Do not treat it as an error and do not
lower the threshold just to fill the list.

`list_only` is a graceful degradation, not an error: the search succeeded, the
summary did not.

### Example: `full`

```json
{
  "query": "faktor risiko stunting balita",
  "threshold": 0.60,
  "max_threshold": 0.60,
  "count": 2,
  "results": [
    {
      "peringkat": 1,
      "skor": 0.82,
      "judul": "Analisis Faktor Risiko Stunting pada Balita di Kabupaten Semarang",
      "penulis": "Rina Kusuma, Bambang Setiawan",
      "deskripsi": "Stunting masih menjadi masalah gizi utama di Indonesia. Penelitian ini menganalisis faktor risiko kejadian stunting pada balita usia 24-59 bulan.",
      "url": "https://ejournal.undip.ac.id/index.php/gizi/article/view/12345",
      "date": "2023-06-01"
    },
    {
      "peringkat": 2,
      "skor": 0.69,
      "judul": "Hubungan Tingkat Pengetahuan Ibu dengan Status Gizi Balita",
      "penulis": "Dewi Lestari",
      "deskripsi": "Pengetahuan ibu berperan penting dalam pemenuhan gizi balita. Studi cross-sectional ini melibatkan 120 responden di Puskesmas Tembalang.",
      "url": "https://ejournal.undip.ac.id/index.php/gizi/article/view/12346",
      "date": "2022-11-15"
    }
  ],
  "status": "full",
  "ringkasan": "Stunting dipengaruhi oleh pengetahuan ibu dan asupan gizi [1]. Studi lain menyebut peran posyandu [2].",
  "ringkasan_error": null,
  "skor_tertinggi": null,
  "arahan": []
}
```

`[1]` and `[2]` refer to `peringkat` 1 and 2. Only the top `SUMMARY_TOP_K = 5`
results are sent to the LLM, so valid citation numbers are always `1..min(count, 5)`.
Any `[n]` outside that range is stripped from the summary before it is returned.

### Example: `list_only` (LLM failed)

```json
{
  "query": "stunting",
  "threshold": 0.60,
  "max_threshold": 0.60,
  "count": 2,
  "results": [
    {
      "peringkat": 1,
      "skor": 0.82,
      "judul": "Analisis Faktor Risiko Stunting pada Balita di Kabupaten Semarang",
      "penulis": "Rina Kusuma, Bambang Setiawan",
      "deskripsi": "Stunting masih menjadi masalah gizi utama di Indonesia. Penelitian ini menganalisis faktor risiko kejadian stunting pada balita usia 24-59 bulan.",
      "url": "https://ejournal.undip.ac.id/index.php/gizi/article/view/12345",
      "date": "2023-06-01"
    },
    {
      "peringkat": 2,
      "skor": 0.69,
      "judul": "Hubungan Tingkat Pengetahuan Ibu dengan Status Gizi Balita",
      "penulis": "Dewi Lestari",
      "deskripsi": "Pengetahuan ibu berperan penting dalam pemenuhan gizi balita. Studi cross-sectional ini melibatkan 120 responden di Puskesmas Tembalang.",
      "url": "https://ejournal.undip.ac.id/index.php/gizi/article/view/12346",
      "date": "2022-11-15"
    }
  ],
  "status": "list_only",
  "ringkasan": null,
  "ringkasan_error": "Kuota atau batas permintaan LLM tercapai. Coba lagi sebentar lagi.",
  "skor_tertinggi": null,
  "arahan": []
}
```

Possible `ringkasan_error` values (user-facing, Indonesian):

| Message | Cause |
|---|---|
| `Ringkasan AI belum dikonfigurasi.` | `GEMINI_API_KEY` or `GEMINI_MODEL` missing |
| `Kuota atau batas permintaan LLM tercapai. Coba lagi sebentar lagi.` | HTTP 429 |
| `Model LLM '<name>' tidak ditemukan. Periksa nilai GEMINI_MODEL.` | HTTP 404 — usually a retired/wrong model name |
| `Layanan LLM mengembalikan error (<code>). Coba lagi.` | Any other Gemini API error |
| `LLM tidak menghasilkan ringkasan. Coba lagi.` | Empty completion |
| `LLM tidak merespons tepat waktu. Coba lagi.` | Exceeded `LLM_TIMEOUT_SECONDS` (15 s) |
| `Ringkasan gagal dibuat. Coba lagi.` | Unexpected exception; details stay in the server log |

There is no automatic retry. The client decides whether to retry.

### Example: `no_results` (nothing above the threshold)

```json
{
  "query": "stunting",
  "threshold": 0.60,
  "max_threshold": 0.60,
  "count": 0,
  "results": [],
  "status": "no_results",
  "ringkasan": null,
  "ringkasan_error": null,
  "skor_tertinggi": 0.4716,
  "arahan": [
    "Tidak ada jurnal yang mencapai ambang kemiripan 0.60.",
    "Kemiripan tertinggi yang ditemukan: 0.47.",
    "Coba kata kunci yang lebih spesifik.",
    "Perluas topik pencarian."
  ]
}
```

`skor_tertinggi` lets a UI explain the miss without a second request. The
guidance is diagnostic: it reports why the list is empty and how far the best
candidate was. It deliberately does not tell the user to lower the threshold,
because the threshold is the relevance guarantee — an empty answer is correct
when the corpus has nothing relevant.

### Example: `no_results` (empty collection)

```json
{
  "query": "stunting",
  "threshold": 0.60,
  "max_threshold": 0.60,
  "count": 0,
  "results": [],
  "status": "no_results",
  "ringkasan": null,
  "ringkasan_error": null,
  "skor_tertinggi": null,
  "arahan": [
    "Belum ada dokumen terindeks di basis data.",
    "Jalankan python -m ingestion.ingest --all untuk mengisi basis data."
  ]
}
```

### Error responses

`422 Unprocessable Content` — validation failure, returned before any embedding,
search, or LLM call.

Whitespace-only query:

```json
{
  "detail": [
    {
      "type": "string_too_short",
      "loc": ["body", "query"],
      "msg": "String should have at least 1 character",
      "input": "   ",
      "ctx": {"min_length": 1}
    }
  ]
}
```

Threshold out of bounds:

```json
{
  "detail": [
    {
      "type": "less_than_equal",
      "loc": ["body", "threshold"],
      "msg": "Input should be less than or equal to 0.9",
      "input": 0.95,
      "ctx": {"le": 0.9}
    }
  ]
}
```

A missing `query` field produces `"type": "missing"` with the same envelope.

`500 Internal Server Error` — only for infrastructure faults: the ChromaDB
collection disappearing or the store becoming unreadable after startup. Missing
paper metadata and every LLM failure are handled and do **not** produce a 500.

### Behaviour notes

- **Candidate pool.** The service always asks Chroma for `N_RESULTS = 100`
  nearest neighbours, then filters in Python by `skor >= threshold`. There is no
  pagination and no way to request more than the top 100.
- **Ordering.** Chroma returns ascending distance; the service preserves that
  order, so `results` are descending by `skor`. Ties keep Chroma's order.
- **LLM input.** Query text plus title and abstract of the top 5 results. The
  prompt instructs the model to answer only from those sources and to cite each
  claim as `[n]`.
- **Logging.** Exactly one line per request:
  `search request_id=<id> status=<status> count=<n> threshold=<t> duration_ms=<ms>`,
  plus a `request request_id=<id> method=… path=… status=… duration_ms=…` line
  from the middleware. `X-Request-ID` is accepted from the caller or generated
  and always echoed in the response.
- **Concurrency.** Endpoint is `async`; encoding and the Chroma query run in a
  threadpool, so a slow query does not block the event loop.

---

## `GET /health`

Liveness only; no database work, so it is safe to poll every 30 s (the Compose
healthcheck does). Always `200` while the process serves HTTP.

| Field | Type | Meaning |
|---|---|---|
| `status` | string | Always `"ok"` |
| `version` | string | Application version |
| `uptime_s` | number \| null | Seconds since startup |

```json
{ "status": "ok", "version": "0.3.0", "uptime_s": 142.7 }
```

---

## `GET /ready`

Readiness: can this process actually answer searches? Returns `200` when every
check passes and `503` with a `problems` list otherwise. The checks run against
the live collection, so this is the endpoint to load before a demo.

| Field | Type | Meaning |
|---|---|---|
| `status` | string | `"ok"` or `"not_ready"` |
| `documents` | integer | Number of vectors in the collection |
| `metric` | string \| null | `hnsw:space`; must be `"cosine"` |
| `model` | string | Current `EMBEDDING_MODEL` |
| `version` | string | Application version |
| `uptime_s` | number \| null | Seconds since startup |
| `llm_configured` | boolean | Whether both Gemini env vars are set (does **not** validate the key or model name) |
| `last_ingest` | object \| null | The most recent entry of `data/history.json` (append-only JSONL, one line per successful run), or `null` if no run is recorded |
| `problems` | array | Human-readable reasons the service is not ready; empty when `ok` |

```json
{
  "status": "ok",
  "documents": 47915,
  "metric": "cosine",
  "model": "sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2",
  "version": "0.3.0",
  "uptime_s": 142.7,
  "llm_configured": true,
  "last_ingest": {
    "status": "ok",
    "stage": "harvest",
    "records_harvested": 47966,
    "records_skipped": 3,
    "documents_indexed": null,
    "resume_from": { "records": 200, "scanned": 3811 }
  },
  "problems": []
}
```

The `503` cases, all reported in `problems`:

- `documents: 0` → ingestion never ran; every search will be `no_results`.
- `metric` not `"cosine"` → stored vectors were built with a different distance
  function; `skor` is not a similarity and the 0.1–0.9 threshold is meaningless.
- collection model differs from `EMBEDDING_MODEL` → re-run `python -m ingestion.ingest --index`.

A failed ingest does **not** fail readiness: `data/history.json` only ever gains
an entry for a successful run (a run that aborted produced no data to point at),
so `last_ingest` always describes a corpus that exists. Failures are visible in
the log's final `INGEST FAILED | stage=…, error="…"` line.

`llm_configured: false` does not fail readiness: searches work, summaries never
appear. Use `/search` to prove the LLM actually responds.

---

## Errors

An unexpected server error returns `500` with a JSON body that carries the
request id, so a user can quote one string that matches the logs:

```json
{ "detail": "Terjadi kesalahan internal. Coba lagi.", "request_id": "9f2c…" }
```

The same value is in the `X-Request-ID` response header.

---

## CORS

Prototype configuration: all origins (`CORS_ALLOW_ORIGINS=*`), all methods, all
headers, credentials disabled.

```
access-control-allow-origin: *
```

This is deliberate — the API sets no cookies and uses no credentials, and the
frontend may run on any port. Before publishing the service, set
`CORS_ALLOW_ORIGINS` to the real frontend origin(s), comma-separated, e.g.
`CORS_ALLOW_ORIGINS=https://aisen.example.com`.

---

## Limits and timing

| Limit | Value | Source |
|---|---|---|
| Candidates fetched per query | 100 | `SEARCH_N_RESULTS` |
| Results returned | up to 100 | no pagination |
| Results sent to the LLM | 5 | `SUMMARY_TOP_K` |
| LLM wait before fallback | 15 s | `LLM_TIMEOUT_SECONDS` |
| `query` minimum length | 1 character after trim | `SearchRequest` |
| `threshold` range | 0.1 - 0.9, default 0.60 | `THRESHOLD_MIN/MAX/DEFAULT` |
| Cold start (first boot) | minutes | embedding-model download |
| Warm start | seconds | model + DB load |
| Typical search latency | dominated by the LLM; retrieval is tens of ms | — |

---

## Contract changelog

Useful for frontend clients written against an earlier build.

| Change | Before | After |
|---|---|---|
| Threshold semantics | `max_threshold` — maximum cosine **distance**, `0.0–2.0`, lower = stricter | `threshold` — minimum **similarity**, `0.1–0.9`, higher = stricter |
| Legacy field name | — | `max_threshold` is accepted again as a deprecated **similarity** alias, and mirrored in every response so older clients keep working |
| Score semantics | `skor` = distance, lower = better | `skor` = similarity, higher = better |
| Response shape | `{query, max_threshold, count, results}` | adds `status`, `ringkasan`, `ringkasan_error`, `skor_tertinggi`, `arahan` |
| AI summary | none | optional, `status` reflects whether it was produced |
| Summary prompt | terse; no length or structure limit | capped at 3 paragraphs / 180 words, forbids invented facts and repeated titles |
| Citations | `[1, 2, 3]` left as-is | split into `[1] [2] [3]`; invalid numbers removed |
| `/health` | `{status, documents}` | `{status, version, uptime_s}`; the data facts moved to `/ready` |
| Readiness | — | `/ready` returns `503` with `problems` when the index is empty, non-cosine, or built with a mismatched model |
| Ingestion failures | recorded as a failed last run, which failed readiness | `INGEST FAILED` log line only; `history.json` gains an entry only for successful runs |
| Metadata handling | direct indexing, `KeyError` possible | missing fields become `""` |
| LLM timeout | 20 s → 8 s | 15 s (measured: real calls run 1–7 s with a tail past 8 s) |
| Default threshold | `0.5` (a guess) | `0.53`, then `0.60` once measured on the full 47,913-record corpus (noise ≤ 0.559, relevant phrases ≥ 0.74) |
| `no_results` guidance | "Kemiripan tertinggi …; turunkan threshold di bawah nilai itu." | reports the threshold and best score, no instruction to lower it — an empty result is valid |
| Ingestion | notebook run by hand | `python -m ingestion.ingest --all` (same harvest, JSON, and Chroma build), resumable per stage |

See [DEMO.md](DEMO.md) for the operational checklist and [DATA.md](DATA.md) for
how the collection must be built for these scores to mean anything.
