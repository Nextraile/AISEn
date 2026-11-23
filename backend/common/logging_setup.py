"""Logging configuration shared by the API and the ingestion CLI.

`uvicorn` installs handlers for its own loggers, so this owns only the
application side (`aisen.*`). Doing it in one place keeps the two entry points
consistent and makes the output swappable:

- `LOG_FORMAT=text` (default) — bracketed, column-aligned lines for a terminal.
- `LOG_FORMAT=json` — one JSON object per line, for a collector.
- `LOG_FILE=logs/ingest.log` — also append to a file, so a `docker compose run
  --rm` container cannot take the only copy of the log with it.

Text output puts the frame in brackets and the data in `key=value` pairs:

    [15:25:33] [INFO]     [ingest]   harvest complete  valid=47945

Nothing sits inside a bracket but the value: `[INFO]`, never `[ INFO ]` and
never `[INFO    ]`. All width lives in the gaps *between* the cells, so a short
cell is followed by more spaces than a long one and the message still lands on
the same column:

    [hello]        [world]
    [this is what] [i mean]

Punctuation is classified by where it sits, not by the character:

- frame (`[ ]`) - the three cells every line has; the formatter owns and pads it
- data (`=`) - `key=value`, bare, never padded
- value - passed through byte-for-byte; a `:` in `TypeError: bad`, a `{}` in a
  metadata repr and a `%` in `pct=52%` are content, not structure. Only
  whitespace, a quote or a newline forces `json.dumps` quoting, which keeps the
  line re-parseable and a multi-line error message on one line.

Third-party libraries log through the same handler, and their DEBUG chatter is
indented so it reads as background.

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

# Every line opens with three framed cells: time, level, logger.
#
#     [15:25:33] [INFO]     [ingest]   harvesting   valid=250
#     [15:25:33] [CRITICAL] [ingest]   boom
#
# Nothing sits inside a bracket but the value - no inner space at all, so the
# value touches both `[` and `]`. Every space lives in the gap AFTER the closing
# bracket, which is what lines the columns up: `[INFO]` is followed by more
# spaces than `[CRITICAL]`, and the message still starts on the same column.
def _cell(value: str) -> str:
    """Frame a value tightly: nothing between the brackets but the value."""
    return "[{}]".format(value)


# Column widths are derived from a CLOSED set of known values, not from
# whatever a line happens to contain. If a width came from the line itself,
# `[INFO]` and `[CRITICAL]` would make their own lines different widths and the
# message column would wander - the asymmetry this shape exists to remove.
# A value longer than the set is truncated before framing, so it cannot push the
# column either.
LEVEL_VALUES = ("DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL")
LOGGER_VALUES = (
    "ingest",
    "search",
    "watch",
    "startup",
    "urllib3",
    "httpcore",
    "httpx",
    "filelock",
)
TIME_CONTENT_WIDTH = len("00:00:00")
LEVEL_CONTENT_WIDTH = max(len(value) for value in LEVEL_VALUES)
LOGGER_CONTENT_WIDTH = max(len(value) for value in LOGGER_VALUES)

TIME_COLUMN = len(_cell("0" * 8))
LEVEL_COLUMN = len(_cell("X" * LEVEL_CONTENT_WIDTH))
LOGGER_COLUMN = len(_cell("X" * LOGGER_CONTENT_WIDTH))

# Third-party DEBUG lines are background noise during an ingest: 87% of a real
# run. They are kept - dropping them would hide genuine transport failures - but
# indented and shortened so the application's own lines stand out. They share
# the same three column widths, so their frames line up with ours.
NOISE_INDENT = "  "
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
    """`[time] [LEVEL] [logger] message key=value` for a terminal.

    Nothing is padded inside a bracket; the gap after each closing bracket is
    what aligns the columns. Data stays `key=value` so it is greppable, and a
    value is quoted only when it would otherwise break the line. Exception
    tracebacks are NOT indented - a traceback read from a wrapped terminal is
    worse than a long line.
    """

    def format(self, record: logging.LogRecord) -> str:
        # Local time, seconds only: the date is in the file name and `+0000` adds
        # nine characters a human never reads.
        stamp = self.formatTime(record, "%H:%M:%S")
        service = _service_name(record.name)[:LOGGER_CONTENT_WIDTH]

        head = "{} {} {} ".format(
            _cell(stamp).ljust(TIME_COLUMN),
            _cell(record.levelname).ljust(LEVEL_COLUMN),
            _cell(service).ljust(LOGGER_COLUMN),
        )

        if _is_noise(record):
            # Background: two-space indent, same frame, truncated body, and no
            # `key=value` decoration - library messages are prose, not data.
            message = record.getMessage().replace("\n", " ")
            if len(message) > NOISE_MESSAGE_WIDTH:
                message = message[: NOISE_MESSAGE_WIDTH - 1] + "…"
            line = NOISE_INDENT + head + message
        else:
            parts = [record.getMessage()]
            for name in _STRUCTURED_FIELDS:
                value = getattr(record, name, None)
                if value is not None:
                    parts.append(_pair(name, value))
            line = head + " ".join(parts)

        if record.exc_info:
            line = line + "\n" + self.formatException(record.exc_info)
        return line


def _pair(key: str, value: object) -> str:
    """Render one data field. The value is passed through byte-for-byte.

    Punctuation inside a value is content, not structure: a `:` in
    `TypeError: bad`, a `{}` in a metadata repr, a `%` in `pct=52%` all mean
    something to the reader and nothing to the formatter. Only characters that
    would break the line - whitespace, a quote, a newline - force quoting, which
    keeps a multi-line `validate()` message on one line and safely delimited.
    """
    text = str(value)
    if not text or any(ch.isspace() or ch == '"' for ch in text):
        return "{}={}".format(key, json.dumps(text, ensure_ascii=False))
    return "{}={}".format(key, text)


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
