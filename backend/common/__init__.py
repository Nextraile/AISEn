"""Shared configuration and logging for the AISEn backend.

`config.py` owns every setting and reads them once at import; `logging_setup.py`
configures text/JSON/file handlers. Both are imported by the service and the
ingestion CLI, so anything here must stay dependency-light.
"""
