"""HTTP surface of the AISEn backend.

`app.py` holds the FastAPI application (`service.app:app`) and `rank_results`,
the ranking helper the ingestion CLI reuses for `--query`. `watch.py` is the
`/ready` poller that runs as its own container. Neither module may import
`ingestion`.
"""
