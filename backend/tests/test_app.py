import time

import numpy as np
import pytest
from fastapi.testclient import TestClient
from google.genai import errors as genai_errors

import app
from app import app as api


class FakeModel:
    def encode(self, text):
        return np.array([1.0, 0.0])


class FakeCollection:
    """`distances` = jarak cosine yang dikembalikan ChromaDB (urut naik)."""

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


def make_client(distances, summarize=None, metadatas=None):
    # TestClient dipakai TANPA `with`, jadi lifespan (model + ChromaDB asli)
    # tidak dijalankan; komponen diganti dengan yang palsu.
    app.app.state.model = FakeModel()
    app.app.state.collection = FakeCollection(distances, metadatas)
    app.app.state.summarize = summarize or (lambda q, r: "Ringkasan [1]")
    return TestClient(api)


# ---------- validasi input ----------
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
def test_input_tidak_valid_ditolak(body):
    assert make_client([0.1]).post("/search", json=body).status_code == 422


def test_threshold_pada_batas_diterima():
    c = make_client([0.1])
    for t in (app.THRESHOLD_MIN, app.THRESHOLD_MAX):
        assert c.post("/search", json={"query": "x", "threshold": t}).status_code == 200


# ---------- threshold: makin tinggi makin ketat ----------
def test_threshold_lebih_tinggi_lebih_sedikit_hasil():
    c = make_client([0.1, 0.3, 0.5, 0.7])  # kemiripan 0.9, 0.7, 0.5, 0.3
    longgar = c.post("/search", json={"query": "x", "threshold": 0.3}).json()
    ketat = c.post("/search", json={"query": "x", "threshold": 0.7}).json()
    assert longgar["count"] == 4
    assert ketat["count"] == 2
    assert [r["skor"] for r in ketat["results"]] == [0.9, 0.7]


def test_kemiripan_sama_dengan_threshold_masih_lolos():
    # jarak 0.3 -> kemiripan 0.7; float 1-0.3 harus tetap lolos di threshold 0.7
    r = make_client([0.3]).post("/search", json={"query": "x", "threshold": 0.7})
    assert r.json()["count"] == 1


# ---------- tiga jalur respons ----------
def test_tidak_ada_jurnal_lolos_memberi_arahan():
    r = make_client([0.9, 0.8]).post("/search", json={"query": "x", "threshold": 0.5})
    body = r.json()
    assert r.status_code == 200
    assert body["status"] == "no_results"
    assert body["count"] == 0 and body["results"] == []
    assert body["arahan"] and body["ringkasan"] is None


def test_threshold_bawaan_menyaring_di_batas_ukur():
    # Jarak 0.45 -> kemiripan 0.55: lolos di default hasil kalibrasi.
    body = make_client([0.45]).post("/search", json={"query": "x"}).json()
    assert body["threshold"] == app.THRESHOLD_DEFAULT
    assert body["count"] == 1


def test_threshold_bawaan_menolak_skor_di_bawahnya():
    # Jarak 0.5 -> kemiripan 0.5: di bawah default, jadi hasil kosong.
    body = make_client([0.5]).post("/search", json={"query": "x"}).json()
    assert body["threshold"] == app.THRESHOLD_DEFAULT
    assert body["status"] == "no_results"


def test_max_threshold_diterima_sebagai_alias_kemiripan():
    # Frontend AISEn masih mengirim `max_threshold` (0.4/0.5/0.6).
    # Nilainya kemiripan minimum, sama seperti `threshold`.
    body = make_client([0.1, 0.5]).post(
        "/search", json={"query": "x", "max_threshold": 0.6}
    ).json()
    assert body["threshold"] == 0.6
    assert body["count"] == 1  # 0.9 lolos, 0.5 tidak


def test_respons_memuat_nama_lama_dan_baru():
    body = make_client([0.1]).post("/search", json={"query": "x"}).json()
    assert body["threshold"] == app.THRESHOLD_DEFAULT
    assert body["max_threshold"] == body["threshold"]  # alias lama, selalu sinkron


def test_no_results_menyebut_skor_tertinggi():
    # jarak 0.9 -> kemiripan 0.099...; threshold 0.5 tidak meloloskan apa pun.
    body = make_client([0.9]).post(
        "/search", json={"query": "x", "threshold": 0.5}
    ).json()
    assert body["skor_tertinggi"] == 0.1
    assert "ambang kemiripan 0.50" in body["arahan"][0]
    assert "0.10" in body["arahan"][1]
    # Relevansi tidak dinegosiasikan: jangan menyarankan menurunkan threshold.
    assert not any("turunkan threshold" in a.lower() for a in body["arahan"])


def test_cors_mengizinkan_frontend_dari_origin_lain():
    # Demo bisa jalan dari port/host apa pun; preflight jangan diblokir.
    r = make_client([0.1]).options(
        "/search",
        headers={
            "Origin": "http://localhost:3000",
            "Access-Control-Request-Method": "POST",
        },
    )
    assert r.status_code == 200
    assert r.headers["access-control-allow-origin"] == "*"


def test_db_kosong_tidak_menyarankan_turunkan_threshold():
    body = make_client([]).post("/search", json={"query": "x"}).json()
    assert body["status"] == "no_results"
    assert body["skor_tertinggi"] is None
    assert "Belum ada dokumen" in body["arahan"][0]


def test_metadata_tidak_lengkap_tidak_menyebabkan_error():
    # Metadata dari ingest.py bisa berubah/bolong; respons harus tetap 200.
    body = make_client(
        [0.1], metadatas=[{"title": "Hanya judul"}]
    ).post("/search", json={"query": "x"}).json()
    assert body["status"] != "no_results"
    hasil = body["results"][0]
    assert hasil["judul"] == "Hanya judul"
    assert hasil["penulis"] == "" and hasil["url"] == "" and hasil["date"] == ""


def test_metadata_none_tidak_menyebabkan_error():
    # Chroma mengembalikan metadata=None untuk dokumen tanpa metadata sama sekali.
    body = make_client([0.1], metadatas=[None]).post("/search", json={"query": "x"}).json()
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


def test_hasil_lengkap_tidak_mengisi_skor_tertinggi():
    body = make_client([0.1]).post("/search", json={"query": "x"}).json()
    assert body["skor_tertinggi"] is None


def test_hasil_lengkap_dengan_ringkasan():
    r = make_client([0.1, 0.2, 0.9]).post("/search", json={"query": "x", "threshold": 0.5})
    body = r.json()
    assert body["status"] == "full"
    assert body["count"] == 2
    assert body["ringkasan"] == "Ringkasan [1]"
    assert body["ringkasan_error"] is None
    assert [x["peringkat"] for x in body["results"]] == [1, 2]


def test_llm_gagal_fallback_dan_pesan_error():
    def broken(q, r):
        raise app.LLMError("Kuota habis.")

    body = make_client([0.1, 0.2], summarize=broken).post(
        "/search", json={"query": "x"}
    ).json()
    assert body["status"] == "list_only"
    assert body["ringkasan"] is None
    assert body["ringkasan_error"] == "Kuota habis."
    assert body["count"] == 2


def test_error_tak_terduga_tidak_membocorkan_detail():
    def boom(q, r):
        raise ValueError("rahasia internal")

    body = make_client([0.1], summarize=boom).post("/search", json={"query": "x"}).json()
    assert body["status"] == "list_only"
    assert "rahasia" not in body["ringkasan_error"]


def test_llm_timeout(monkeypatch):
    monkeypatch.setattr(app, "LLM_TIMEOUT_SECONDS", 0.05)

    def lambat(q, r):
        time.sleep(0.3)
        return "terlambat"

    body = make_client([0.1], summarize=lambat).post("/search", json={"query": "x"}).json()
    assert body["status"] == "list_only"
    assert "tepat waktu" in body["ringkasan_error"]


def test_llm_hanya_menerima_top_k():
    received = {}

    def spy(query, results):
        received["n"] = len(results)
        return "ok"

    make_client([0.01 * i for i in range(20)], summarize=spy).post(
        "/search", json={"query": "x", "threshold": 0.5}
    )
    assert received["n"] == app.SUMMARY_TOP_K


# ---------- LLM: konfigurasi, error, sitasi ----------
def test_llm_belum_dikonfigurasi(monkeypatch):
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


def _hasil(n):
    return [
        app.SearchResult(
            peringkat=i, skor=0.9, judul=f"J{i}", penulis="A",
            deskripsi="d", url="u", date="2021",
        )
        for i in range(1, n + 1)
    ]


def test_llm_429_jadi_pesan_kuota(monkeypatch):
    def raise_429():
        raise genai_errors.APIError(429, {"error": {"message": "quota"}})

    _patch_client(monkeypatch, raise_429)
    with pytest.raises(app.LLMError, match="Kuota"):
        app.summarize_with_llm("x", _hasil(2))


def test_llm_model_tidak_dikenal_jadi_pesan_jelas(monkeypatch):
    # 404 biasanya berarti GEMINI_MODEL salah; pesannya harus menunjuk ke sana.
    def raise_404():
        raise genai_errors.APIError(404, {"error": {"message": "model not found"}})

    _patch_client(monkeypatch, raise_404)
    with pytest.raises(app.LLMError, match="GEMINI_MODEL"):
        app.summarize_with_llm("x", _hasil(2))


def test_llm_teks_kosong_dianggap_gagal(monkeypatch):
    class Resp:
        text = "   "

    _patch_client(monkeypatch, lambda: Resp())
    with pytest.raises(app.LLMError, match="tidak menghasilkan"):
        app.summarize_with_llm("x", _hasil(2))


def test_llm_sukses_dan_sitasi_tidak_valid_dibuang(monkeypatch):
    class Resp:
        text = "Kemiskinan menurun [1][2]. Klaim lain [7]."

    _patch_client(monkeypatch, lambda: Resp())
    out = app.summarize_with_llm("x", _hasil(2))
    assert "[1]" in out and "[2]" in out
    assert "[7]" not in out


def test_sitasi_gabungan_dipecah_jadi_satu_per_satu():
    # Model kadang tetap menulis [1, 2, 3]; UI butuh [1] [2] [3].
    out = app.sanitize_citations("A [1, 2, 3]. B [7].", {1, 2, 3})
    assert "[1] [2] [3]" in out
    assert "[7]" not in out
    assert "[1, 2, 3]" not in out


def test_prompt_memuat_instruksi_sitasi_dan_sumber():
    prompt = app.build_prompt("kemiskinan", _hasil(2))
    assert "kemiskinan" in prompt
    assert "[1]" in prompt and "[2]" in prompt
    assert "180 kata" in prompt
    assert "jangan digabung" in prompt


# ---------- lain-lain ----------
def test_health(monkeypatch):
    monkeypatch.delenv("GEMINI_API_KEY", raising=False)
    monkeypatch.delenv("GEMINI_MODEL", raising=False)
    assert make_client([0.1, 0.2, 0.3]).get("/health").json() == {
        "status": "ok",
        "documents": 3,
        "llm_configured": False,
        "metric": "cosine",
    }


def test_health_menandai_llm_terkonfigurasi(monkeypatch):
    monkeypatch.setenv("GEMINI_API_KEY", "k")
    monkeypatch.setenv("GEMINI_MODEL", "m")
    assert make_client([0.1]).get("/health").json()["llm_configured"] is True


def test_startup_gagal_jelas_bila_koleksi_tidak_ada(tmp_path, monkeypatch):
    monkeypatch.setattr(app, "DB_PATH", str(tmp_path / "kosong"))
    with pytest.raises(RuntimeError, match="was not found"):
        with TestClient(api):
            pass
