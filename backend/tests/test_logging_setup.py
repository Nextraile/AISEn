"""The terminal log shape, so a future edit cannot silently make it unreadable."""

from __future__ import annotations

import logging

from common import logging_setup


def _render(record: logging.LogRecord) -> str:
    return logging_setup.TextFormatter().format(record)


def _record(name: str, level: int, msg: str, args=(), **extra) -> logging.LogRecord:
    record = logging.LogRecord(name, level, __file__, 1, msg, args, None)
    for key, value in extra.items():
        setattr(record, key, value)
    return record


def test_application_line_has_bracketed_columns_then_message():
    line = _render(
        _record(
            "aisen.ingest",
            logging.INFO,
            "harvest complete   valid=%d scanned=%d",
            (47945, 55575),
        )
    )
    # [time] [logger] [LEVEL] message - the leading phrase stays readable.
    assert line.startswith("[")
    assert "] [ingest  ] [" in line
    assert line.endswith("harvest complete   valid=47945 scanned=55575")
    # Seconds only: no date and no offset in a terminal line.
    assert line.count(":") >= 2 and "T" not in line.split("]")[0]


def test_columns_align_across_levels():
    info = _render(_record("aisen.ingest", logging.INFO, "BODY"))
    warn = _render(_record("aisen.ingest", logging.WARNING, "BODY"))
    crit = _render(_record("aisen.ingest", logging.CRITICAL, "BODY"))
    # The message must start at the same column whatever the level length.
    assert info.index("BODY") == warn.index("BODY") == crit.index("BODY")
    # ...and the level still fits its bracket without overflow.
    assert "[WARNING ]" in warn and "[INFO    ]" in info and "[CRITICAL]" in crit


def test_third_party_debug_is_indented_and_shortened():
    line = _render(
        _record(
            "urllib3.connectionpool",
            logging.DEBUG,
            "GET " + "x" * 400,
        )
    )
    assert line.startswith(logging_setup.NOISE_INDENT + "[")
    assert "urllib3" in line
    assert "\n" not in line
    assert len(line) < 200


def test_application_debug_is_not_treated_as_noise():
    line = _render(_record("aisen.ingest", logging.DEBUG, "our own debug"))
    assert not line.startswith(" ")
    assert line.endswith("our own debug")


def test_structured_fields_render_as_single_grep_able_tokens():
    line = _render(
        _record(
            "aisen.search",
            logging.INFO,
            "request",
            request_id="abc123",
            status=200,
            duration_ms=12.5,
        )
    )
    assert "request_id=abc123" in line
    assert "status=200" in line
    assert "duration_ms=12.5" in line


def test_structured_field_with_spaces_is_quoted():
    line = _render(_record("aisen.search", logging.INFO, "x", stage="a b"))
    assert 'stage="a b"' in line


def test_traceback_is_not_indented():
    try:
        raise ValueError("boom")
    except ValueError:
        import sys

        record = logging.LogRecord(
            "aisen.search",
            logging.ERROR,
            __file__,
            1,
            "unhandled error",
            (),
            sys.exc_info(),
        )
    line = _render(record)
    head, _, trace = line.partition("\n")
    assert head.startswith("[")
    assert "Traceback" in trace
    assert not trace.startswith(" ")


def test_json_format_still_one_object_per_line():
    import json

    record = _record("aisen.search", logging.INFO, "x", request_id="abc")
    payload = json.loads(logging_setup.JsonFormatter().format(record))
    assert payload["level"] == "INFO"
    assert payload["logger"] == "aisen.search"
    assert payload["request_id"] == "abc"
