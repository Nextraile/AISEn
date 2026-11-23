"""Logging configuration shared by the API and the ingestion CLI.

`uvicorn` installs handlers for its own loggers, so this owns only the
application side (`aisen.*`). Doing it in one place keeps the two entry points
consistent and makes the output swappable:

- `LOG_FORMAT=text` (default) — bracketed, column-aligned lines for a terminal.
- `LOG_FORMAT=json` — one JSON object per line, for a collector.
- `LOG_FILE=logs/ingest.log` — also append to a file, so a `docker compose run
  --rm` container cannot take the only copy of the log with it.

Text output is a tight frame, then a phrase, then the data:

    [15:25:33.123] [INFO] [ingest]: harvest complete | valid=47945, scanned=55575

One space between cells and nothing inside a bracket: `[INFO]`, never
`[ INFO ]`. There is no column padding, because the `:` after the last bracket is
a stronger anchor than a lined-up column and it costs nothing:
`[hello] [world]`. The consequence is that the message column shifts by a few
characters as the level name changes width — that is accepted, not a bug.

Punctuation is classified by where it sits, not by the character:

- frame (`[ ]`) - the three cells every line has; the formatter owns it
- dividers (`]: `, ` | `, `, `) - line-level structure, always the separator and
  never a value: `]: ` starts the phrase, ` | ` starts the data, `, ` splits pairs
- data (`=`) - `key=value`, bare
- value - passed through byte-for-byte; a `:` in `TypeError: bad`, a `{}` in a
  metadata repr and a `%` in `pct=52%` are content, not structure. Only
  whitespace, a quote or a newline forces `json.dumps` quoting, which is also
  what keeps an unquoted ` | ` unambiguous - a value containing one has
  whitespace, so it is quoted.

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

# Every line opens with three framed cells, then `: `, then the phrase:
#
#     [15:25:33.123] [INFO] [ingest]: harvest complete | valid=47945, scanned=55575
#     [15:25:33.123] [CRITICAL] [ingest]: boom
#
# Nothing sits inside a bracket but the value - no inner space at all, so the
# value touches both `[` and `]`. There is no column padding either: the `:` after
# the last bracket is the anchor the eye lands on, which reads better than a
# lined-up column and costs nothing. The trade is that the phrase starts a few
# characters later on a `[CRITICAL]` line than on an `[INFO]` one. That is
# accepted; padding it back out is what made the old shape ugly.
def _cell(value: str) -> str:
    """Frame a value tightly: nothing between the brackets but the value."""
    return "[{}]".format(value)


# Longest logger name the frame will print. Names longer than this are cut, so a
# surprise third-party logger cannot widen the frame unboundedly. The values are
# the loggers this project and its main dependencies actually use.
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
LOGGER_CONTENT_WIDTH = max(len(value) for value in LOGGER_VALUES)

# The stamp is `HH:MM:SS.mmm` - 12 characters. `strftime` has no `%f`, so the
# milliseconds come from the record; this constant is only the width, used by the
# tests and by `watch.py`, which builds the frame by hand.
TIMESTAMP_WIDTH = len("15:25:33.123")

# Third-party DEBUG lines are background noise during an ingest: 87% of a real
# run. They are kept - dropping them would hide genuine transport failures - but
# indented and shortened so the application's own lines stand out. They use the
# same frame, so their cells line up with ours.
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
    """`[time] [LEVEL] [logger]: phrase | key=value, key=value` for a terminal.

    Nothing is padded inside a bracket and no column is padded either; the `:` is
    the anchor. Data stays `key=value` so it is greppable, and a value is quoted
    only when it would otherwise break the line. Exception tracebacks are NOT
    indented - a traceback read from a wrapped terminal is worse than a long line.
    """

    def format(self, record: logging.LogRecord) -> str:
        # Local time to the millisecond: the date is in the file name and `+0000`
        # adds nine characters a human never reads. `strftime` has no `%f`, so the
        # milliseconds are appended from `record.msecs` - which is what makes the
        # stamp a fixed 12 characters.
        stamp = "{}.{:03d}".format(
            self.formatTime(record, "%H:%M:%S"), int(record.msecs)
        )
        service = _service_name(record.name)[:LOGGER_CONTENT_WIDTH]

        head = "{} {} {}: ".format(
            _cell(stamp),
            _cell(record.levelname),
            _cell(service),
        )

        if _is_noise(record):
            # Background: two-space indent, same frame, truncated body, and no
            # `key=value` decoration - library messages are prose, not data.
            message = record.getMessage().replace("\n", " ")
            if len(message) > NOISE_MESSAGE_WIDTH:
                message = message[: NOISE_MESSAGE_WIDTH - 1] + "…"
            line = NOISE_INDENT + head + message
        else:
            line = head + _phrase(record) + _data(record)

        if record.exc_info:
            line = line + "\n" + self.formatException(record.exc_info)
        return line


# Keys `_data` renders, in the order the call site lists them. Anything else on
# the record (including `extra_data`, which carries the pairs for the text form)
# stays out of the visible line.
_TEXT_KEYS = (
    "request_id",
    "method",
    "path",
    "status",
    "duration_ms",
    "documents",
    "stage",
)


def _phrase(record: logging.LogRecord) -> str:
    """The human sentence: what happened, with no data glued into it."""
    return record.getMessage()


def _data(record: logging.LogRecord) -> str:
    """` | k=v, k=v` built from the call site's `extra_data` dict.

    `extra_data` is one dict rather than a pile of keyword arguments because the
    phrase must not contain the data any more: the formatter joins the pairs, so
    a call site cannot forget the comma or drift out of the agreed shape.
    """
    pairs = list(getattr(record, "extra_data", None) or {})
    # Legacy structured attributes, kept so an older call site still renders.
    pairs.extend(name for name in _TEXT_KEYS if getattr(record, name, None) is not None)
    if not pairs:
        return ""
    return " | " + ", ".join(_pair(name, _field_value(record, name)) for name in pairs)


def _field_value(record: logging.LogRecord, name: str) -> object:
    """Read one data field from `extra_data`, falling back to the record."""
    extra = getattr(record, "extra_data", None) or {}
    if name in extra:
        return extra[name]
    return getattr(record, name)


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
    """One JSON object per line: timestamp, level, logger, message, extras.

    `extra_data` is spread into the top level rather than nested under its own
    key, so `valid=250` in the text form and `{"valid": 250}` here are the same
    field at the same depth - a collector reads one shape whichever formatter is
    active.
    """

    def format(self, record: logging.LogRecord) -> str:
        payload: dict[str, object] = {
            "ts": self.formatTime(record, DATE_FORMAT),
            "level": record.levelname,
            "logger": record.name,
            "message": record.getMessage(),
        }
        payload.update(getattr(record, "extra_data", None) or {})
        for name in _STRUCTURED_FIELDS:
            if name == "extra_data":
                continue
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
