import asyncio
import logging
import os
import re
import time
from contextlib import asynccontextmanager
from typing import Literal

import chromadb
from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from pydantic import AliasChoices, BaseModel, ConfigDict, Field, computed_field
from starlette.concurrency import run_in_threadpool

from settings import SETTINGS

# Konfigurasi dibaca sekali dari environment (lihat `settings.py` dan
# `.env.example`). Alias di bawah ini yang dipakai kode dan test; nilai
# bawaannya identik dengan konstanta lama, jadi environment kosong berperilaku
# sama seperti sebelumnya.
MODEL_NAME = SETTINGS.model_name
DB_PATH = SETTINGS.db_path
COLLECTION_NAME = SETTINGS.collection_name
N_RESULTS = SETTINGS.n_results
THRESHOLD_MIN = SETTINGS.threshold_min
THRESHOLD_MAX = SETTINGS.threshold_max
THRESHOLD_DEFAULT = SETTINGS.threshold_default
SUMMARY_TOP_K = SETTINGS.summary_top_k
LLM_TIMEOUT_SECONDS = SETTINGS.llm_timeout_seconds

# Threshold = kemiripan minimum (1 - jarak cosine). Makin tinggi = makin ketat.
# Batas dan nilai bawaannya bisa diatur (THRESHOLD_MIN / THRESHOLD_MAX /
# THRESHOLD_DEFAULT); nilai bawaan diukur manual pada 1.000 dokumen asli
# (eprints2): query relevan 0.58-0.73, noise <= 0.46. Lihat README
# ("Why the default is 0.53") untuk tabel pengukurannya.

# Jumlah jurnal teratas yang dikirim ke LLM (SUMMARY_TOP_K, bisa disetel).
# Batas waktu menunggu LLM sebelum dianggap gagal (LLM_TIMEOUT_SECONDS).
# Diukur pada data asli (1.000 dokumen, gemini-3.5-flash-lite): panggilan
# normal 1,3-7 detik, tetapi ekor latensi dan beberapa request bersamaan
# bisa melewati 8 detik. 15 detik menutup hampir semua kasus tanpa membuat
# pengguna menunggu terlalu lama saat LLM benar-benar tidak merespons.

# Arahan saat tidak ada jurnal yang lolos threshold. Bersifat diagnostik:
# menjelaskan mengapa hasilnya kosong, tanpa menyuruh pengguna menurunkan
# ambang relevansi. Hasil kosong adalah jawaban yang sah.
GUIDANCE = [
    "Coba kata kunci yang lebih spesifik.",
    "Perluas topik pencarian.",
]

# Logging dasar untuk observability. Di bawah uvicorn, logger aplikasi
# tidak otomatis aktif, jadi dikonfigurasi di sini.
logging.basicConfig(
    level=SETTINGS.log_level,
    format="%(asctime)s %(levelname)s %(name)s %(message)s",
)
log = logging.getLogger("aisen.search")


# Validasi startup (dari app.py versi tim, dipertahankan)
@asynccontextmanager
async def lifespan(app: FastAPI):
    # Log sebelum membuka DB. Kalau direktori berada di network share
    # (mis. \\wsl.localhost di Windows), SQLite/Chroma bisa menggantung
    # tanpa pesan apa pun; log ini menandai tepat di mana ia berhenti.
    log.info("Membuka ChromaDB di '%s'...", DB_PATH)
    client = chromadb.PersistentClient(path=DB_PATH)

    try:
        app.state.collection = client.get_collection(name=COLLECTION_NAME)
    except chromadb.errors.NotFoundError as exc:
        raise RuntimeError(
            f"ChromaDB collection '{COLLECTION_NAME}' was not found "
            f"in '{DB_PATH}'. Check the database path and make sure "
            "you've run the data-ingestion step."
        ) from exc

    # Terlihat jelas di log: salah path DB / DB kosong jangan sampai
    # baru ketahuan saat demo (gejalanya cuma "tidak ada hasil").
    total_documents = app.state.collection.count()
    log.info(
        "ChromaDB siap: %d dokumen di koleksi '%s' (%s)",
        total_documents,
        COLLECTION_NAME,
        DB_PATH,
    )
    if total_documents == 0:
        log.warning(
            "Koleksi '%s' KOSONG: semua pencarian akan mengembalikan "
            "'no_results' meskipun kode berjalan normal.",
            COLLECTION_NAME,
        )
    if (app.state.collection.metadata or {}).get("hnsw:space") != "cosine":
        log.warning(
            "Koleksi '%s' tidak memakai hnsw:space=cosine: skor kemiripan "
            "bisa tidak valid (threshold 0.1-0.9 jadi tidak bermakna).",
            COLLECTION_NAME,
        )

    # Model dan indeks harus sepadan. Kalau EMBEDDING_MODEL diubah tanpa
    # `ingest.py --index`, vektor query dan vektor tersimpan berasal dari ruang
    # berbeda: pencarian tetap jalan tetapi skornya tidak bermakna.
    model_tercatat = (app.state.collection.metadata or {}).get("embedding_model")
    if model_tercatat and model_tercatat != MODEL_NAME:
        log.warning(
            "Koleksi '%s' dibangun dengan model '%s', tetapi EMBEDDING_MODEL "
            "sekarang '%s'. Jalankan ulang `python ingest.py --index`.",
            COLLECTION_NAME,
            model_tercatat,
            MODEL_NAME,
        )

    # Unduhan model pertama bisa memakan beberapa menit; tanpa log ini
    # startup terlihat seperti hang. Log ditulis SEBELUM import karena
    # `import sentence_transformers` (torch) sendiri butuh belasan detik.
    log.info("Memuat model embedding '%s' (unduhan pertama bisa lama)...", MODEL_NAME)

    # Import dipindah ke sini agar test yang tidak butuh model
    # tidak perlu memuat torch.
    from sentence_transformers import SentenceTransformer

    app.state.model = SentenceTransformer(MODEL_NAME)
    log.info("Model embedding siap.")
    app.state.summarize = summarize_with_llm  # mudah diganti saat test

    gemini_model = os.getenv("GEMINI_MODEL")
    if not (os.getenv("GEMINI_API_KEY") and gemini_model):
        log.warning(
            "GEMINI_API_KEY / GEMINI_MODEL belum diatur: ringkasan AI nonaktif, "
            "API hanya mengembalikan daftar jurnal."
        )
    else:
        # Nama model yang salah tidak membuat startup gagal; tanpa log ini
        # ringkasan AI "menghilang" tanpa jejak.
        log.info("Ringkasan AI aktif dengan model Gemini '%s'.", gemini_model)
    yield


app = FastAPI(
    title="Undip Research Semantic Search API",
    description="Semantic search over the harvested Undip eprints2 repository.",
    version="0.2.0",
    lifespan=lifespan,
)


# Prototipe/demo: bawaan `CORS_ALLOW_ORIGINS=*` karena API tidak memakai cookie
# maupun kredensial. Saat dipublikasikan, isi dengan origin frontend yang
# sebenarnya, mis. `CORS_ALLOW_ORIGINS=https://aisen.example.com`.
app.add_middleware(
    CORSMiddleware,
    allow_origins=list(SETTINGS.cors_allow_origins),
    allow_credentials=False,
    allow_methods=["*"],
    allow_headers=["*"],
)


class SearchRequest(BaseModel):
    # Spasi di awal/akhir dibuang, jadi query "   " ditolak (min_length=1).
    model_config = ConfigDict(str_strip_whitespace=True)

    query: str = Field(..., min_length=1, description="Free-text search query (Indonesian or English).")
    # `max_threshold` adalah nama lama yang masih dipakai klien frontend.
    # Nilainya SELALU kemiripan minimum (makin tinggi makin ketat), bukan jarak.
    threshold: float = Field(
        default=THRESHOLD_DEFAULT,
        validation_alias=AliasChoices("threshold", "max_threshold"),
        ge=THRESHOLD_MIN,
        le=THRESHOLD_MAX,
        description=(
            "Minimum similarity (1 - cosine distance). Higher = stricter. "
            "`max_threshold` is accepted as a deprecated alias."
        ),
    )


class SearchResult(BaseModel):
    peringkat: int
    skor: float  # BERUBAH: kemiripan (makin tinggi makin mirip), bukan jarak
    judul: str
    penulis: str
    deskripsi: str
    url: str
    date: str


class SearchResponse(BaseModel):
    query: str
    # `threshold` tetap nama kanonis untuk klien baru.
    threshold: float
    count: int
    results: list[SearchResult]
    # Field tambahan:
    #   full       -> ringkasan AI + daftar jurnal
    #   list_only  -> LLM gagal, daftar jurnal saja (alasannya di `ringkasan_error`)
    #   no_results -> tidak ada jurnal lolos threshold, lihat `arahan`
    status: Literal["full", "list_only", "no_results"]
    ringkasan: str | None = None  # sitasi berformat [n], n = `peringkat`
    ringkasan_error: str | None = None  # pesan untuk pengguna; coba lagi manual
    # Kemiripan tertinggi yang ditemukan walau tidak lolos threshold.
    # Membantu menilai apakah threshold kebablasan ketat atau DB-nya kosong.
    skor_tertinggi: float | None = None
    arahan: list[str] = Field(default_factory=list)

    @computed_field  # type: ignore[prop-decorator]
    @property
    def max_threshold(self) -> float:
        """Alias lama untuk `threshold` (dipakai frontend). Akan dihapus
        setelah klien pindah ke `threshold`; nilainya selalu sama."""
        return self.threshold


# ---- LLM (Google Gemini API) -----------------------------------------------
class LLMError(Exception):
    """Pesan exception ini ditampilkan ke pengguna, jadi tulis dalam Indonesia."""


def build_prompt(query: str, results: list[SearchResult]) -> str:
    sources = "\n\n".join(f"[{r.peringkat}] {r.judul}\n{r.deskripsi}" for r in results)
    return (
        "Anda adalah asisten riset. Tulis ringkasan singkat dalam bahasa Indonesia "
        "tentang isi, temuan utama, atau pola yang muncul dari jurnal yang disajikan. "
        "Gunakan HANYA sumber di bawah ini; jangan menambah fakta atau angka. "
        "Panjang maksimal 3 paragraf dan 180 kata. Utamakan poin paling relevan, "
        "hindari pengantar panjang dan jangan mengulang judul jurnal. "
        "Setiap klaim faktual wajib memiliki sitasi [n] sesuai nomor sumber. "
        "Tulis sitasi satu per satu, misalnya [1] [2] [3], jangan digabung "
        "menjadi satu kurung seperti [1, 2, 3]. "
        "Gunakan Markdown sederhana hanya untuk penekanan penting (**teks**) bila perlu. "
        "Jika sumber tidak cukup, katakan secara singkat.\n\n"
        f"Pertanyaan pengguna: {query}\n\nSumber jurnal:\n{sources}"
    )


_CITATION = re.compile(r"\[(\d+(?:\s*,\s*\d+)*)\]")


def sanitize_citations(text: str, valid: set[int]) -> str:
    """Buang sitasi [n] yang nomornya tidak ada di sumber yang dikirim ke LLM.

    Sitasi gabungan seperti [1, 2, 3] dipecah menjadi [1] [2] [3] agar setiap
    nomor bisa diklik satu per satu di UI.
    """

    def keep(m: re.Match) -> str:
        nomor = [int(n) for n in m.group(1).split(",")]
        dipakai = [n for n in nomor if n in valid]

        for n in nomor:
            if n not in valid:
                log.warning("Sitasi tidak valid dibuang: [%s]", n)

        return " ".join(f"[{n}]" for n in dipakai)

    return _CITATION.sub(keep, text)


def summarize_with_llm(query: str, results: list[SearchResult]) -> str:
    """Panggil Gemini. Tanpa retry otomatis: gagal -> LLMError, pengguna
    mencoba lagi secara manual."""
    api_key = os.getenv("GEMINI_API_KEY")
    model = os.getenv("GEMINI_MODEL")
    if not (api_key and model):
        raise LLMError("Ringkasan AI belum dikonfigurasi.")

    from google import genai
    from google.genai import errors as genai_errors

    try:
        client = genai.Client(api_key=api_key)
        resp = client.models.generate_content(
            model=model, contents=build_prompt(query, results)
        )
    except genai_errors.APIError as exc:
        if exc.code == 429:
            raise LLMError(
                "Kuota atau batas permintaan LLM tercapai. Coba lagi sebentar lagi."
            ) from exc
        if exc.code == 404:
            # Paling sering: GEMINI_MODEL salah/ sudah tidak tersedia.
            raise LLMError(
                f"Model LLM '{model}' tidak ditemukan. Periksa nilai GEMINI_MODEL."
            ) from exc
        raise LLMError(
            f"Layanan LLM mengembalikan error ({exc.code}). Coba lagi."
        ) from exc

    text = (resp.text or "").strip()
    if not text:
        raise LLMError("LLM tidak menghasilkan ringkasan. Coba lagi.")
    return sanitize_citations(text, {r.peringkat for r in results})


@app.get("/health")
def health() -> dict:
    # Untuk pengecekan hidup/mati dan jumlah dokumen ter-index.
    # `llm_configured` membuat konfigurasi Gemini bisa dicek sebelum demo
    # tanpa harus menembak /search; `metric` memastikan koleksi memakai cosine
    # (kalau tidak, `skor` di /search tidak bermakna).
    return {
        "status": "ok",
        "documents": app.state.collection.count(),
        "llm_configured": bool(
            os.getenv("GEMINI_API_KEY") and os.getenv("GEMINI_MODEL")
        ),
        "metric": (app.state.collection.metadata or {}).get("hnsw:space"),
    }


def metadata_text(metadata: dict, key: str) -> str:
    """Ambil metadata dengan aman.

    Bentuk metadata dimiliki `ingest.py`. Kalau satu dokumen saja
    kehilangan key (atau tipenya bukan string), jangan sampai seluruh
    pencarian jadi 500.
    """
    if not metadata:  # None atau {} (dokumen tanpa metadata sama sekali)
        return ""
    value = metadata.get(key)
    return "" if value is None else str(value)


@app.post("/search", response_model=SearchResponse)
async def search(payload: SearchRequest) -> SearchResponse:
    started = time.perf_counter()
    model = app.state.model
    collection = app.state.collection

    query_vector = await run_in_threadpool(model.encode, payload.query)
    query_vector = query_vector.tolist()

    raw = await run_in_threadpool(
        collection.query,
        query_embeddings=[query_vector],
        n_results=N_RESULTS,
        include=["metadatas", "distances"],
    )

    results: list[SearchResult] = []
    rank = 0
    for metadata, distance in zip(raw["metadatas"][0], raw["distances"][0]):
        similarity = round(1.0 - distance, 6)  # dibulatkan agar perbandingan stabil
        if similarity < payload.threshold:
            continue
        rank += 1
        results.append(
            SearchResult(
                peringkat=rank,
                skor=similarity,
                judul=metadata_text(metadata, "title"),
                penulis=metadata_text(metadata, "creators"),
                deskripsi=metadata_text(metadata, "description"),
                url=metadata_text(metadata, "url"),
                date=metadata_text(metadata, "date"),
            )
        )

    # Cabang flowchart setelah threshold
    ringkasan = None
    ringkasan_error = None
    skor_tertinggi: float | None = None
    arahan: list[str] = []
    if not results:
        status = "no_results"
        distances = raw["distances"][0] if raw["distances"] else []
        if distances:
            # Laporkan fakta: ambang yang dipakai dan skor tertinggi yang ada.
            # Tidak menyarankan menurunkan threshold, karena ambang itu memang
            # penjaga relevansi dan hasil kosong adalah jawaban yang sah.
            skor_tertinggi = round(1.0 - distances[0], 6)
            arahan = [
                f"Tidak ada jurnal yang mencapai ambang kemiripan "
                f"{payload.threshold:.2f}.",
                f"Kemiripan tertinggi yang ditemukan: {skor_tertinggi:.2f}.",
                *GUIDANCE,
            ]
        else:
            # DB kosong / belum di-ingest: jangan menyarankan menurunkan
            # threshold, itu menyesatkan.
            arahan = [
                "Belum ada dokumen terindeks di basis data.",
                "Jalankan python ingest.py --all untuk mengisi basis data.",
            ]
    else:
        try:
            ringkasan = await asyncio.wait_for(
                run_in_threadpool(
                    app.state.summarize, payload.query, results[:SUMMARY_TOP_K]
                ),
                timeout=LLM_TIMEOUT_SECONDS,
            )
            status = "full"
        except asyncio.TimeoutError:
            log.warning("LLM timeout setelah %ss", LLM_TIMEOUT_SECONDS)
            status = "list_only"
            ringkasan_error = "LLM tidak merespons tepat waktu. Coba lagi."
        except LLMError as exc:
            log.warning("LLM gagal: %s", exc)
            status = "list_only"
            ringkasan_error = str(exc)
        except Exception:
            log.exception("LLM gagal (error tak terduga)")
            status = "list_only"
            ringkasan_error = "Ringkasan gagal dibuat. Coba lagi."

    log.info(
        "search status=%s count=%d threshold=%.2f duration_ms=%.0f",
        status,
        len(results),
        payload.threshold,
        (time.perf_counter() - started) * 1000,
    )

    return SearchResponse(
        query=payload.query,
        threshold=payload.threshold,
        count=len(results),
        results=results,
        status=status,
        ringkasan=ringkasan,
        ringkasan_error=ringkasan_error,
        skor_tertinggi=skor_tertinggi,
        arahan=arahan,
    )
