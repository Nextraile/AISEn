import json

import ingest


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
def test_harvest_menyaring_admin_abstrak_pendek_dan_record_rusak(tmp_path):
    records = [
        object(),  # record tanpa `.metadata` (deleted/broken)
        FakeRecord(make_metadata("Penilaian Sejawat", "x" * 100)),  # administratif
        FakeRecord(make_metadata("Tanpa Abstrak", "pendek")),  # <= 50 karakter
        FakeRecord(make_metadata("Layak", "a" * 60, date="2024")),
    ]

    output = tmp_path / "clean.json"
    items = ingest.harvest(target=10, output=output, records=records)

    assert len(items) == 1
    assert items[0]["title"] == "Layak"
    assert items[0]["date"] == "2024"
    assert items[0]["creators"] == ["A", "B"]
    assert json.loads(output.read_text(encoding="utf-8")) == items


def test_harvest_mengisi_tanggal_default(tmp_path):
    items = ingest.harvest(
        target=10,
        output=tmp_path / "clean.json",
        records=[FakeRecord(make_metadata("Judul", "a" * 60))],
    )
    assert items[0]["date"] == "Tanggal tidak diketahui"


def test_harvest_berhenti_di_target(tmp_path):
    records = [FakeRecord(make_metadata(f"J{i}", "a" * 60)) for i in range(10)]
    items = ingest.harvest(target=3, output=tmp_path / "clean.json", records=records)
    assert len(items) == 3


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
    def __init__(self):
        self.collection = FakeCollection()
        self.deleted = None
        self.created_metadata = None

    def delete_collection(self, name):
        self.deleted = name

    def get_or_create_collection(self, name, metadata):
        self.created_metadata = metadata
        return self.collection


def test_build_index_cosine_batch_dan_metadata(tmp_path):
    data = [
        {
            "title": f"Judul {i}",
            "creators": ["A", "B"],
            "description": "abstrak",
            "url": f"http://x/{i}.pdf",
            "date": "2024",
        }
        for i in range(3)
    ]
    json_path = tmp_path / "clean.json"
    json_path.write_text(json.dumps(data), encoding="utf-8")

    client = FakeClient()
    total = ingest.build_index(
        json_path=json_path, batch_size=2, model=FakeModel(), client=client
    )

    assert total == 3
    assert client.deleted == ingest.COLLECTION_NAME
    assert client.created_metadata == {
        "hnsw:space": "cosine",
        "embedding_model": ingest.MODEL_NAME,
    }
    assert [len(b["ids"]) for b in client.collection.batches] == [2, 1]
    assert client.collection.batches[0]["metadatas"][0]["creators"] == "A, B"
    assert client.collection.batches[0]["documents"][0] == "Judul 0. abstrak"


def test_build_index_menolak_json_kosong(tmp_path):
    json_path = tmp_path / "clean.json"
    json_path.write_text("[]", encoding="utf-8")
    try:
        ingest.build_index(json_path=json_path, model=FakeModel(), client=FakeClient())
    except ValueError as exc:
        assert "kosong" in str(exc)
    else:  # pragma: no cover
        raise AssertionError("JSON kosong seharusnya ditolak")


# ---------- query demo ----------
class FakeQueryCollection:
    def query(self, query_embeddings, n_results, include):
        metadatas = [{"title": f"Judul {i}"} for i in range(3)]
        return {"metadatas": [metadatas], "distances": [[0.1, 0.2, 0.7]]}


def test_query_demo_memakai_kemiripan_dan_peringkat_rapat():
    results = ingest.query_demo(
        "x",
        min_similarity=0.53,
        model=FakeModel(),
        collection=FakeQueryCollection(),
    )

    assert [item["peringkat"] for item in results] == [1, 2]
    assert [item["skor"] for item in results] == [0.9, 0.8]


def test_query_demo_bisa_kosong():
    results = ingest.query_demo(
        "x",
        min_similarity=0.95,
        model=FakeModel(),
        collection=FakeQueryCollection(),
    )
    assert results == []
