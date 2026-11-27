import time

import numpy as np
import pytest
from fastapi.testclient import TestClient
from google.genai import errors as genai_errors

from service import app
from service.app import app as api


class FakeModel:
    def encode(self, text):
        return np.array([1.0, 0.0])


class FakeCollection:
    """`distances` = cosine distances as returned by ChromaDB (ascending)."""

    metadata = {"hnsw:space": "cosine"}

    def __init__(self, distances, metadatas=None):
        self.distances = distances
        self.metadatas = metadatas

    def query(self, query_embeddings, n_results, include):
        if self.metadatas is not None:
            metas = self.metadatas
        else:
            metas = [
                {
                    "title": f"Judul {i}",
                    "creators": "A, B",
                    "description": f"Abstrak {i}",
                    "url": f"http://x/{i}.pdf",
                    "date": "2021",
                }
                for i in range(len(self.distances))
            ]
        return {"metadatas": [metas], "distances": [self.distances]}

    def count(self):
        return len(self.distances)


def make_client(distances, summarize=None, metadatas=None, raise_server_exceptions=True):
    # TestClient is used WITHOUT `with`, so lifespan (real model + ChromaDB)
    # never runs; the components are replaced with fakes instead.
    app.app.state.model = FakeModel()
    app.app.state.collection = FakeCollection(distances, metadatas)
    app.app.state.summarize = summarize or (lambda q, r: "Ringkasan [1]")
    return TestClient(api, raise_server_exceptions=raise_server_exceptions)


@pytest.fixture(autouse=True)
def _no_history(monkeypatch, tmp_path):
    """Keep /ready deterministic: never read a real data/history.json."""
    monkeypatch.setattr(app, "HISTORY_PATH", tmp_path / "history.json")


# ---------- input validation ----------
@pytest.mark.parametrize(
    "body",
    [
        {"query": ""},
        {"query": "   "},
        {"query": "kemiskinan", "threshold": app.THRESHOLD_MAX + 0.01},
        {"query": "kemiskinan", "threshold": app.THRESHOLD_MIN - 0.01},
        {"query": "kemiskinan", "max_threshold": app.THRESHOLD_MAX + 0.01},
        {"query": "kemiskinan", "max_threshold": app.THRESHOLD_MIN - 0.01},
        {},
    ],
)
def test_invalid_input_rejected(body):
    assert make_client([0.1]).post("/search", json=body).status_code == 422


# ---------- configuration through the environment ----------
def test_threshold_bounds_from_environment_are_used(monkeypatch):
    # The threshold bounds are now a deployment setting, not a dead constant.
    # Pydantic validates `ge`/`le` when the class is created, so the modules are
    # reloaded with the new environment: that is what proves the env value
    # actually reaches the request.
    import importlib

    from common import config as settings

    monkeypatch.setenv("THRESHOLD_MIN", "0.4")
    monkeypatch.setenv("THRESHOLD_MAX", "0.5")
    monkeypatch.setenv("THRESHOLD_DEFAULT", "0.45")
    try:
        importlib.reload(settings)
        reloaded = importlib.reload(app)
        reloaded.app.state.model = FakeModel()
        reloaded.app.state.collection = FakeCollection([0.1])
        reloaded.app.state.summarize = lambda q, r: "Ringkasan [1]"
        client = TestClient(reloaded.app)
        assert (
            client.post("/search", json={"query": "x", "threshold": 0.45}).status_code
            == 200
        )
        assert (
            client.post("/search", json={"query": "x", "threshold": 0.7}).status_code
            == 422
        )
    finally:
        for name in ("THRESHOLD_MIN", "THRESHOLD_MAX", "THRESHOLD_DEFAULT"):
            monkeypatch.delenv(name, raising=False)
        importlib.reload(settings)
        importlib.reload(app)  # restore the module to its default state


def test_n_results_from_environment_is_used(monkeypatch):
    # `from settings import SETTINGS` binds the object at import time, so
    # changing os.environ alone is not enough: `settings` must be reloaded too.
    # This is the order uvicorn/Compose use when the process really starts.
    import importlib

    from common import config as settings

    monkeypatch.setenv("SEARCH_N_RESULTS", "7")
    try:
        importlib.reload(settings)
        reloaded = importlib.reload(app)

        seen = {}

        class RecordingCollection(FakeCollection):
            def query(self, query_embeddings, n_results, include):
                seen["n_results"] = n_results
                return super().query(query_embeddings, n_results, include)

        reloaded.app.state.model = FakeModel()
        reloaded.app.state.collection = RecordingCollection([0.9])
        reloaded.app.state.summarize = lambda q, r: "Ringkasan [1]"
        TestClient(reloaded.app).post("/search", json={"query": "x"})

        assert reloaded.SETTINGS.n_results == 7
        assert seen["n_results"] == 7
    finally:
        monkeypatch.delenv("SEARCH_N_RESULTS", raising=False)
        importlib.reload(settings)
        importlib.reload(app)


def test_cors_list_follows_environment():
    kwargs = next(
        middleware.kwargs
        for middleware in app.app.user_middleware
        if "allow_origins" in middleware.kwargs
    )
    assert kwargs["allow_origins"] == list(app.SETTINGS.cors_allow_origins)


def test_threshold_at_bounds_accepted():
    c = make_client([0.1])
    for t in (app.THRESHOLD_MIN, app.THRESHOLD_MAX):
        assert c.post("/search", json={"query": "x", "threshold": t}).status_code == 200


# ---------- threshold: higher means stricter ----------
def test_higher_threshold_returns_fewer_results():
    c = make_client([0.1, 0.3, 0.5, 0.7])  # similarity 0.9, 0.7, 0.5, 0.3
    loose = c.post("/search", json={"query": "x", "threshold": 0.3}).json()
    strict = c.post("/search", json={"query": "x", "threshold": 0.7}).json()
    assert loose["count"] == 4
    assert strict["count"] == 2
    assert [r["skor"] for r in strict["results"]] == [0.9, 0.7]


def test_similarity_equal_to_threshold_still_passes():
    # distance 0.3 -> similarity 0.7; the float 1-0.3 must still pass at 0.7
    r = make_client([0.3]).post("/search", json={"query": "x", "threshold": 0.7})
    assert r.json()["count"] == 1


# ---------- the three response paths ----------
def test_no_result_includes_guidance():
    r = make_client([0.9, 0.8]).post("/search", json={"query": "x", "threshold": 0.5})
    body = r.json()
    assert r.status_code == 200
    assert body["status"] == "no_results"
    assert body["count"] == 0 and body["results"] == []
    assert body["arahan"] and body["ringkasan"] is None


def test_default_threshold_filters_at_measured_boundary():
    # distance 0.35 -> similarity 0.65: passes the calibrated default (0.60).
    body = make_client([0.35]).post("/search", json={"query": "x"}).json()
    assert body["threshold"] == app.THRESHOLD_DEFAULT
    assert body["count"] == 1


def test_default_threshold_rejects_score_below_it():
    # distance 0.45 -> similarity 0.55: below the 0.60 default, so the result is
    # empty. This is the measured boundary: off-topic noise tops out at 0.559.
    body = make_client([0.45]).post("/search", json={"query": "x"}).json()
    assert body["threshold"] == app.THRESHOLD_DEFAULT
    assert body["status"] == "no_results"


def test_max_threshold_accepted_as_similarity_alias():
    # The AISEn frontend still sends `max_threshold` (0.4/0.5/0.6).
    # The value is a minimum similarity, exactly like `threshold`.
    body = (
        make_client([0.1, 0.5])
        .post("/search", json={"query": "x", "max_threshold": 0.6})
        .json()
    )
    assert body["threshold"] == 0.6
    assert body["count"] == 1  # 0.9 passes, 0.5 does not


def test_response_carries_old_and_new_name():
    body = make_client([0.1]).post("/search", json={"query": "x"}).json()
    assert body["threshold"] == app.THRESHOLD_DEFAULT
    assert body["max_threshold"] == body["threshold"]  # legacy alias, always in sync


def test_no_results_report_best_score():
    # distance 0.9 -> similarity 0.099...; threshold 0.5 passes nothing.
    body = (
        make_client([0.9]).post("/search", json={"query": "x", "threshold": 0.5}).json()
    )
    assert body["skor_tertinggi"] == 0.1
    assert "ambang kemiripan 0.50" in body["arahan"][0]
    assert "0.10" in body["arahan"][1]
    # Relevance is not negotiable: never suggest lowering the threshold.
    assert not any("turunkan threshold" in a.lower() for a in body["arahan"])


def test_cors_allows_frontend_from_other_origin():
    # The demo can be served from any port/host; the preflight must not block.
    r = make_client([0.1]).options(
        "/search",
        headers={
            "Origin": "http://localhost:3000",
            "Access-Control-Request-Method": "POST",
        },
    )
    assert r.status_code == 200
    assert r.headers["access-control-allow-origin"] == "*"


def test_empty_db_does_not_suggest_lowering_threshold():
    body = make_client([]).post("/search", json={"query": "x"}).json()
    assert body["status"] == "no_results"
    assert body["skor_tertinggi"] is None
    assert "Belum ada dokumen" in body["arahan"][0]


def test_incomplete_metadata_does_not_error():
    # Metadata from ingest.py can change or be missing; the response must stay 200.
    body = (
        make_client([0.1], metadatas=[{"title": "Hanya judul"}])
        .post("/search", json={"query": "x"})
        .json()
    )
    assert body["status"] != "no_results"
    first = body["results"][0]
    assert first["judul"] == "Hanya judul"
    assert first["penulis"] == "" and first["url"] == "" and first["date"] == ""


def test_none_metadata_does_not_error():
    # Chroma returns metadata=None for a document with no metadata at all.
    body = (
        make_client([0.1], metadatas=[None]).post("/search", json={"query": "x"}).json()
    )
    assert body["status"] != "no_results"
    assert body["results"][0] == {
        "peringkat": 1,
        "skor": 0.9,
        "judul": "",
        "penulis": "",
        "deskripsi": "",
        "url": "",
        "date": "",
    }


def test_full_result_leaves_best_score_empty():
    body = make_client([0.1]).post("/search", json={"query": "x"}).json()
    assert body["skor_tertinggi"] is None


def test_full_result_with_summary():
    r = make_client([0.1, 0.2, 0.9]).post(
        "/search", json={"query": "x", "threshold": 0.5}
    )
    body = r.json()
    assert body["status"] == "full"
    assert body["count"] == 2
    assert body["ringkasan"] == "Ringkasan [1]"
    assert body["ringkasan_error"] is None
    assert [x["peringkat"] for x in body["results"]] == [1, 2]


def test_llm_failure_falls_back_with_error_message():
    def broken(q, r):
        raise app.LLMError("Kuota habis.")

    body = (
        make_client([0.1, 0.2], summarize=broken)
        .post("/search", json={"query": "x"})
        .json()
    )
    assert body["status"] == "list_only"
    assert body["ringkasan"] is None
    assert body["ringkasan_error"] == "Kuota habis."
    assert body["count"] == 2


def test_unexpected_error_does_not_leak_details():
    def boom(q, r):
        raise ValueError("rahasia internal")

    body = (
        make_client([0.1], summarize=boom).post("/search", json={"query": "x"}).json()
    )
    assert body["status"] == "list_only"
    assert "rahasia" not in body["ringkasan_error"]


def test_llm_timeout(monkeypatch):
    # `Settings` is frozen, so the timeout is changed through the environment
    # and a reload rather than by mutating the shared object.
    import importlib

    from common import config as settings

    monkeypatch.setenv("LLM_TIMEOUT_SECONDS", "0.05")
    try:
        importlib.reload(settings)
        reloaded = importlib.reload(app)

        def slow(q, r):
            time.sleep(0.3)
            return "terlambat"

        reloaded.app.state.model = FakeModel()
        reloaded.app.state.collection = FakeCollection([0.1])
        reloaded.app.state.summarize = slow
        body = (
            TestClient(reloaded.app)
            .post("/search", json={"query": "x"})
            .json()
        )
        assert body["status"] == "list_only"
        assert "tepat waktu" in body["ringkasan_error"]
    finally:
        monkeypatch.delenv("LLM_TIMEOUT_SECONDS", raising=False)
        importlib.reload(settings)
        importlib.reload(app)


def test_llm_receives_top_k_only():
    received = {}

    def spy(query, results):
        received["n"] = len(results)
        return "ok"

    make_client([0.01 * i for i in range(20)], summarize=spy).post(
        "/search", json={"query": "x", "threshold": 0.5}
    )
    assert received["n"] == app.SETTINGS.summary_top_k


# ---------- LLM: configuration, errors, citations ----------
def test_llm_not_configured(monkeypatch):
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_MODEL", raising=False)
    with pytest.raises(app.LLMError, match="belum dikonfigurasi"):
        app.summarize_with_llm("x", [])


class _FakeModels:
    def __init__(self, behavior):
        self.behavior = behavior

    def generate_content(self, model, contents):
        return self.behavior()


class _FakeClient:
    def __init__(self, behavior):
        self.models = _FakeModels(behavior)


def _patch_client(monkeypatch, behavior):
    monkeypatch.setenv("GEMINI_API_KEY", "k")
    monkeypatch.setenv("GEMINI_MODEL", "m")
    from google import genai

    monkeypatch.setattr(genai, "Client", lambda api_key: _FakeClient(behavior))


def _results(n):
    return [
        app.SearchResult(
            peringkat=i,
            skor=0.9,
            judul=f"J{i}",
            penulis="A",
            deskripsi="d",
            url="u",
            date="2021",
        )
        for i in range(1, n + 1)
    ]


def test_llm_429_becomes_quota_message(monkeypatch):
    def raise_429():
        raise genai_errors.APIError(429, {"error": {"message": "quota"}})

    _patch_client(monkeypatch, raise_429)
    with pytest.raises(app.LLMError, match="Kuota"):
        app.summarize_with_llm("x", _results(2))


def test_llm_404_points_at_model_setting(monkeypatch):
    # A 404 usually means GEMINI_MODEL is wrong; the message must point there.
    def raise_404():
        raise genai_errors.APIError(404, {"error": {"message": "model not found"}})

    _patch_client(monkeypatch, raise_404)
    with pytest.raises(app.LLMError, match="GEMINI_MODEL"):
        app.summarize_with_llm("x", _results(2))


def test_llm_empty_text_counts_as_failure(monkeypatch):
    class Resp:
        text = "   "

    _patch_client(monkeypatch, lambda: Resp())
    with pytest.raises(app.LLMError, match="tidak menghasilkan"):
        app.summarize_with_llm("x", _results(2))


def test_llm_success_and_invalid_citations_dropped(monkeypatch):
    class Resp:
        text = "Kemiskinan menurun [1][2]. Klaim lain [7]."

    _patch_client(monkeypatch, lambda: Resp())
    out = app.summarize_with_llm("x", _results(2))
    assert "[1]" in out and "[2]" in out
    assert "[7]" not in out


def test_combined_citations_are_split():
    # The model sometimes still writes [1, 2, 3]; the UI needs [1] [2] [3].
    out = app.sanitize_citations("A [1, 2, 3]. B [7].", {1, 2, 3})
    assert "[1] [2] [3]" in out
    assert "[7]" not in out
    assert "[1, 2, 3]" not in out


def test_prompt_contains_citation_rule_and_sources():
    prompt = app.build_prompt("kemiskinan", _results(2))
    assert "kemiskinan" in prompt
    assert "[1]" in prompt and "[2]" in prompt
    assert "180 kata" in prompt
    assert "jangan digabung" in prompt


# ---------- misc ----------
def test_health_is_liveness_only():
    # /health must not touch the DB: it backs the Compose healthcheck.
    body = make_client([0.1, 0.2, 0.3]).get("/health").json()
    assert body["status"] == "ok"
    assert body["version"] == app.APP_VERSION
    assert "documents" not in body


def test_ready_reports_a_usable_index(monkeypatch):
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_MODEL", raising=False)
    response = make_client([0.1, 0.2, 0.3]).get("/ready")
    assert response.status_code == 200
    body = response.json()
    assert body["status"] == "ok"
    assert body["documents"] == 3
    assert body["metric"] == "cosine"
    assert body["llm_configured"] is False
    assert body["problems"] == []


def test_ready_flags_llm_configured(monkeypatch):
    monkeypatch.setenv("GEMINI_API_KEY", "k")
    monkeypatch.setenv("GEMINI_MODEL", "m")
    assert make_client([0.1]).get("/ready").json()["llm_configured"] is True


def test_ready_rejects_an_empty_or_non_cosine_index():
    empty = make_client([]).get("/ready")
    assert empty.status_code == 503
    assert any("empty" in problem for problem in empty.json()["problems"])

    class WrongMetric(FakeCollection):
        metadata = {"hnsw:space": "l2"}

    client = make_client([0.1])
    app.app.state.collection = WrongMetric([0.1])
    body = client.get("/ready").json()
    assert any("cosine" in problem for problem in body["problems"])


def test_ready_reports_the_last_successful_ingest(tmp_path, monkeypatch):
    history = tmp_path / "history.json"
    history.write_text(
        "\n".join(
            [
                '{"status": "ok", "records_harvested": 200, "resume_from": null}',
                '{"status": "ok", "records_harvested": 500, "resume_from": {"records": 200}}',
            ]
        )
        + "\n",
        encoding="utf-8",
    )
    monkeypatch.setattr(app, "HISTORY_PATH", history)

    body = make_client([0.1]).get("/ready").json()

    assert body["problems"] == []
    # The most recent run, not the first one.
    assert body["last_ingest"]["records_harvested"] == 500
    assert body["last_ingest"]["resume_from"] == {"records": 200}


def test_ready_skips_a_torn_final_line(tmp_path, monkeypatch):
    # An append interrupted mid-line must not be read as an ingest record.
    history = tmp_path / "history.json"
    history.write_text(
        '{"status": "ok", "records_harvested": 500}\n{"status": "ok", "records_har',
        encoding="utf-8",
    )
    monkeypatch.setattr(app, "HISTORY_PATH", history)

    body = make_client([0.1]).get("/ready").json()

    assert body["problems"] == []
    assert body["last_ingest"]["records_harvested"] == 500


def test_ready_without_history_has_no_last_ingest(tmp_path, monkeypatch):
    monkeypatch.setattr(app, "HISTORY_PATH", tmp_path / "history.json")
    body = make_client([0.1]).get("/ready").json()
    assert body["last_ingest"] is None


def test_request_id_is_echoed():
    response = make_client([0.1]).get("/health", headers={"X-Request-ID": "abc123"})
    assert response.headers["X-Request-ID"] == "abc123"


def test_unhandled_error_returns_request_id():
    client = make_client([0.1], raise_server_exceptions=False)
    app.app.state.collection = None  # makes /search raise AttributeErro

    response = client.get("/health")  # sanity: unrelated route still fine
    assert response.status_code == 200

    response = client.post(
        "/search", json={"query": "x"}, headers={"X-Request-ID": "boom1"}
    )
    assert response.status_code == 500
    assert response.json()["request_id"] == "boom1"


def test_startup_fails_clearly_when_collection_missing(tmp_path, monkeypatch):
    import importlib

    from common import config as settings

    monkeypatch.setenv("CHROMA_DB_PATH", str(tmp_path / "missing"))
    try:
        importlib.reload(settings)
        reloaded = importlib.reload(app)
        with pytest.raises(RuntimeError, match="was not found"):
            with TestClient(reloaded.app):
                pass
    finally:
        monkeypatch.delenv("CHROMA_DB_PATH", raising=False)
        importlib.reload(settings)
        importlib.reload(app)


# ---------- device selection ----------
def test_load_model_passes_the_resolved_device(monkeypatch):
    # The API embeds queries with the same model, so it must honour the same
    # setting. Loading it without `device=` would silently ignore EMBED_DEVICE.
    import sys
    from types import SimpleNamespace

    recorded = {}

    class RecordingTransformer:
        def __init__(self, name, device=None):
            recorded["name"] = name
            recorded["device"] = device

    monkeypatch.setitem(
        sys.modules,
        "sentence_transformers",
        SimpleNamespace(SentenceTransformer=RecordingTransformer),
    )
    monkeypatch.setattr(app, "resolve_device", lambda requested: "cuda")

    target = SimpleNamespace(state=SimpleNamespace())
    app._load_model(target)

    assert recorded["device"] == "cuda"
    assert isinstance(target.state.model, RecordingTransformer)
