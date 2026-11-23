"""The terminal log shape, so a future edit cannot silently make it unreadable.

The frame rule: nothing inside a bracket but the value, one space between cells,
and `: ` after the last bracket. There is no column padding - the `:` is the
anchor - so the phrase starts a few characters later on a `[CRITICAL]` line than
on an `[INFO]` one. That is deliberate and these tests pin the parts that are
not: one divider per line, ` | ` present only with data, `, ` between pairs, and
values surviving byte-for-byte.
"""

from __future__ import annotations

import json
import logging
import re

import pytest

from common import logging_setup


def _render(record: logging.LogRecord) -> str:
    return logging_setup.TextFormatter().format(record)


def _record(name: str, level: int, msg: str, args=(), data=None, **extra) -> logging.LogRecord:
    record = logging.LogRecord(name, level, __file__, 1, msg, args, None)
    if data is not None:
        record.extra_data = data
    for key, value in extra.items():
        setattr(record, key, value)
    return record


def _bodies(line: str) -> list[str]:
    """Raw contents of each bracketed cell on the line, in order."""
    return re.findall(r"\[([^\]]*)\]", line)


# ---- the frame --------------------------------------------------------------


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
    for value in ("15:25:33.123", "INFO", "CRITICAL", "ingest", "httpcore"):
        cell = logging_setup._cell(value)
        assert cell == "[{}]".format(value)
        assert len(cell) == len(value) + 2, "just the brackets, no inner space"


def test_timestamp_has_milliseconds():
    line = _render(_record("aisen.ingest", logging.INFO, "BODY"))
    stamp = _bodies(line)[0]
    assert re.fullmatch(r"\d\d:\d\d:\d\d\.\d{3}", stamp), stamp
    assert len(stamp) == logging_setup.TIMESTAMP_WIDTH


def test_cells_are_separated_by_exactly_one_space():
    line = _render(_record("aisen.ingest", logging.INFO, "BODY"))
    head = line.split("]", 1)[0] + "]"
    assert re.match(r"^\[\d\d:\d\d:\d\d\.\d{3}\] \[INFO\] \[ingest\]: ", line), line


def test_every_line_has_exactly_one_divider():
    for level in ("DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"):
        line = _render(_record("aisen.ingest", getattr(logging, level), "BODY"))
        assert line.count("]: ") == 1, line
        assert line.split("]: ", 1)[1] == "BODY", line


# ---- the data block ---------------------------------------------------------


def test_data_is_pipe_divided_comma_separated_pairs():
    line = _render(
        _record(
            "aisen.ingest",
            logging.INFO,
            "harvest complete",
            data={"valid": 47945, "scanned": 55575},
        )
    )
    phrase, _, data = line.partition(": ")[2].partition(" | ")
    assert phrase == "harvest complete"
    assert data == "valid=47945, scanned=55575"


def test_no_data_means_no_pipe():
    line = _render(_record("aisen.ingest", logging.CRITICAL, "boom"))
    assert "|" not in line
    assert line.endswith(": boom")


def test_pairs_keep_the_order_the_call_site_chose():
    line = _render(
        _record(
            "aisen.ingest",
            logging.INFO,
            "embedding",
            data={
                "done": "1400/47954",
                "pct": "3%",
                "rate": "24.9/s",
                "eta": "31m11s",
            },
        )
    )
    tail = line.split(" | ", 1)[1]
    assert tail == "done=1400/47954, pct=3%, rate=24.9/s, eta=31m11s"


def test_empty_extra_data_renders_no_pipe():
    line = _render(_record("aisen.ingest", logging.INFO, "quiet", data={}))
    assert "|" not in line


# ---- zones ------------------------------------------------------------------


def test_value_punctuation_is_passed_through_byte_for_byte():
    cases = [
        ("path", "data/documents.json"),
        ("path", "./data/vector_db"),
        ("stage", "harvest"),
    ]
    for key, value in cases:
        rendered = logging_setup._pair(key, value)
        tail = rendered.split("=", 1)[1]
        recovered = json.loads(tail) if tail.startswith('"') else tail
        assert recovered == value, (key, rendered)


def test_pair_keeps_punctuation_byte_for_byte():
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


def test_pipe_inside_a_value_cannot_be_mistaken_for_the_separator():
    # Any value with a literal ` | ` has whitespace, so `_pair` quotes it and the
    # pipe ends up inside the quotes. That is what keeps the divider unambiguous.
    rendered = logging_setup._pair("k", "a | b")
    assert rendered == 'k="a | b"'
    line = _render(_record("aisen.ingest", logging.INFO, "phrase", data={"k": "a | b"}))
    assert line.endswith(': phrase | k="a | b"')
    # Splitting on the FIRST ` | ` gives the real divider: the phrase is never
    # ambiguous, and everything after it re-parses because the value is quoted.
    phrase, _, data = line.partition(": ")[2].partition(" | ")
    assert phrase == "phrase"
    assert data == 'k="a | b"'
    assert json.loads(data.split("=", 1)[1]) == "a | b"


# ---- noise handling ---------------------------------------------------------


def test_third_party_debug_is_indented_and_bounded():
    line = _render(
        _record("urllib3.connectionpool", logging.DEBUG, "GET " + "x" * 400)
    )
    assert line.startswith(logging_setup.NOISE_INDENT + "[")
    assert "\n" not in line
    assert len(line) < 200


def test_noise_uses_the_same_frame_as_app_lines():
    app = _render(_record("aisen.ingest", logging.INFO, "BODY"))
    noise = _render(_record("urllib3.connectionpool", logging.DEBUG, "BODY"))
    assert noise.lstrip().split("]: ", 1)[0].split("] [", 1)[1] == "DEBUG] [urllib3"
    # Same divider, same convention; only the indent and the level differ.
    assert noise.count("]: ") == 1
    assert noise.startswith(logging_setup.NOISE_INDENT)
    assert app.count("]: ") == 1


def test_application_debug_is_not_treated_as_noise():
    line = _render(_record("aisen.ingest", logging.DEBUG, "our own debug"))
    assert not line.startswith(" ")
    assert line.endswith(": our own debug")


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
    assert head.endswith(": unhandled error"), head
    assert "Traceback" in trace
    assert not trace.startswith(" ")


def test_logger_column_is_wide_enough_for_every_known_logger():
    for name in logging_setup.LOGGER_VALUES:
        line = _render(_record("aisen." + name, logging.INFO, "BODY"))
        # No truncation marker means the frame did not have to cut it.
        assert "…" not in line
        assert _bodies(line)[2] == name


# ---- json -------------------------------------------------------------------


def test_json_format_still_one_object_per_line():
    record = _record("aisen.search", logging.INFO, "x", request_id="abc")
    payload = json.loads(logging_setup.JsonFormatter().format(record))
    assert payload["level"] == "INFO"
    assert payload["logger"] == "aisen.search"
    assert payload["request_id"] == "abc"


def test_json_format_spreads_extra_data_flat():
    record = _record("aisen.ingest", logging.INFO, "harvesting", data={"valid": 250})
    payload = json.loads(logging_setup.JsonFormatter().format(record))
    assert payload["valid"] == 250
    assert "extra_data" not in payload
    assert payload["message"] == "harvesting"
