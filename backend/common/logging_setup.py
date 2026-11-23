"""Logging configuration shared by the API and the ingestion CLI.

`uvicorn` installs handlers for its own loggers, so this owns only the
application side (`aisen.*`). Doing it in one place keeps the two entry points
consistent and makes the output swappable:

- `LOG_FORMAT=text` (default) — bracketed, column-aligned lines for a terminal.
- `LOG_FORMAT=json` — one JSON object per line, for a collector.
- `LOG_FILE=logs/ingest.log` — also append to a file, so a `docker compose run
  --rm` container cannot take the only copy of the log with it.

Text output puts structure in brackets and data in `key=value` pairs:

    [07:28:55] [ingest] [INFO]  harvest done    valid=47945 took=15m59s

Brackets mark the fixed columns (time, logger, level) that every line has, so
the eye can skip them and land on the message. Data stays `key=value` because
that survives `grep failure stage=index` and any log collector; brackets there
would only add noise. Third-party libraries log through the same handler, and
their DEBUG chatter is indented and dimmed so it reads as background.

The handler is added once per process: `app.py` is imported by the tests, and
duplicate handlers would double every line. The level is applied on every call,
not only the first, so `configure_logging` always leaves the process at
`LOG_LEVEL`.
"""

from __future__ import annotations

import json
import logging
import sys
from pathlib import Path

from common.config import SETTINGS

DATE_FORMAT = "%Y-%m-%dT%H:%M:%S%z"

# `[HH:MM:SS] [logger] [LEVEL] message` — the three bracketed columns are what
# every line shares, so they are padded to a fixed width and the message always
# starts at the same column.
TIME_WIDTH = 8  # HH:MM:SS
LOGGER_WIDTH = 8  # aisen.ingest -> "ingest"; urllib3.connectionpool -> "urllib3"
LEVEL_WIDTH = len("CRITICAL")  # 8: the longest level, so every bracket is the same width

# Third-party DEBUG lines are background noise during an ingest: 87% of a real
# run. They are kept - dropping them would hide genuine transport failures - but
# indented and shortened so the application's own lines stand out.
NOISE_INDENT = "  "
NOISE_LOGGER_WIDTH = 16
NOISE_MESSAGE_WIDTH = 100

# Libraries whose DEBUG output is transport/library chatter rather than ours.
_NOISE_PREFIXES = (
    "urllib3",
    "httpcore",
    "httpx",
    "filelock",
    "huggingface_hub",
    "sentence_transformers",
    "transformers",
    "torch",
    "asyncio",
    "multipart",
)

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


def _service_name(logger_name: str) -> str:
    """`aisen.ingest` -> `ingest`; an unrelated logger keeps its head."""
    parts = logger_name.split(".")
    if parts[0] == "aisen" and len(parts) > 1:
        return parts[1]
    return parts[0]


def _is_noise(record: logging.LogRecord) -> bool:
    """True for library DEBUG chatter, which is rendered as background."""
    return record.levelno <= logging.DEBUG and record.name.startswith(_NOISE_PREFIXES)


class TextFormatter(logging.Formatter):
    """`[time] [service] [LEVEL] message key=value` for a terminal.

    Structure is bracketed because every line has it; data keeps `key=value` so
    it stays greppable. Exception tracebacks are NOT indented - a traceback read
    from a wrapped terminal is worse than a long line.
    """

    def format(self, record: logging.LogRecord) -> str:
        # Local time, seconds only: the date is in the file name and `+0000` adds
        # nine characters a human never reads.
        stamp = self.formatTime(record, "%H:%M:%S")

        if _is_noise(record):
            # Background: two-space indent, abbreviated logger, truncated body.
            message = record.getMessage().replace("\n", " ")
            if len(message) > NOISE_MESSAGE_WIDTH:
                message = message[: NOISE_MESSAGE_WIDTH - 1] + "…"
            head = "{indent}[{time}] [{logger:<{lw}}] {level:<{lvw}} ".format(
                indent=NOISE_INDENT,
                time=stamp,
                logger=_service_name(record.name)[:NOISE_LOGGER_WIDTH],
                lw=NOISE_LOGGER_WIDTH,
                level=record.levelname,
                lvw=LEVEL_WIDTH,
            )
            line = head + message
        else:
            head = "[{time}] [{service:<{sw}}] [{level:<{lw}}] ".format(
                time=stamp,
                service=_service_name(record.name)[:LOGGER_WIDTH],
                sw=LOGGER_WIDTH,
                level=record.levelname,
                lw=LEVEL_WIDTH,
            )
            parts = [record.getMessage()]
            for name in _STRUCTURED_FIELDS:
                value = getattr(record, name, None)
                if value is not None:
                    parts.append("{}={}".format(name, _format_value(value)))
            line = head + " ".join(parts)

        if record.exc_info:
            line = line + "\n" + self.formatException(record.exc_info)
        return line


def _format_value(value: object) -> str:
    """Render a structured field so it stays one grep-able token."""
    text = str(value)
    if not text or any(ch.isspace() or ch == '"' for ch in text):
        return json.dumps(text, ensure_ascii=False)
    return text


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
    return TextFormatter()


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

    # Outside the guard: a second call must still honour LOG_LEVEL, otherwise a
    # caller that configured once would silently keep whatever level was set
    # first - the reason a run with LOG_LEVEL=INFO could still emit DEBUG.
    root.setLevel(SETTINGS.log_level)

    return logging.getLogger(service)
