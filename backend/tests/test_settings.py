import pytest

from settings import Settings


# ---------- defaults ----------
def test_defaults_sama_dengan_konstanta_lama():
    # Environment kosong harus berperilaku persis seperti sebelum config dipindah.
    s = Settings.from_env({})
    assert s.model_name == "sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2"
    assert s.db_path == "./data/vector_db"
    assert s.collection_name == "undip_research_search"
    assert s.n_results == 100
    assert s.summary_top_k == 5
    assert s.llm_timeout_seconds == 15
    assert s.log_level == "INFO"
    assert (s.threshold_min, s.threshold_max, s.threshold_default) == (0.1, 0.9, 0.53)
    assert s.cors_allow_origins == ("*",)
    assert s.oai_endpoint == "https://eprints2.undip.ac.id/cgi/oai2"
    assert s.ingest_target == 1000
    assert s.ingest_json_path == "data/documents.json"
    assert (s.insert_batch_size, s.embed_batch_size) == (1000, 64)
    assert s.min_description_chars == 50


def test_override_tipe_dibaca_benar():
    s = Settings.from_env(
        {
            "EMBEDDING_MODEL": "  model/lain  ",
            "CHROMA_DB_PATH": "/tmp/db",
            "SEARCH_N_RESULTS": "20",
            "LLM_TIMEOUT_SECONDS": "2.5",
            "LOG_LEVEL": "warning",
            "THRESHOLD_DEFAULT": "0.6",
        }
    )
    assert s.model_name == "model/lain"  # spasi dibuang
    assert s.db_path == "/tmp/db"
    assert s.n_results == 20
    assert s.llm_timeout_seconds == 2.5
    assert s.log_level == "WARNING"  # dinormalisasi ke huruf besar
    assert s.threshold_default == 0.6


def test_cors_daftar_dipisah_koma():
    assert Settings.from_env(
        {"CORS_ALLOW_ORIGINS": "https://a.example, https://b.example"}
    ).cors_allow_origins == ("https://a.example", "https://b.example")


def test_nilai_kosong_diabaikan():
    # `.env` yang berisi `SEARCH_N_RESULTS=` jangan bikin startup gagal.
    s = Settings.from_env({"SEARCH_N_RESULTS": "", "LOG_LEVEL": "  "})
    assert s.n_results == 100
    assert s.log_level == "INFO"


def test_cors_kosong_kembali_ke_default():
    assert Settings.from_env({"CORS_ALLOW_ORIGINS": " , "}).cors_allow_origins == ("*",)


# ---------- validasi ----------
@pytest.mark.parametrize(
    "env",
    [
        {"SEARCH_N_RESULTS": "banyak"},
        {"LLM_TIMEOUT_SECONDS": "cepat"},
        {"THRESHOLD_DEFAULT": "tinggi"},
    ],
)
def test_angka_tidak_valid_menyebut_nama_variabel(env):
    name = next(iter(env))
    with pytest.raises(ValueError, match=name):
        Settings.from_env(env)


@pytest.mark.parametrize(
    "env",
    [
        {"THRESHOLD_DEFAULT": "0.05"},  # di bawah MIN
        {"THRESHOLD_DEFAULT": "0.95"},  # di atas MAX
        {"THRESHOLD_MIN": "0.6", "THRESHOLD_DEFAULT": "0.55"},  # tidak konsisten
        {"THRESHOLD_MAX": "1.5"},  # di luar 0..1
        {"SUMMARY_TOP_K": "200"},  # lebih besar dari N_RESULTS
        {"LOG_LEVEL": "VERBOSE"},
        {"INSERT_BATCH_SIZE": "6000"},  # di atas batas ChromaDB
        {"EMBED_BATCH_SIZE": "0"},
    ],
)
def test_kombinasi_tidak_valid_ditolak(env):
    with pytest.raises(ValueError, match="Konfigurasi tidak valid"):
        Settings.from_env(env)


def test_validate_mengembalikan_self_saat_valid():
    assert Settings().validate() == Settings()
