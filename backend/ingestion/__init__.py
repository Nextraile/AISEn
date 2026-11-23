"""Data ingestion for the AISEn backend, run as `python -m ingestion.ingest`.

`ingest.py` is the only path that writes `data/documents.json` and the Chroma
collection: OAI-PMH harvest, then embed and store. It is left out of the serving
image and shipped only through the `tools` Compose profile.
"""
