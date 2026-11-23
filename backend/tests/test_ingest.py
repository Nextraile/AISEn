import json

import pytest

from ingestion import ingest


class FakeRecord:
    def __init__(self, metadata):
        self.metadata = metadata


def make_metadata(title, description, url="http://x/1.pdf", creators=None, date=None):
    metadata = {
        "title": [title],
        "description": [description],
        "identifier": [url],
        "creator": creators if creators is not None else ["A", "B"],
    }
    if date is not None:
        metadata["date"] = [date]
    return metadata


# ---------- harvest ----------
def test_harvest_skips_admin_short_and_broken_records(tmp_path):
    records = [
        object(),  # record without `.metadata` (deleted/broken)
        FakeRecord(make_metadata("Penilaian Sejawat", "x" * 100)),  # administrative
        FakeRecord(make_metadata("Tanpa Abstrak", "pendek")),  # <= 50 characters
        FakeRecord(make_metadata("Layak", "a" * 60, date="2024")),
    ]

    output = tmp_path / "clean.json"
    items = ingest.harvest(target=10, output=output, records=records)

    assert len(items) == 1
    assert items[0]["title"] == "Layak"
    assert items[0]["date"] == "2024"
    assert items[0]["creators"] == ["A", "B"]
    assert json.loads(output.read_text(encoding="utf-8")) == items


def test_harvest_fills_default_date(tmp_path):
    items = ingest.harvest(
        target=10,
        output=tmp_path / "clean.json",
        records=[FakeRecord(make_metadata("Judul", "a" * 60))],
    )
    # Stored as data (it is displayed to users), so it stays Indonesian.
    assert items[0]["date"] == "Tanggal tidak diketahui"


def test_harvest_stops_at_target(tmp_path):
    records = [FakeRecord(make_metadata(f"J{i}", "a" * 60)) for i in range(10)]
    items = ingest.harvest(target=3, output=tmp_path / "clean.json", records=records)
    assert len(items) == 3


def test_harvest_rejects_bad_target(tmp_path):
    with pytest.raises(ValueError, match="--target"):
        ingest.harvest(target=0, output=tmp_path / "clean.json", records=[])


def test_harvest_drops_empty_creator_entries(tmp_path):
    # An empty <dc:creator/> reaches us as None inside the list. The full-corpus
    # harvest contains such records, and joining them used to abort the index.
    items = ingest.harvest(
        target=10,
        output=tmp_path / "clean.json",
        records=[FakeRecord(make_metadata("Judul", "a" * 60, creators=[None, "A"]))],
    )
    assert items[0]["creators"] == ["A"]


# ---------- index ----------
class FakeEmbeddings(list):
    def tolist(self):
        return list(self)


class FakeModel:
    def encode(self, documents, batch_size=64, show_progress_bar=False):
        return FakeEmbeddings([[1.0, 0.0] for _ in documents])


class FakeCollection:
    metadata = {"hnsw:space": "cosine"}

    def __init__(self):
        self.batches = []

    def add(self, ids, embeddings, metadatas, documents):
        self.batches.append({"ids": ids, "metadatas": metadatas, "documents": documents})

    def count(self):
        return sum(len(batch["ids"]) for batch in self.batches)


class FakeClient:
    def __init__(self, missing=False):
        self.collection = FakeCollection()
        self.deleted = None
        self.created_metadata = None
        self.missing = missing

    def delete_collection(self, name):
        if self.missing:
            import chromadb

            raise chromadb.errors.NotFoundError("no such collection")
        self.deleted = name

    def get_or_create_collection(self, name, metadata):
        self.created_metadata = metadata
        return self.collection


def _write_json(tmp_path, n=3):
    data = [
        {
            "title": f"Judul {i}",
            "creators": ["A", "B"],
            "description": "abstrak",
            "url": f"http://x/{i}.pdf",
            "date": "2024",
        }
        for i in range(n)
    ]
    json_path = tmp_path / "clean.json"
    json_path.write_text(json.dumps(data), encoding="utf-8")
    return json_path


def test_build_index_cosine_batches_and_metadata(tmp_path):
    json_path = _write_json(tmp_path)

    client = FakeClient()
    total = ingest.build_index(
        json_path=json_path, batch_size=2, model=FakeModel(), client=client
    )

    assert total == 3
    assert client.deleted == ingest.SETTINGS.collection_name
    assert client.created_metadata == {
        "hnsw:space": "cosine",
        "embedding_model": ingest.SETTINGS.model_name,
    }
    assert [len(b["ids"]) for b in client.collection.batches] == [2, 1]
    assert client.collection.batches[0]["metadatas"][0]["creators"] == "A, B"
    assert client.collection.batches[0]["documents"][0] == "Judul 0. abstrak"


def test_build_index_tolerates_missing_collection(tmp_path):
    # First build: deleting a collection that does not exist is not an error.
    client = FakeClient(missing=True)
    assert ingest.build_index(
        json_path=_write_json(tmp_path), model=FakeModel(), client=client
    ) == 3


def test_build_index_survives_null_creator_in_json(tmp_path):
    # JSON harvested by an older build can still contain nulls; rebuilding from
    # it must not crash after the model has already been loaded.
    data = [
        {
            "title": "Judul",
            "creators": [None, "A"],
            "description": "abstrak",
            "url": "http://x/1.pdf",
            "date": "2024",
        }
    ]
    json_path = tmp_path / "clean.json"
    json_path.write_text(json.dumps(data), encoding="utf-8")

    client = FakeClient()
    ingest.build_index(json_path=json_path, model=FakeModel(), client=client)

    assert client.collection.batches[0]["metadatas"][0]["creators"] == "A"


def test_build_index_rejects_empty_json(tmp_path):
    json_path = tmp_path / "clean.json"
    json_path.write_text("[]", encoding="utf-8")
    with pytest.raises(ValueError, match="empty"):
        ingest.build_index(json_path=json_path, model=FakeModel(), client=FakeClient())


class CountingModel(FakeModel):
    def __init__(self):
        self.calls = []

    def encode(self, documents, batch_size=64, show_progress_bar=False):
        self.calls.append(list(documents))
        return FakeEmbeddings([[1.0, 0.0] for _ in documents])


def test_encode_in_chunks_makes_one_call_per_chunk(tmp_path):
    # A single silent `encode` over 48k abstracts is the hang-looking phase this
    # logging exists to expose; chunking must actually split the calls.
    data = [
        f"document {i}" for i in range(5)
    ]
    model = CountingModel()

    vectors = ingest._encode_in_chunks(model, data, chunk_size=2)

    assert len(vectors) == 5
    assert [len(call) for call in model.calls] == [2, 2, 1]


def test_harvest_accepts_a_custom_progress_every(tmp_path):
    records = [FakeRecord(make_metadata(f"J{i}", "a" * 60)) for i in range(5)]
    items = ingest.harvest(
        target=10,
        output=tmp_path / "clean.json",
        records=records,
        progress_every=2,
    )
    assert len(items) == 5


def test_ingest_lock_refuses_a_second_writer(tmp_path):
    pytest.importorskip("fcntl")
    lock = tmp_path / ".ingest.lock"

    with ingest.ingest_lock(lock):
        with pytest.raises(ingest.IngestLocked, match="already running"):
            with ingest.ingest_lock(lock):
                pass

    # Released: a later run can take the lock again.
    with ingest.ingest_lock(lock):
        pass


def test_run_summary_records_outcome(tmp_path, monkeypatch):
    import dataclasses

    monkeypatch.setattr(
        ingest,
        "SETTINGS",
        dataclasses.replace(
            ingest.SETTINGS, ingest_json_path=str(tmp_path / "d.json")
        ),
    )
    ingest._write_run_summary(
        status="failed",
        stage="index",
        started_at="2026-01-01T00:00:00+00:00",
        duration_s=12.5,
        counts={"records": 5, "documents": 3},
        error="TypeError: boom",
    )
    payload = json.loads((tmp_path / "last_ingest.json").read_text(encoding="utf-8"))
    assert payload["status"] == "failed"
    assert payload["records_harvested"] == 5
    assert payload["documents_indexed"] == 3
    assert payload["error"] == "TypeError: boom"


def test_main_records_a_failed_run_and_exits_non_zero(tmp_path, monkeypatch, caplog):
    # The incident this guards: a traceback scrolls past and a --rm container
    # takes the only copy with it, so nothing says the run failed.
    import dataclasses

    monkeypatch.setattr(
        ingest,
        "SETTINGS",
        dataclasses.replace(
            ingest.SETTINGS, ingest_json_path=str(tmp_path / "d.json")
        ),
    )

    def boom(**kwargs):
        raise TypeError("sequence item 1: expected str instance, NoneType found")

    monkeypatch.setattr(ingest, "harvest", boom)

    with caplog.at_level("ERROR"):
        code = ingest.main(["--harvest"])

    assert code == 1
    # The rendered line is `[time] [ERROR] [ingest]: INGEST FAILED | stage=...`.
    # `caplog.text` goes through pytest's own handler, so assert on the record's
    # data rather than the formatted string, which is the formatter's business.
    failed = [r for r in caplog.records if r.getMessage() == "INGEST FAILED"]
    assert failed, caplog.text
    assert failed[0].extra_data["stage"] == "harvest"
    assert failed[0].extra_data["error"].startswith("TypeError: sequence item 1")

    payload = json.loads((tmp_path / "last_ingest.json").read_text(encoding="utf-8"))
    assert payload["status"] == "failed"
    assert payload["stage"] == "harvest"
    assert payload["error"].startswith("TypeError: sequence item 1")


# ---------- query demo ----------
class FakeQueryCollection:
    def query(self, query_embeddings, n_results, include):
        metadatas = [{"title": f"Judul {i}"} for i in range(3)]
        return {"metadatas": [metadatas], "distances": [[0.1, 0.2, 0.7]]}


def test_query_demo_uses_similarity_and_dense_rank():
    results = ingest.query_demo(
        "x",
        min_similarity=0.53,
        model=FakeModel(),
        collection=FakeQueryCollection(),
    )

    assert [item["peringkat"] for item in results] == [1, 2]
    assert [item["skor"] for item in results] == [0.9, 0.8]


def test_query_demo_can_be_empty():
    results = ingest.query_demo(
        "x",
        min_similarity=0.95,
        model=FakeModel(),
        collection=FakeQueryCollection(),
    )
    assert results == []


def test_query_demo_matches_api_ranking():
    # The demo must report exactly what POST /search would return, so both go
    # through the same `app.rank_results` helper.
    from service.app import rank_results

    raw = FakeQueryCollection().query([], n_results=3, include=[])
    expected = [
        r.model_dump() for r in rank_results(raw["metadatas"][0], raw["distances"][0], 0.53)
    ]
    assert (
        ingest.query_demo(
            "x", min_similarity=0.53, model=FakeModel(), collection=FakeQueryCollection()
        )
        == expected
    )
