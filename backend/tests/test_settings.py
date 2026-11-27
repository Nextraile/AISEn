import pytest

from common.config import Settings


# ---------- defaults ----------
def test_defaults_match_previous_constants():
    # An empty environment must behave exactly as before the config move.
    s = Settings.from_env({})
    assert s.model_name == "sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2"
    assert s.db_path == "./data/vector_db"
    assert s.collection_name == "undip_research_search"
    assert s.n_results == 100
    assert s.summary_top_k == 5
    assert s.llm_timeout_seconds == 15
    assert s.log_level == "INFO"
    assert (s.threshold_min, s.threshold_max, s.threshold_default) == (0.1, 0.9, 0.60)
    assert s.cors_allow_origins == ("*",)
    assert s.oai_endpoint == "https://eprints2.undip.ac.id/cgi/oai2"
    assert s.ingest_target == 1000
    assert s.ingest_json_path == "data/documents.json"
    assert (s.insert_batch_size, s.embed_batch_size) == (1000, 64)
    assert s.min_description_chars == 50
    assert s.embed_device == "auto"


def test_overrides_are_read_with_the_right_type():
    s = Settings.from_env(
        {
            "EMBEDDING_MODEL": "  model/lain  ",
            "CHROMA_DB_PATH": "/tmp/db",
            "SEARCH_N_RESULTS": "20",
            "LLM_TIMEOUT_SECONDS": "2.5",
            "LOG_LEVEL": "warning",
            "THRESHOLD_DEFAULT": "0.6",
            "EMBED_DEVICE": " CUDA ",
        }
    )
    assert s.model_name == "model/lain"  # whitespace stripped
    assert s.embed_device == "cuda"  # normalised to lower case
    assert s.db_path == "/tmp/db"
    assert s.n_results == 20
    assert s.llm_timeout_seconds == 2.5
    assert s.log_level == "WARNING"  # normalised to upper case
    assert s.threshold_default == 0.6


def test_cors_list_is_comma_separated():
    assert Settings.from_env(
        {"CORS_ALLOW_ORIGINS": "https://a.example, https://b.example"}
    ).cors_allow_origins == ("https://a.example", "https://b.example")


def test_empty_values_are_ignored():
    # A `.env` file containing `SEARCH_N_RESULTS=` must not break startup.
    s = Settings.from_env({"SEARCH_N_RESULTS": "", "LOG_LEVEL": "  "})
    assert s.n_results == 100
    assert s.log_level == "INFO"


def test_empty_cors_falls_back_to_default():
    assert Settings.from_env({"CORS_ALLOW_ORIGINS": " , "}).cors_allow_origins == ("*",)


# ---------- validation ----------
@pytest.mark.parametrize(
    "env",
    [
        {"SEARCH_N_RESULTS": "many"},
        {"LLM_TIMEOUT_SECONDS": "fast"},
        {"THRESHOLD_DEFAULT": "high"},
    ],
)
def test_invalid_number_names_the_variable(env):
    name = next(iter(env))
    with pytest.raises(ValueError, match=name):
        Settings.from_env(env)


@pytest.mark.parametrize(
    "env",
    [
        {"THRESHOLD_DEFAULT": "0.05"},  # below MIN
        {"THRESHOLD_DEFAULT": "0.95"},  # above MAX
        {"THRESHOLD_MIN": "0.6", "THRESHOLD_DEFAULT": "0.55"},  # inconsistent
        {"THRESHOLD_MAX": "1.5"},  # outside 0..1
        {"SUMMARY_TOP_K": "200"},  # greater than N_RESULTS
        {"LOG_LEVEL": "VERBOSE"},
        {"INSERT_BATCH_SIZE": "6000"},  # above the ChromaDB limit
        {"EMBED_BATCH_SIZE": "0"},
        {"EMBED_DEVICE": "gpu"},  # not auto/cpu/cuda
    ],
)
def test_invalid_combination_rejected(env):
    with pytest.raises(ValueError, match="Invalid configuration"):
        Settings.from_env(env)


def test_validate_returns_self_when_valid():
    # Returns `self` so `from_env()` can build and validate in one expression.
    assert Settings().validate() == Settings()
