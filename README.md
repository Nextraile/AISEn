# undip-harvester

Pipeline data repository Universitas Diponegoro melalui protokol **OAI-PMH**
(`https://eprints2.undip.ac.id/cgi/oai2`) → **cleansing** → **normalisasi** →
**vektorisasi** (TF-IDF atau ONNX) → **indexing ke Qdrant**.

Semuanya berjalan sebagai **CLI** (bukan REST API) dan **setiap tahap menyimpan
hasil ke file**, sehingga tiap langkah bisa diperiksa, diulang, atau diganti
tanpa menjalankan ulang seluruh alur.

```
harvest              clean                normalize             vectorize              index
──────────           ──────────           ──────────            ──────────             ──────
OAI-PMH  ──────► data/raw/ ──────► data/clean/ ──────► data/normalized/ ──────► data/vectors/
(ListRecords)     records.jsonl      records.jsonl        records.jsonl           vectors.bin
                                             └──────────────────────────────────────┘ + model.json / engine.json
                                                                                       │
                                                                                       ▼
                                                                                 Qdrant (6333)
```

---

## Pilih satu cara menjalankan

Terdapat **dua cara** — pilih **salah satu** sesuai lingkungan Anda. Isi tiap
tahap, format file, dan seluruh flag **sama persis**; yang berbeda hanya cara
memanggil binary-nya.

| | **Cara A — Lokal** | **Cara B — Docker** |
|---|---|---|
| Cara memanggil | `./undip-harvester <perintah> [flags]` | `docker compose run --rm harvester <perintah> [flags]` |
| Perlu install Go? | **Ya** (untuk build) | **Tidak** |
| Perlu Docker? | Hanya untuk menjalankan Qdrant | Ya (Qdrant + image CLI) |
| Cocok untuk | Iterasi cepat, mengubah kode | Lingkungan reproducible, CI, mesin tanpa Go |
| File hasil | `./data/` | `./data/` (di-mount dari container — **sama**) |

> **Aturan main:** seluruh contoh perintah di README ini ditulis dalam
> **Cara A (lokal)**. Untuk **Cara B**, ganti prefix-nya saja:
>
> ```
> ./undip-harvester X   →   docker compose run --rm harvester X
> ```

---

## Prasyarat

**Cara A — Lokal:**
- Go ≥ 1.21 (dikembangkan & diuji dengan Go 1.26)
- Qdrant berjalan di `127.0.0.1:6333` (pilih satu):
  ```bash
  docker compose up -d qdrant                      # via Docker (hanya Qdrant-nya)
  # atau tanpa compose:
  docker run -d --name qdrant -p 6333:6333 qdrant/qdrant
  # atau binary resmi Qdrant, atau Qdrant Cloud (set QDRANT_URL + QDRANT_API_KEY)
  ```

- **Engine ONNX** (jika dipakai, default `auto`): shared library ONNX Runtime
  + model multilingual MiniLM-L12:
  ```bash
  # libonnxruntime.so -> https://github.com/microsoft/onnxruntime/releases
  # (atau via package manager: apt install libonnxruntime), lalu:
  export ONNXRUNTIME_SHARED_LIBRARY_PATH=/path/ke/libonnxruntime.so
  # model: data/models/multilingual-minilm-l12/{tokenizer.json, model_optimized.onnx}
  #       (TIDAK ikut di repo — 235 MB. Salin dari mesin lain atau unduh model
  #        ONNX fastembed "Qdrant/paraphrase-multilingual-MiniLM-L12-v2-onnx-Q";
  #        untuk Docker di-mount lewat ./data)
  ```

**Cara B — Docker:**
- Docker Engine + Docker Compose v2 (tidak butuh Go)

> Catatan: tahap `harvest`, `clean`, `normalize`, dan `vectorize` **tidak
> butuh Qdrant** maupun API key — semuanya murni stdlib dan bekerja offline
> terhadap Qdrant. Hanya `index`/`search` yang membutuhkannya.

---

## Quickstart

### Cara A — Lokal

```bash
# 1) nyalakan Qdrant (sekali saja; via Docker, hanya Qdrant-nya)
docker compose up -d qdrant

# 2) build binary sekali
go build -o undip-harvester .

# 3) jalankan pipeline penuh
./undip-harvester harvest           # resumable — boleh dihentikan & dilanjut
./undip-harvester pipeline -skip-harvest
./undip-harvester search -q "supply chain management logistik"
```

### Cara B — Docker

```bash
# 1) nyalakan Qdrant (service "harvester" sengaja memakai profile [tools],
#    sehingga `up -d` HANYA menyalakan Qdrant)
docker compose up -d qdrant

# 2) build image CLI sekali
docker compose build harvester

# 3) jalankan pipeline penuh (data di-mount ke ./data — sama dengan Cara A)
docker compose run --rm harvester harvest
docker compose run --rm harvester pipeline -skip-harvest
docker compose run --rm harvester search -q "supply chain management logistik"
```

Setelah itu semua perintah pada bagian berikut bisa dipakai apa adanya
(tinggal ganti prefix sesuai aturan di atas).

---

## Perintah

Semua contoh di bagian ini memakai **bentuk Cara A**.
Untuk Cara B: `./undip-harvester X` → `docker compose run --rm harvester X`.

### `harvest` — ambil data (dapat dihentikan & dilanjutkan)

```bash
# Uji cepat: 2.000 record pertama
./undip-harvester harvest -limit 2000

# Kebutuhan penuh: tanpa -limit (resumable, checkpoint disimpan tiap halaman)
./undip-harvester harvest
```

* Pola pengambilan: `ListRecords` + `resumptionToken` (halaman berisi 100 record).
* **Resumable**: `data/harvest_state.json` menyimpan token terakhir. Kalau proses
  mati/`Ctrl+C`, jalankan lagi perintah yang sama — lanjut dari token terakhir.
* `-fresh` mengulang dari nol (menimpa file).
* Jeda sopan antar request: `-delay` (default `500ms`).
* Daftar set (`ListSets`, 193 entri) ikut disimpan ke `data/sets.jsonl`
  untuk memberi nama human-readable pada `setSpec` hex EPrints.

### Tahap data (per langkah)

```bash
./undip-harvester clean       # data/raw/records.jsonl    -> data/clean/records.jsonl
./undip-harvester normalize   # data/clean/records.jsonl  -> data/normalized/records.jsonl
./undip-harvester vectorize   # data/normalized/records   -> data/vectors/vectors.bin (+ engine.json)
./undip-harvester index       # vectors.bin + records     -> Qdrant collection undip_eprints
```

**Engine embedding** — dua backend tersedia:

```bash
./undip-harvester vectorize -engine tfidf   # TF-IDF hashing (dim 1024) — default lama
./undip-harvester vectorize -engine onnx   # ONNX multilingual MiniLM-L12 (dim 384) — semantic
./undip-harvester vectorize                  # auto -> onnx bila model tersedia di data/models/
```

- `tfidf`: TF-IDF + feature hashing (lexical), model di `data/vectors/model.json`.
- `onnx`: encoder ONNX Runtime (model `Qdrant/paraphrase-multilingual-MiniLM-L12-v2-onnx-Q`,
  int8) + tokenizer HuggingFace (pure Go) → vektor **384-dim** mean pooling + L2 normalize,
  sama persis dengan perilaku model sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2
  di proyek legacy. Metadata engine (engine/model_dir/dim) ditulis ke
  `data/vectors/engine.json` sehingga `search` otomatis memakai engine yang sama dengan
  yang dipakai saat indexing.

### `pipeline` — semua tahap sekaligus

```bash
./undip-harvester pipeline                    # full alur (harvest sambung dari checkpoint)
./undip-harvester pipeline -limit 500         # alur penuh tapi dibatasi 500 record
./undip-harvester pipeline -skip-harvest      # pakai data mentah yang sudah ada
./undip-harvester pipeline -skip-index        # berhenti di file vektor
./undip-harvester pipeline -engine onnx -skip-harvest -recreate
./undip-harvester pipeline -timing-json data/timing.json   # simpan timing per tahap ke JSON (untuk perbandingan)
```

> **Dari awal hingga akhir?** Cukup jalankan `./undip-harvester pipeline` (tanpa flag) —
> dia akan harvest (sambung dari checkpoint) → clean → normalize → vectorize → index.
> Pakai `-skip-harvest` bila raw sudah ada, dan `-recreate` bila ganti engine
> (mis. tfidf → onnx) agar collection Qdrant dibuat ulang dengan dimensi baru.

Catatan: bila engine berubah (mis. tfidf -> onnx, dim 1024 -> 384), selalu pakai
`-recreate` saat `index`/`pipeline` supaya collection Qdrant dibuat ulang dengan
dimensi yang sesuai (tool juga auto-detect & hapus bila dimensi beda).

**Pengukuran waktu:** setiap tahap mencetak durasinya (`--- clean selesai dalam 5.692s`),
dan akhir pipeline mencetak tabel timing per tahap + `TOTAL`. Gunakan
`-timing-json <file>` untuk menyimpan timing dalam format JSON (mudah dibandingkan
dengan implementasi Python).

### `search` — uji pencarian

```bash
./undip-harvester search -q "supply chain management rantai pasokan"
./undip-harvester search -q "green supply chain" -n 5 -min-score 0.2
./undip-harvester search -q "kapal nelayan" -set "Status = Published"
./undip-harvester search -q "cars" -engine onnx -n 5 -summarize -llm-model gemini-2.5-flash
```

- `-engine`: memaksa engine (`tfidf` | `onnx` | `auto`) — default `auto` (dari
  `engine.json` bila ada, else `tfidf`).
- `-model-dir`: direktori model ONNX (default `data/models/multilingual-minilm-l12`).
- `-summarize`: tambahkan ringkasan AI (Gemini) dari hasil teratas.
- Env: `GEMINI_API_KEY` + `GEMINI_MODEL` (default `gemini-2.5-flash`) untuk ringkasan AI.

### `serve` — REST API (Huma v2 + Scalar docs)

```bash
./undip-harvester serve                     # listen 0.0.0.0:8080
./undip-harvester serve -port 3000          # override port (atau env PORT)
./undip-harvester serve -api-key "secret"   # aktifkan auth (header X-API-Key / Bearer)
```

Endpoint (semua di bawah `/api/v1`, kecuali `/health`, `/docs`, `/openapi.json`):

| Method | Path | Keterangan |
|---|---|---|
| `GET` | `/health` | status + versi |
| `POST` | `/api/v1/search` | pencarian vektor (body: `query`, `n`, `min_score`, `set`, `engine`, `summarize`) |
| `POST` | `/api/v1/jobs` | buat job async (`type`: harvest/clean/normalize/vectorize/index/pipeline) |
| `GET` | `/api/v1/jobs` | daftar job |
| `GET` | `/api/v1/jobs/{id}` | status + hasil job |
| `POST` | `/api/v1/admin/{action}` | convenience wrapper di atas `/api/v1/jobs` |

- **Dokumentasi interaktif (Scalar)**: `http://localhost:8080/docs`
- **OpenAPI 3.1 (auto-generated oleh Huma)**: `http://localhost:8080/openapi.json`
- Job asinkron: `POST /api/v1/jobs` mengembalikan `job_id` (202) → polling
  `GET /api/v1/jobs/{id}` untuk status/progress/hasil.
- Auth opsional: set `API_KEY` (env/.env) atau flag `-api-key`; jika diset,
  request wajib membawa `X-API-Key` atau `Authorization: Bearer <key>`.
  `/health`, `/docs`, `/openapi.json` tetap publik.

### `peek` — inspeksi file

```bash
./undip-harvester peek -file data/raw/records.jsonl -n 2
./undip-harvester peek -file data/vectors/vectors.bin -n 5
./undip-harvester peek -file data/normalized/records.jsonl -count
```

### Bantuan

```bash
./undip-harvester help                 # daftar perintah
./undip-harvester <perintah> -h        # flag per perintah
```

---

## Struktur proyek

```
main.go          # entry point: parse perintah, exit code, usage
cli/             # package cli: implementasi tiap perintah + konstanta default
                   cli_harvest, cli_clean, cli_normalize, cli_vectorize,
                   cli_index, cli_search, cli_pipeline, cli_peek, config
internal/oai/    # klien OAI-PMH: ListRecords + resumptionToken, retry/backoff, checkpoint
internal/clean/  # cleansing: entity/HTML/control-char, dedup, flag kualitas, URL vs kutipan
internal/normalize/ # skema kanonik: tahun, penulis {family,given}, bahasa, label set hex
internal/embed/  # TF-IDF (2 pass streaming) + signed feature hashing -> vektor dense 1024, L2 norm
internal/semantic/ # ONNX Runtime (MiniLM-L12) + tokenizer HuggingFace -> vektor dense 384, L2 norm
internal/store/  # JSONL streaming + format biner vektor (sparse idx/f32)
internal/api/    # REST API Huma v2 + chi: routes, handlers, job store, Scalar docs
internal/qdrant/ # klien REST murni stdlib: create collection, upsert, search
Dockerfile       # multi-stage build (golang:1.26-bookworm, CGO -> debian, ONNX lib + user non-root)
docker-compose.yml # service qdrant + harvester (profile tools)
```

Arah import selalu satu arah sehingga **tidak ada siklus import**:

```
main  ->  cli  ->  internal/{oai,clean,normalize,embed,semantic,store,api,qdrant}
```

---

## Struktur file (`data/`)

Berlaku sama untuk Cara A maupun Cara B (Cara B mem-mount `./data` host ke
`/app/data` container).

| File | Isi | Format |
|------|-----|--------|
| `raw/records.jsonl` | Record mentah OAI-PMH (header + Dublin Core apa adanya) | JSONL |
| `harvest_state.json` | Checkpoint `resumptionToken`, jumlah record, status selesai | JSON |
| `sets.jsonl` | Pemetaan `setSpec` → `setName` dari `ListSets` | JSONL |
| `clean/records.jsonl` | Hasil cleansing + flag kualitas + pemisahan URL/kutipan | JSONL |
| `normalized/records.jsonl` | Skema dokumen final kanonik (siap vektor & payload) | JSONL |
| `vectors/vectors.bin` | Vektor per dokumen (sparse: idx+value) | biner, little-endian |
| `vectors/model.json` | Model TF-IDF (IDF per term, parameter, jumlah dokumen) | JSON |
| `vectors/engine.json` | Metadata engine embedding aktif (`onnx` \| `tfidf`, model_dir, dim) | JSON |

> Format `vectors.bin`:
> `magic "UDV1" | version u32 | dim u32 | count u32 | reserved u32` diikuti tiap record:
> `idLen u16 | id | nnz u32 | nnz × {idx u32, value f32}`.
> Penyimpanan sparse karena rata-rata hanya ~50 dari 1024 dimensi terisi —
> file 1.500 dokumen ≈ 650 KB.
> Pakai `-format jsonl` bila ingin vektor dalam bentuk JSON yang mudah dibaca.

---

## Skema dokumen (`data/normalized/records.jsonl`)

```jsonc
{
  "id": "oai:eprints2.undip.ac.id:10",
  "eprint_id": 10,
  "landing_url": "https://eprints2.undip.ac.id/id/eprint/10/",
  "file_url": "https://eprints2.undip.ac.id/id/eprint/10/1/Scan0001.pdf",
  "title": "Analisis Pengaruh Kinerja Logistik Pemasok ...",
  "authors": [{ "raw": "KUSUMAWARDHANI, Amie", "family": "KUSUMAWARDHANI", "given": "Amie" }],
  "subjects": ["Undip Formal Documents"],
  "abstract": "...",
  "publisher": "Departemen Manajemen FEB UNDIP",
  "citation": "KUSUMAWARDHANI, Amie (2015) Analisis ... ISSN 2337-3792",
  "year": 2015,
  "date": "2015",
  "types": ["article", "peerreviewed"],
  "peer_reviewed": true,
  "language": "English",
  "language_code": "en",
  "set_specs": ["7374617475733D707562", "..."],
  "set_names": ["Status = Published", "Subject = Undip Formal Documents", "Type = Article"],
  "quality_flags": ["missing_description"],
  "embedding_text": "Judul. Subjek. Abstrak. penulis: ... Penerbit. Status = Published, ...",
  "source": "eprints2.undip.ac.id",
  "harvested_at": "2026-11-20T02:11:41Z"
}
```

---

## Rincian tiap tahap

### Cleansing (`internal/clean`)
- Unescape entity (`&#13;`, `&amp;`), buang tag HTML, karakter kontrol,
  zero-width/BOM, dan spasi berlebih → satu spasi.
- Deduplikasi nilai per field (case-insensitive) dan dedup record per `identifier`.
- Pisahkan `dc:identifier` menjadi **URL** (landing page/file) vs **kutipan**.
- Penilaian kualitas (`quality_flags`): `missing_title`, `missing_creator`,
  `missing_description`, `missing_date`, `short_title`, `boilerplate_title`
  (mis. `Turnitin_…`, `Hasil Scan Plagiasi …`), `deleted`, `empty_citation`.
- Record `status=deleted` atau tanpa judul **dibuang** (`usable=false`).
- **Filter metadata (`Options`)** — port aturan harvest proyek legacy
  `semantic-search-undip`: `-min-desc` (abstrak pertama > N karakter,
  default 50) dan `-skip-admin` (judul "penilaian sejawat" / URL
  "peer_review", default aktif). Alasan dibuang tercatat di statistik
  `filtered_no_abstract` & `filtered_administrative`.

### Normalisasi (`internal/normalize`)
- Tahun/tanggal dari `dc:date` (`2019`, `2017-03`, RFC3339, dst.).
- Penulis `"Family, Given"` → struktur `{raw, family, given}` + dedup.
- Kode bahasa → nama bahasa (`en` → `English`).
- `setSpec` hex EPrints → label: utamakan `ListSets`, fallback decode hex
  (`7374617475733D707562` → `status=pub`).
- `PeerReviewed` vs `NonPeerReviewed` → bool `peer_reviewed`.
- Ekstraksi `eprint_id`, `landing_url`, `file_url`.
- Menyusun `embedding_text` (judul + subjek + abstrak + penulis + penerbit + set).

### Vektorisasi (`internal/embed`)
- Tokenisasi: lowercase, buang stopword Indonesia/Inggris/noise akademik,
  token < 3 huruf, angka murni; n-gram 1–2 (`rantai_pasokan`).
- **TF-IDF** dua pass (streaming, hemat memori):
  `idf = ln((1+N)/(1+df)) + 1`, `tf` sublinear `1+ln(tf)`.
- **Feature hashing** (signed, dua seed FNV-1a berbeda) → vektor padat
  **dimensi 1024**, lalu **normalisasi L2** → siap cosine di Qdrant.
- Model (`model.json`) disimpan agar query di `search` memakai bobot yang sama;
  `vectorize` otomatis memakai model lama bila ada (pakai `-rebuild-model`
  untuk fit ulang setelah korpus berubah).

### Vektorisasi / embedding (`internal/semantic`)
- Backend **ONNX Runtime** (`github.com/yalue/onnxruntime_go`) + tokenizer
  HuggingFace (`github.com/sugarme/tokenizer`, pure Go):
  - session ONNX memakai input `input_ids`, `attention_mask`, `token_type_ids`
    → output `last_hidden_state` (384-dim);
  - mean pooling di atas token non-pad + **L2 normalize** → vektor 384-dim;
  - batch inference (ukuran `EncodeBatchSize=8`) diproses **goroutine per batch**
    (ONNX Runtime session thread-safe), hasil dikumpulkan berurutan.
- Runtime perlu shared library ONNX (`ONNXRUNTIME_SHARED_LIBRARY_PATH`, default
  `lib/libonnxruntime.so`); di Docker di-set otomatis ke `/usr/local/lib/...`.
- Engine dipilih via `-engine tfidf|onnx|auto` (default `auto`); metadata
  (`engine.json`) dipakai `search`/`pipeline` agar konsisten dengan indexing.

### Indexing (`internal/qdrant`)
- Klien REST murni stdlib (tanpa SDK): `PUT /collections`, `PUT .../points`,
  `POST .../points/search`.
- Collection dibuat otomatis (`Cosine`, dim = dimensi engine aktif — 1024
  TF-IDF / 384 ONNX); bila sudah ada dengan dimensi berbeda, tool **hapus &
  buat ulang** otomatis.
- ID point = `eprint_id` (bila ada) agar upsert idempotent; selebihnya FNV-1a
  dari identifier OAI (dibatasi 53 bit).
- Payload: `title, authors, year, abstract(max 4000), subjects, types,
  publisher, language, sets, url, file_url, citation, quality_flags, ...`
- Upsert per batch (`-batch`, default 64) dengan `wait=true`.

---

## Konfigurasi

| Flag / env | Default | Keterangan |
|---|---|---|
| `-url` (harvest) | `https://eprints2.undip.ac.id/cgi/oai2` | endpoint OAI-PMH |
| `-prefix` | `oai_dc` | metadataPrefix |
| `-set`, `-from`, `-until` | kosong | filter subset data |
| `-limit` | `0` (tanpa batas) | batas record (tetap bisa dilanjut) |
| `-delay` | `500ms` | jeda sopan antar request |
| `-min-df` / `-max-features` / `-ngram` | `2` / `50000` / `2` | parameter TF-IDF |
| `-engine` (vectorize/pipeline/search) | `auto` | engine vektorisasi: `tfidf` \| `onnx` \| `auto` (default auto → onnx bila model ada) |
| `-model-dir` (vectorize/pipeline/search) | `data/models/multilingual-minilm-l12` | direktori model ONNX |
| `-min-desc` (clean/pipeline) | `50` | panjang minimal abstrak dalam karakter; `0` = tanpa syarat |
| `-skip-admin` (clean/pipeline) | `true` | buang dokumen administratif (penilaian sejawat / peer_review) |
| `-workers` (index/pipeline) | `4` | jumlah goroutine upsert paralel ke Qdrant |
| `-summarize` (search) | `false` | tampilkan ringkasan AI (Gemini) |
| `-llm-model` (search) | `gemini-2.5-flash` | model Gemini untuk ringkasan (env: `GEMINI_MODEL`) |
| `-timing-json` (pipeline) | kosong | simpan ringkasan timing per tahap ke file JSON (perbandingan) |
| `-url` (index/search) | `http://127.0.0.1:6333` | URL Qdrant |
| `-collection` | `undip_eprints` | nama collection |
| `QDRANT_URL` | `http://127.0.0.1:6333` | override URL Qdrant via environment (otomatis di-set `http://qdrant:6333` di Docker) |
| `QDRANT_API_KEY` | kosong | untuk Qdrant Cloud/berauth (env) |
| `GEMINI_API_KEY` | kosong | API key Google Gemini untuk ringkasan AI (`-summarize`) |
| `GEMINI_MODEL` | `gemini-2.5-flash` | model Gemini (env), default untuk ringkasan AI |

---

## File `.env`

Semua secret & override (Qdrant, Gemini, path lib ONNX) dibaca dari **environment**.
Agar tidak perlu `export` manual, binary otomatis memuat file `.env` di
direktori kerja (stdlib-only, tanpa dependensi eksternal) — lihat
`cli/config.go` (`LoadDotEnv`). Variabel env nyata tetap lebih prioritas
daripada nilai di `.env`.

```bash
cp .env.example .env
# isi .env: GEMINI_API_KEY=... , GEMINI_MODEL=..., dst.
./undip-harvester search -q "cars" -summarize
```

Di Docker, Compose otomatis membaca `.env` dari root proyek sehingga `docker
compose run --rm harvester ...` langsung memakai isi `.env` tanpa mount khusus.

---

## Detail Docker (Cara B)

Dockerfile multi-stage: build `golang:1.26-bookworm` (CGO diaktifkan untuk
wrapper ONNX Runtime + `gcc`/`libc6-dev`) → runtime `debian:bookworm-slim`,
copy binary + `libonnxruntime.so` ke `/usr/local/lib`, user non-root,
`WORKDIR /app`, volume `/app/data`.

```bash
# Build alternatif tanpa compose:
docker build -t undip-harvester .

# Jalankan tanpa compose (harus set network/URL sendiri):
docker run --rm -v "$PWD/data:/app/data" undip-harvester harvest -limit 2000
docker run --rm -v "$PWD/data:/app/data" --network host \
  -e QDRANT_URL=http://127.0.0.1:6333 undip-harvester index
```

Poin penting:
* `WORKDIR /app` sehingga semua flag relatif (`data/...`) menunjuk ke `/app/data`
  (di-mount ke `./data` di host) — file tetap bisa dibuka/diperiksa dari luar container,
  dan **hasil Cara A & Cara B saling terbaca**.
* Service `harvester` memakai compose `profiles: [tools]`, jadi
  `docker compose up -d` **hanya** menyalakan Qdrant; CLI dipanggil dengan
  `docker compose run --rm harvester <perintah>`.
* Di dalam compose, `QDRANT_URL` sudah di-set ke `http://qdrant:6333`.
* ONNX: image mem-copy `libonnxruntime.so` dan set `ONNXRUNTIME_SHARED_LIBRARY_PATH`;
  model ONNX berada di `./data/models/...` (di-mount lewat `./data:/app/data`).
* Versi binary bisa dioverride saat build:
  `docker build --build-arg VERSION=0.2.0 -t undip-harvester .`
  (atau `docker compose build --build-arg VERSION=0.2.0 harvester`)

---

## Konkurensi (goroutines)

Sebagian tahap dijalankan paralel; sisanya sengaja sekuensial:

| Tahap | Paralel? | Keterangan |
|---|---|---|
| `harvest` | ❌ sekuensial | `resumptionToken` halaman N **bergantung** halaman N-1 + jeda sopan `-delay` — paralelisasi mustahil & melanggar etika crawling |
| `clean`, `normalize` | ❌ sekuensial | I/O streaming ringan (~ratusan ms untuk puluhan ribu record) — paralel hanya menambah konteks memori |
| `vectorize` (fit TF-IDF) | ✅ **goroutine per-batch** | tokenisasi pass 1 & 2 dipecah ke `GOMAXPROCS` goroutine per batch (`BatchSize=512`), merge df komutatif → deterministik |
| `vectorize` (transform TF-IDF) | ✅ **goroutine per-batch** | `Model.Transform` memproses batch paralel, **penulisan tetap berurutan** → file identik dengan versi sekuensial |
| `vectorize` (ONNX) | ✅ **goroutine per batch** | batch inference (batch ≤8) diproses worker pool per batch via ONNX Runtime session, **penulisan tetap berurutan** → file identik |
| `index` | ✅ **`-workers` goroutine** | pembacaan file tetap sinkron (pasangan dokumen↔vektor), tetapi request upsert dikirim oleh pool worker (default 4) |
| `search` | — | satu query tunggal |

Hasil ukur nyata (8.153 point, Qdrant lokal): `workers=1` → **3.09s**,
`workers=4` → **1.03s** (≈3×). Seluruh kode paralel diuji dengan
`go test -race ./...` — tanpa data race.

---

## Menjalankan test

```bash
gofmt -l . && go vet ./... && go test ./...
```

Test menutupi: parsing XML OAI-PMH (record, `resumptionToken`, `error`,
record terhapus), sanitasi karakter ilegal, checkpoint harvest, cleansing
(termasuk filter metadata legacy `Options`), normalisasi (hex set, penulis,
tanggal), TF-IDF (deterministik, L2-normalisasi, konsistensi sparse↔dense,
round-trip model, **kesetaraan df paralel vs referensi sekuensial**,
urutan `Transform`), **embedding ONNX** (encoder 384-dim, L2-normalisasi,
vektor kosong), round-trip file vektor biner, dan klien Qdrant +
**upsert paralel** (mock `httptest`, dibuktikan konkuren & error terpropagasi).

---

## Catatan

- **Etika crawling**: default `-delay 500ms` + retry dengan exponential backoff
  untuk 5xx/429. Naikkan delay bila mengambil korpus penuh.
- Korpus UNDIP sangat besar; harvest penuh memakan waktu lama. Karena
  checkpoint disimpan per halaman, proses aman dihentikan kapan pun.
- Kualitas pencarian: dengan engine **ONNX** (default `auto` bila model tersedia),
  hasil lebih relevan karena pemahaman semantik lintas bahasa (contoh: "cars"
  ≈ "mobil"/"otomotif"). Dengan TF-IDF hasil lebih ringkas & cepat (dim 1024).
- Tahap REST API belum dibuat (sesuai permintahan); struktur `internal/*`
  yang sudah terpisah dari CLI memudahkan penambahan handler HTTP nanti.
