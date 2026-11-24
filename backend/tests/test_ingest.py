import json

import pytest

from ingestion import ingest


class FakeRecord:
    def __init__(self, metadata, identifier=None):
        self.metadata = metadata
        if identifier is not None:
            self.header = type("Header", (), {"identifier": identifier})()


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
def harvest_into(tmp_path, **kwargs):
    """Harvest with its resume state confined to `tmp_path`, as production does.

    Without this the journal lands in the real `data/temp/` and one test would
    resume from another test's pages.
    """
    kwargs.setdefault("output", tmp_path / "clean.json")
    kwargs["temp_dir"] = tmp_path / "temp"
    return ingest.harvest(**kwargs)


def test_harvest_skips_admin_short_and_broken_records(tmp_path):
    records = [
        object(),  # record without `.metadata` (deleted/broken)
        FakeRecord(make_metadata("Penilaian Sejawat", "x" * 100)),  # administrative
        FakeRecord(make_metadata("Tanpa Abstrak", "pendek")),  # <= 50 characters
        FakeRecord(make_metadata("Layak", "a" * 60, date="2024")),
    ]

    output = tmp_path / "clean.json"
    items = harvest_into(tmp_path, target=10, records=records)

    assert len(items) == 1
    assert items[0]["title"] == "Layak"
    assert items[0]["date"] == "2024"
    assert items[0]["creators"] == ["A", "B"]
    assert json.loads(output.read_text(encoding="utf-8")) == items


def test_harvest_fills_default_date(tmp_path):
    items = harvest_into(
        tmp_path, target=10, records=[FakeRecord(make_metadata("Judul", "a" * 60))]
    )
    # Stored as data (it is displayed to users), so it stays Indonesian.
    assert items[0]["date"] == "Tanggal tidak diketahui"


def test_harvest_stops_at_target(tmp_path):
    records = [FakeRecord(make_metadata(f"J{i}", "a" * 60)) for i in range(10)]
    assert len(harvest_into(tmp_path, target=3, records=records)) == 3


def test_harvest_rejects_bad_target(tmp_path):
    with pytest.raises(ValueError, match="--target"):
        harvest_into(tmp_path, target=0, records=[])


def test_harvest_drops_empty_creator_entries(tmp_path):
    # An empty <dc:creator/> reaches us as None inside the list. The full-corpus
    # harvest contains such records, and joining them used to abort the index.
    items = harvest_into(
        tmp_path,
        target=10,
        records=[FakeRecord(make_metadata("Judul", "a" * 60, creators=[None, "A"]))],
    )
    assert items[0]["creators"] == ["A"]


def test_harvest_journals_records_and_writes_output(tmp_path):
    records = [FakeRecord(make_metadata(f"J{i}", "a" * 60)) for i in range(3)]

    harvest_into(tmp_path, target=10, records=records)

    journal = tmp_path / "temp" / "harvest.jsonl"
    lines = [json.loads(l) for l in journal.read_text(encoding="utf-8").splitlines()]
    assert [entry["title"] for entry in lines] == ["J0", "J1", "J2"]
    # The identifier rides along so a replayed page can be deduplicated...
    assert all("_identifier" in entry for entry in lines)
    # ...but it must not leak into the document the index build consumes.
    output = json.loads((tmp_path / "clean.json").read_text(encoding="utf-8"))
    assert all("_identifier" not in item for item in output)


def test_harvest_resumes_from_the_journal_without_refetching(tmp_path):
    first = [
        FakeRecord(make_metadata(f"A{i}", "a" * 60), identifier=f"oai:{i}")
        for i in range(2)
    ]
    harvest_into(tmp_path, target=2, records=first)

    # Second run: the endpoint offers the same records again from the top.
    second = [
        FakeRecord(make_metadata("A0", "a" * 60), identifier="oai:0"),
        FakeRecord(make_metadata("A1", "a" * 60), identifier="oai:1"),
        FakeRecord(make_metadata("B0", "a" * 60), identifier="oai:2"),
    ]
    items = harvest_into(tmp_path, target=3, records=second)

    assert [item["title"] for item in items] == ["A0", "A1", "B0"]


def test_harvest_at_target_does_not_append_one_more(tmp_path):
    # Resuming with the target already met must be a no-op: a top-up request for
    # the same number used to fetch one extra record.
    records = [
        FakeRecord(make_metadata(f"A{i}", "a" * 60), identifier=f"oai:{i}")
        for i in range(2)
    ]
    harvest_into(tmp_path, target=2, records=records)

    again = harvest_into(
        tmp_path,
        target=2,
        records=[FakeRecord(make_metadata("X", "a" * 60), identifier="oai:x")],
    )

    assert [item["title"] for item in again] == ["A0", "A1"]


def test_harvest_records_without_an_identifier_are_still_kept(tmp_path):
    # Dedup depends on the header identifier; a stream without one must still
    # yield documents rather than silently dropping them.
    items = harvest_into(
        tmp_path, target=10, records=[FakeRecord(make_metadata("Tanpa ID", "a" * 60))]
    )
    assert [item["title"] for item in items] == ["Tanpa ID"]


def test_harvest_fresh_flag_discards_the_journal(tmp_path):
    harvest_into(
        tmp_path, target=1, records=[FakeRecord(make_metadata("Lama", "a" * 60))]
    )

    items = harvest_into(
        tmp_path,
        target=10,
        records=[FakeRecord(make_metadata("Baru", "a" * 60))],
        fresh=True,
    )

    assert [item["title"] for item in items] == ["Baru"]
    output = json.loads((tmp_path / "clean.json").read_text(encoding="utf-8"))
    assert [item["title"] for item in output] == ["Baru"]


def test_harvest_survives_a_torn_journal_line(tmp_path):
    journal = tmp_path / "temp" / "harvest.jsonl"
    journal.parent.mkdir(parents=True)
    good = {
        "title": "Utuh",
        "creators": [],
        "description": "a" * 60,
        "url": "u",
        "date": "2024",
        "_identifier": "x1",
    }
    journal.write_text(
        json.dumps(good) + '\n{"title": "terpo', encoding="utf-8"
    )

    items = harvest_into(
        tmp_path, target=10, records=[FakeRecord(make_metadata("Baru", "a" * 60))]
    )

    assert [item["title"] for item in items] == ["Utuh", "Baru"]


# ---------- harvest: OAI page boundaries ----------
class FakeRequest:
    def __init__(self, token):
        self.attrib = {"resumptionToken": token} if token else {}


class FakePageXML:
    """The parts of an OAI-PMH response the harvester reads."""

    def __init__(self, request_token, record_count):
        self._request_token = request_token
        self._record_count = record_count

    def find(self, tag):
        return FakeRequest(self._request_token) if tag.endswith("request") else None

    def findall(self, tag):
        return [object()] * self._record_count


class FakeOaiResponse:
    def __init__(self, xml):
        self.xml = xml


class FakeToken:
    def __init__(self, token):
        self.token = token


class FakePagedStream:
    """Mimics the two sickle behaviours the resume design depends on.

    Both were measured against the live endpoint:

    * the parsed response carries its own `<request resumptionToken="...">`
      naming the page being read -- this is the cursor a resume must send back;
    * `resumption_token` already names the NEXT page before the current page's
      first record is yielded, so storing it skips a page on resume.
    """

    def __init__(self, pages):
        self.pages = pages
        self.oai_response = None
        self.resumption_token = None
        self._records = self._iterate()

    def _set_page(self, number):
        page = self.pages[number]
        request_token = None if number == 0 else f"offset={number * 100}"
        self.oai_response = FakeOaiResponse(FakePageXML(request_token, len(page)))
        self.resumption_token = FakeToken(f"offset={(number + 1) * 100}")

    def _iterate(self):
        for number, page in enumerate(self.pages):
            self._set_page(number)
            for record in page:
                yield record

    def __iter__(self):
        return self

    def __next__(self):
        return next(self._records)


def paged(title_prefix, count):
    return [
        FakeRecord(
            make_metadata(f"{title_prefix}{i}", "a" * 60),
            identifier=f"oai:{title_prefix}{i}",
        )
        for i in range(count)
    ]


def test_harvest_commits_the_cursor_at_each_page_boundary(tmp_path):
    # The cursor advances per page, not only at the end: that is what makes an
    # interrupted run resume instead of re-fetching the whole corpus.
    stream = FakePagedStream([paged("A", 2), paged("B", 2), paged("C", 2)])

    seen = []
    original = ingest._atomic_write

    def spy(path, text):
        if path.name == "harvest_state.json":
            seen.append(json.loads(text))
        original(path, text)

    ingest._atomic_write = spy
    try:
        harvest_into(tmp_path, target=100, records=stream)
    finally:
        ingest._atomic_write = original

    # Page 0 was requested without a token, so there is nothing to resume from
    # until page 1 has been read; after that the cursor names that page. The
    # final page leaves an empty buffer, so no fourth write happens.
    assert [entry["next_token"] for entry in seen] == [
        None,
        "offset=100",
        "offset=200",
    ]
    assert [entry["valid"] for entry in seen] == [2, 4, 6]


def test_harvest_cursor_names_the_page_read_not_the_next_one(tmp_path):
    # The regression this design exists for: sickle's own token is one page
    # ahead, and storing it lost 12 records on a live resume.
    pages = [paged("A", 2), paged("B", 2)]
    harvest_into(tmp_path, target=3, records=FakePagedStream(pages))

    state = json.loads(
        (tmp_path / "temp" / "harvest_state.json").read_text(encoding="utf-8")
    )
    # Stopped mid-page-2 (2 from A + 1 from B), so the page being read is still
    # page 2 -- and sickle's own token says offset=200, i.e. page 3.
    assert state["next_token"] == "offset=100"
    assert state["valid"] == 3


def test_harvest_resume_sends_the_saved_cursor(tmp_path, monkeypatch):
    pages = [paged("A", 2), paged("B", 2)]
    harvest_into(tmp_path, target=3, records=FakePagedStream(pages))

    asked = {}

    class FakeSickle:
        def __init__(self, endpoint):
            pass

        def ListRecords(self, **params):
            asked.update(params)
            return iter([FakeRecord(make_metadata("Z", "a" * 60), identifier="oai:z")])

    import sickle

    monkeypatch.setattr(sickle, "Sickle", FakeSickle)
    items = harvest_into(tmp_path, target=5, records=None)

    assert asked["resumptionToken"] == "offset=100"
    assert asked["metadataPrefix"] == "oai_dc"
    # The resume continues from page 2; the fake endpoint stands in for it and
    # yields Z. Records of page 1 are not refetched.
    assert [item["title"] for item in items] == ["A0", "A1", "B0", "Z"]


def test_harvest_first_page_has_no_cursor_to_resume_from(tmp_path, monkeypatch):
    # A crash during the first page restarts from the top: the request carried
    # no token, so nothing else is possible.
    harvest_into(tmp_path, target=10, records=FakePagedStream([paged("A", 2)]))

    state = json.loads(
        (tmp_path / "temp" / "harvest_state.json").read_text(encoding="utf-8")
    )
    assert state["next_token"] is None

    asked = {}

    class FakeSickle:
        def __init__(self, endpoint):
            pass

        def ListRecords(self, **params):
            asked.update(params)
            return iter([FakeRecord(make_metadata("Z", "a" * 60), identifier="oai:z")])

    import sickle

    monkeypatch.setattr(sickle, "Sickle", FakeSickle)
    harvest_into(tmp_path, target=3, records=None)

    assert "resumptionToken" not in asked


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
    items = harvest_into(
        tmp_path,
        target=10,
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


def test_run_summary_appends_to_history(tmp_path, monkeypatch):
    import dataclasses

    data_dir = tmp_path / "data"
    monkeypatch.setattr(
        ingest,
        "SETTINGS",
        dataclasses.replace(
            ingest.SETTINGS, ingest_json_path=str(data_dir / "d.json")
        ),
    )

    ingest._write_run_summary(
        status="ok",
        stage="harvest",
        started_at="2026-01-01T00:00:00+00:00",
        duration_s=12.5,
        counts={"records": 5, "documents": 3},
        resume_from={"records": 200, "scanned": 800},
    )
    ingest._write_run_summary(
        status="ok",
        stage="index",
        started_at="2026-01-02T00:00:00+00:00",
        duration_s=3.0,
        counts={"records": 500},
    )

    # Append-only JSONL, one line per run, and the first entry survives intact.
    lines = (data_dir / "history.json").read_text(encoding="utf-8").splitlines()
    assert len(lines) == 2

    first, second = (json.loads(line) for line in lines)
    assert first["records_harvested"] == 5
    assert first["documents_indexed"] == 3
    assert first["resume_from"] == {"records": 200, "scanned": 800}
    # A run that started from scratch says so, rather than omitting the key.
    assert second["resume_from"] is None
    assert second["stage"] == "index"


def test_failed_run_does_not_touch_history(tmp_path, monkeypatch, caplog):
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

    # A failure must not be appended: /ready reads the last line as the state of
    # the data, and a failed run produced none.
    assert not (tmp_path / "history.json").exists()


def test_successful_run_records_what_it_resumed_from(tmp_path, monkeypatch):
    import dataclasses

    data_dir = tmp_path / "data"
    monkeypatch.setattr(
        ingest,
        "SETTINGS",
        dataclasses.replace(
            ingest.SETTINGS,
            ingest_json_path=str(data_dir / "d.json"),
            temp_dir=str(data_dir / "temp"),
        ),
    )
    journal = data_dir / "temp" / "harvest.jsonl"
    journal.parent.mkdir(parents=True)
    journal.write_text('{"title": "Lama"}\n{"title": "Lama 2"}\n', encoding="utf-8")
    (data_dir / "temp" / "harvest_state.json").write_text(
        json.dumps({"next_token": "offset%3D100", "scanned": 812, "valid": 2}),
        encoding="utf-8",
    )

    monkeypatch.setattr(
        ingest,
        "harvest",
        lambda **kwargs: [{"title": "Lama"}, {"title": "Lama 2"}, {"title": "Baru"}],
    )

    assert ingest.main(["--harvest"]) == 0

    entry = json.loads((data_dir / "history.json").read_text(encoding="utf-8"))
    assert entry["records_harvested"] == 3
    assert entry["resume_from"] == {"records": 2, "scanned": 812}


def test_fresh_run_records_no_resume_state(tmp_path, monkeypatch):
    import dataclasses

    data_dir = tmp_path / "data"
    monkeypatch.setattr(
        ingest,
        "SETTINGS",
        dataclasses.replace(
            ingest.SETTINGS,
            ingest_json_path=str(data_dir / "d.json"),
            temp_dir=str(data_dir / "temp"),
        ),
    )
    monkeypatch.setattr(ingest, "harvest", lambda **kwargs: [{"title": "A"}])

    assert ingest.main(["--harvest"]) == 0

    entry = json.loads((data_dir / "history.json").read_text(encoding="utf-8"))
    assert entry["resume_from"] is None


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
