"""Logging configuration shared by the API and the ingestion CLI.

`uvicorn` installs handlers for its own loggers, so this owns only the
application side (`aisen.*`). Doing it in one place keeps the two entry points
consistent and makes the output swappable:

- `LOG_FORMAT=text` (default) — the line format used so far, for a terminal.
- `LOG_FORMAT=json` — one JSON object per line, for a collector.
- `LOG_FILE=logs/ingest.log` — also append to a file, so a `docker compose run
  --rm` container cannot take the only copy of the log with it.

The handler is added once per process: `app.py` is imported by the tests, and
duplicate handlers would double every line.
"""

from __future__ import annotations

import json
import logging
import sys
from pathlib import Path

from settings import SETTINGS

TEXT_FORMAT = "%(asctime)s %(levelname)-8s %(name)s %(message)s"
DATE_FORMAT = "%Y-%m-%dT%H:%M:%S%z"

# Marker on the handlers this module owns, so a second call is a no-op.
_OWNED = "_aisen_handler"

# LogRecord attributes copied into the JSON payload when present.
_STRUCTURED_FIELDS = (
    "event",
    "request_id",
    "method",
    "path",
    "status",
    "duration_ms",
    "documents",
    "stage",
    "extra_data",
)


class JsonFormatter(logging.Formatter):
    """One JSON object per line: timestamp, level, logger, message, extras."""

    def format(self, record: logging.LogRecord) -> str:
        payload: dict[str, object] = {
            "ts": self.formatTime(record, DATE_FORMAT),
            "level": record.levelname,
            "logger": record.name,
            "message": record.getMessage(),
        }
        for name in _STRUCTURED_FIELDS:
            value = getattr(record, name, None)
            if value is not None:
                payload[name] = value
        if record.exc_info:
            payload["exception"] = self.formatException(record.exc_info)
        return json.dumps(payload, ensure_ascii=False)


def _build_formatter() -> logging.Formatter:
    if SETTINGS.log_format == "json":
        return JsonFormatter()
    return logging.Formatter(TEXT_FORMAT, DATE_FORMAT)


def configure_logging(service: str) -> logging.Logger:
    """Configure application logging once, then return `service`'s logger."""
    root = logging.getLogger()

    if not any(getattr(handler, _OWNED, False) for handler in root.handlers):
        formatter = _build_formatter()

        stream = logging.StreamHandler(sys.stderr)
        stream.setFormatter(formatter)
        setattr(stream, _OWNED, True)
        root.addHandler(stream)

        if SETTINGS.log_file:
            path = Path(SETTINGS.log_file)
            path.parent.mkdir(parents=True, exist_ok=True)
            file_handler = logging.FileHandler(path, encoding="utf-8")
            file_handler.setFormatter(formatter)
            setattr(file_handler, _OWNED, True)
            root.addHandler(file_handler)

        root.setLevel(SETTINGS.log_level)

    return logging.getLogger(service)
