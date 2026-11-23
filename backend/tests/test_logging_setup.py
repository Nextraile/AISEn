"""The terminal log shape, so a future edit cannot silently make it unreadable.

The frame rule: nothing inside a bracket but the value. `[INFO]`, never
`[ INFO ]`, never `[INFO    ]`. All width lives in the gap AFTER the closing
bracket, which is what aligns the columns. Asserting `body == body.strip()` is
what pins that - a `strip()`-tolerant check would let `" INFO "` back in, and
that is exactly the shape this replaced.
"""

from __future__ import annotations

import json
import logging
import re

import pytest

from common import logging_setup


def _render(record: logging.LogRecord) -> str:
    return logging_setup.TextFormatter().format(record)


def _record(name: str, level: int, msg: str, args=(), **extra) -> logging.LogRecord:
    record = logging.LogRecord(name, level, __file__, 1, msg, args, None)
    for key, value in extra.items():
        setattr(record, key, value)
    return record


def _bodies(line: str) -> list[str]:
    """Raw contents of each bracketed cell on the line, in order."""
    return re.findall(r"\[([^\]]*)\]", line)


def _message_column(line: str) -> int:
    """Offset where the message text starts, ignoring the noise indent."""
    match = re.search(r"\] +(?!.*\])", line)
    assert match, "no frame found in: %r".format(line)
    return match.end()


# ---- the bracket rule -------------------------------------------------------


@pytest.mark.parametrize("level", ["DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"])
def test_no_padding_inside_any_bracket(level):
    line = _render(_record("aisen.ingest", getattr(logging, level), "message"))
    bodies = _bodies(line)
    assert bodies, "no frame found in: %r".format(line)
    for body in bodies:
        # The value touches both walls: no leading or trailing space at all.
        assert body == body.strip(), "padding leaked inside the bracket: {!r}".format(body)
        assert "  " not in body, "run of spaces inside the bracket: {!r}".format(body)


def test_cell_is_the_value_between_tight_brackets():
    for value in ("15:25:33", "INFO", "CRITICAL", "ingest", "httpcore"):
        cell = logging_setup._cell(value)
        assert cell == "[{}]".format(value)
        assert len(cell) == len(value) + 2, "just the brackets, no inner space"


def test_gap_is_outside_the_bracket_not_inside():
    info = _render(_record("aisen.ingest", logging.INFO, "BODY"))
    crit = _render(_record("aisen.ingest", logging.CRITICAL, "BODY"))
    # The inside of a short cell is still just `INFO`.
    assert "[INFO]" in info
    # The width difference shows up AFTER the closing bracket of the level cell.
    level_cell = re.search(r"\[(?:DEBUG|INFO|WARNING|ERROR|CRITICAL)\]( +)", info)
    assert level_cell, info
    assert len(level_cell.group(1)) > 1, "gap must be outside, not inside"
    # ...and CRITICAL, the widest, needs the minimum gap of one space.
    crit_cell = re.search(r"\[CRITICAL\]( +)", crit)
    assert crit_cell and len(crit_cell.group(1)) == 1


def test_message_starts_at_the_same_column_for_every_level():
    columns = {
        level: _message_column(
            _render(_record("aisen.ingest", getattr(logging, level), "BODY"))
        )
        for level in ("DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL")
    }
    assert len(set(columns.values())) == 1, columns


def test_indented_noise_shares_the_app_columns():
    app = _render(_record("aisen.ingest", logging.INFO, "BODY"))
    noise = _render(_record("urllib3.connectionpool", logging.DEBUG, "BODY"))
    # Noise is indented, so its message sits exactly the indent further right.
    assert noise.startswith(logging_setup.NOISE_INDENT)
    assert (
        _message_column(noise)
        == _message_column(app) + len(logging_setup.NOISE_INDENT)
    )


def test_logger_column_is_wide_enough_for_every_known_logger():
    for name in logging_setup.LOGGER_VALUES:
        line = _render(_record("aisen." + name, logging.INFO, "BODY"))
        # No truncation marker means the frame did not have to cut it.
        assert "…" not in line
        assert _bodies(line)[2] == name


# ---- zones ------------------------------------------------------------------


def test_data_pairs_are_bare_key_value():
    line = _render(
        _record("aisen.ingest", logging.INFO, "harvesting", documents=250, stage="index")
    )
    assert "documents=250" in line
    assert "stage=index" in line
    # Quoted only when needed - neither of these is.
    assert 'documents="250"' not in line


def test_value_punctuation_is_passed_through_byte_for_byte():
    # `path` and `stage` are real structured fields; the values carry the
    # punctuation under test.
    cases = [
        ("path", "data/documents.json"),
        ("path", "./data/vector_db"),
        ("stage", "harvest"),
    ]
    for key, value in cases:
        line = _render(_record("aisen.ingest", logging.INFO, "x", **{key: value}))
        rendered = line.split("{}=".format(key), 1)[1].rstrip()
        recovered = json.loads(rendered) if rendered.startswith('"') else rendered
        assert recovered == value, (key, rendered)


def test_pair_keeps_punctuation_byte_for_byte():
    # Exercised through `_pair` directly for values that are not structured
    # fields, which is where the interesting punctuation lives.
    for key, value in [
        ("metadata", "{'hnsw:space': 'cosine'}"),
        ("endpoint", "https://eprints2.undip.ac.id/cgi/oai2"),
        ("ratio", "25000/47915"),
        ("pct", "52%"),
        ("took", "15m59s"),
        ("error", "TypeError: NoneType object has no attribute get"),
    ]:
        rendered = logging_setup._pair(key, value)
        assert rendered.startswith(key + "=")
        tail = rendered.split("=", 1)[1]
        recovered = json.loads(tail) if tail.startswith('"') else tail
        assert recovered == value, (key, rendered)


def test_value_with_spaces_or_newlines_is_quoted_and_recoverable():
    rendered = logging_setup._pair("error", "ValueError: bad\n- line two")
    assert "\n" not in rendered, "a newline in a value must not break the one-line rule"
    assert json.loads(rendered.split("=", 1)[1]) == "ValueError: bad\n- line two"


def test_value_with_parens_is_not_respaced():
    rendered = logging_setup._pair("text", "Reading (partial) results")
    assert "( partial )" not in rendered
    assert json.loads(rendered.split("=", 1)[1]) == "Reading (partial) results"


# ---- noise handling ---------------------------------------------------------


def test_third_party_debug_is_indented_and_bounded():
    line = _render(
        _record("urllib3.connectionpool", logging.DEBUG, "GET " + "x" * 400)
    )
    assert line.startswith(logging_setup.NOISE_INDENT + "[")
    assert "\n" not in line
    assert len(line) < 200


def test_application_debug_is_not_treated_as_noise():
    line = _render(_record("aisen.ingest", logging.DEBUG, "our own debug"))
    assert not line.startswith(" ")
    assert line.endswith("our own debug")


def test_traceback_is_not_indented():
    import sys

    try:
        raise ValueError("boom")
    except ValueError:
        record = logging.LogRecord(
            "aisen.search", logging.ERROR, __file__, 1, "unhandled error", (), sys.exc_info()
        )
    line = _render(record)
    head, _, trace = line.partition("\n")
    assert head.startswith("[")
    assert "Traceback" in trace
    assert not trace.startswith(" ")


def test_json_format_still_one_object_per_line():
    record = _record("aisen.search", logging.INFO, "x", request_id="abc")
    payload = json.loads(logging_setup.JsonFormatter().format(record))
    assert payload["level"] == "INFO"
    assert payload["logger"] == "aisen.search"
    assert payload["request_id"] == "abc"
