# Data Pipeline

How the vector database gets built, and the contract the service depends on.
Everything here happens in `ingestion/ingest.py` — there is no other ingestion path.

> The app reads `./data/vector_db` and refuses to start without it. That
> directory and `data/documents.json` are git-ignored, so a fresh clone has
> no data until you run `python -m ingestion.ingest --all`.
>
> `data/` itself *is* tracked (an empty directory via `data/.gitkeep`). That
> matters for Docker: a missing bind-mount source gets created as root, which
> the container (running as `APP_UID`) cannot write. Keeping the directory in
> git means a clone already owns it.

## Pipeline overview

```
OAI-PMH (eprints2.undip.ac.id/cgi/oai2)
        │  harvest + filter
        ▼
data/documents.json          (~1,000 records)
        │  embed "{title}. {description}"
        ▼
data/vector_db/                 ChromaDB, collection "undip_research_search"
```

## Stage 1 — Harvest

| Setting | Value |
|---|---|
| Endpoint | `https://eprints2.undip.ac.id/cgi/oai2` |
| Metadata format | `oai_dc` |
| Client | `sickle` |
| Target | 1,000 valid records |
| Observed | 1,000 kept out of 5,035 scanned |

A record is kept when **all** of these hold:

- it has metadata (deleted/broken records are skipped);
- it has a `description` whose trimmed length exceeds 50 characters;
- the title does not contain `penilaian sejawat` and the URL does not contain
  `peer_review` (those are administrative review sheets, not papers).

Extraction rules:

| Field | Rule |
|---|---|
| `title` | first `dc:title` |
| `creators` | all `dc:creator` values (a list) |
| `description` | first `dc:description` — treated as the abstract |
| `url` | first `dc:identifier` containing `http` |
| `date` | first `dc:date`, else `"Tanggal tidak diketahui"` |

Result is written to `data/documents.json`.

Because harvesting stops at 1,000, the indexed corpus is an **early-page sample**
of the OAI feed, not a random or complete sample of Undip's repository.

## Stage 2 — Embed and index

| Setting | Value |
|---|---|
| Model | `sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2` |
| Vector size | 384 |
| Text embedded | `"{title}. {description}"` |
| Encoding | `batch_size=64` |
| Collection | `undip_research_search` |
| Distance | `{"hnsw:space": "cosine"}` |
| Insert batch | 1,000 (Chroma rejects batches above ~5,461) |
| Document IDs | `doc_0`, `doc_1`, … |

**`ingestion/ingest.py` deletes the collection first**, then recreates it. Re-running it
wipes every vector; there is no incremental update path. Stop the API server
before re-ingesting so it is not querying a collection that is being replaced.

The model truncates at 128 tokens, so a stored vector effectively represents the
title plus the opening of the abstract — not the whole document.

## Metadata contract

`ingestion/ingest.py` owns the metadata shape; `service/app.py` reads it. Change both together.

| Chroma metadata key | Source | Returned as | Notes |
|---|---|---|---|
| `title` | `dc:title` | `judul` | Missing → `""` |
| `creators` | `dc:creator`, joined with `", "` | `penulis` | Always a string, never a list |
| `url` | first `http` identifier | `url` | May be `""` |
| `description` | `dc:description` | `deskripsi` | Also part of the embedded text |
| `date` | `dc:date` | `date` | Free text, not parsed as a date |

The service tolerates missing keys (they become `""`) but not a changed distance
metric: if the collection was not created with `hnsw:space=cosine`, `skor` is not
a similarity. `/ready` reports the metric so this is checkable.

## Rebuilding from scratch

1. Stop the API server.
2. From `backend/`, run the whole pipeline:

   ```bash
   python -m ingestion.ingest --all
   ```

   That harvests `eprints2` into `data/documents.json`, then deletes and
   recreates the Chroma collection. Use `--harvest` / `--index` to run one stage,
   `--target N` to change the record count, and `--query "..."` for a
   similarity check against the fresh index.

   Without Python on the host, the same script ships in the `tools` Compose
   profile (the serving image does not contain it):

   ```bash
   docker compose -f docker/docker-compose.yml --profile tools run --rm tools --all
   docker compose -f docker/docker-compose.yml --profile tools run --rm tools --query "analisis kualitas air sungai"
   ```

   Every setting has an environment default (see `../.env.example`), so a
   deployment can run the same command with `INGEST_TARGET`, `OAI_ENDPOINT`,
   `INGEST_JSON_PATH`, `CHROMA_DB_PATH` and `CHROMA_COLLECTION` set instead. CLI
   flags still win over the environment for that run.
3. Start the server, then confirm:

   ```bash
   curl http://localhost:8000/ready
   # status == "ok", documents == number of harvested records, metric == "cosine"
   ```

   A failed run is recorded too: `/ready` puts `last ingest failed: …` in
   `problems`, and `data/last_ingest.json` holds the stage, timings, counts, and
   error.

4. Re-check the threshold on the new data with `python -m ingestion.ingest --query "<phrase>"`
   (see the measured table in the README). If relevant and off-topic scores no
   longer separate, fix retrieval; do not invent a threshold.

`sickle` is part of `requirements.txt`, so `pip install -r requirements.txt`
covers harvesting as well as serving.

## Reproducibility notes

- The harvested JSON and the vector DB are both git-ignored. Rebuilding requires
  network access to the Undip OAI endpoint, which is outside this repository's
  control.
- `ingestion/ingest.py` and `service/app.py` both read the same `common.config.SETTINGS` object, so
  ingestion and serving cannot drift onto a different model, database path, or
  collection name.
- The collection stores the model it was built with in its metadata
  (`embedding_model`). Changing `EMBEDDING_MODEL` without re-running
  `python -m ingestion.ingest --index` logs a warning at startup; the scores would otherwise be
  meaningless without any error.
- The embedding model is referenced by name, not by revision. An upstream model
  update would silently produce vectors that no longer match queries.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `ModuleNotFoundError: sickle` | Dependencies not installed | `pip install -r requirements.txt` |
| Harvest logs an exception and stops | OAI endpoint unreachable or rate-limited | Retry later; check the endpoint in a browser |
| Fewer than 1,000 records saved | The OAI stream ended early | Read the log and re-run; the count is not a filter problem |
| `data/documents.json` missing after an error | `json.dump` only runs after a successful harvest loop | Re-run `python -m ingestion.ingest --harvest` |
| Chroma rejects an insert batch | Batch over the ~5,461 cap | Keep the 1,000-record batching |
| `documents: 0` in `/ready` after ingest | Wrong `CHROMA_DB_PATH` (relative to CWD) | Run `ingestion/ingest.py` and the server from `backend/` |
| Search results look random | Collection not built with `hnsw:space=cosine` | Recreate it with `python -m ingestion.ingest --index` |
| Docker sees no data | `./data/vector_db` missing on the host | Build it on the host; compose bind-mounts `./data` |
| Container exits with `attempt to write a readonly database` | The bind-mounted `./data` is owned by your UID, the image runs as a different UID | Build with `APP_UID=$(id -u) docker compose -f docker/docker-compose.yml up -d --build`; a root-owned `./data` (delete-and-recreate without the `.gitkeep`) needs `sudo chown -R "$(id -u):$(id -g)" data` |
| `APP_UID` changed but the container still fails | The `model_cache` volume is owned by the previous UID | Give it to the new user and keep the cached model: `docker run --rm -v aisen_model_cache:/model-cache busybox chown -R <uid>:<uid> /model-cache`. `docker compose -f docker/docker-compose.yml down -v` also works but redownloads ~470 MB |
