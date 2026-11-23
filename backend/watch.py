"""Standalone health watcher for the API.

Polls `/ready` and logs a line only when the state changes, so a service that
stops answering is noticed even when nobody is reading its logs. Designed to run
as the `watch` Compose service next to `aisen-api`; stdlib only, no dependencies.

    python watch.py

Environment:

- `WATCH_URL`              default `http://127.0.0.1:8000/ready`
- `WATCH_INTERVAL_SECONDS` default `30`
- `WATCH_TIMEOUT_SECONDS`  default `5`
- `ALERT_WEBHOOK_URL`      optional; POSTs `{"content", "text"}` on every change,
                           which Discord and most generic webhooks accept
"""

from __future__ import annotations

import json
import os
import time
import urllib.erro
import urllib.request
from datetime import datetime, timezone

WATCH_URL = os.getenv("WATCH_URL", "http://127.0.0.1:8000/ready")
INTERVAL = float(os.getenv("WATCH_INTERVAL_SECONDS", "30"))
TIMEOUT = float(os.getenv("WATCH_TIMEOUT_SECONDS", "5"))
WEBHOOK = os.getenv("ALERT_WEBHOOK_URL", "")


def _now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def _log(message: str) -> None:
    print(f"{_now()} watch: {message}", flush=True)


def probe() -> tuple[bool, str]:
    """Return (healthy, detail). Never raises: an unreachable API is unhealthy."""
    try:
        with urllib.request.urlopen(WATCH_URL, timeout=TIMEOUT) as response:
            body = response.read(2000).decode("utf-8", "replace")
            if response.status == 200:
                return True, f"status={response.status}"
            return False, f"status={response.status} {body}"
    except urllib.error.HTTPError as error:
        body = error.read(2000).decode("utf-8", "replace")
        return False, f"status={error.code} {body}"
    except Exception as error:  # noqa: BLE001 - unreachable/timeout/JSON all mean unhealthy
        return False, f"{type(error).__name__}: {error}"


def notify(message: str) -> None:
    if not WEBHOOK:
        return
    payload = json.dumps({"content": message, "text": message}).encode("utf-8")
    request = urllib.request.Request(
        WEBHOOK,
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        urllib.request.urlopen(request, timeout=TIMEOUT).read()
    except Exception as error:  # noqa: BLE001 - a failed alert must not stop the loop
        _log(f"webhook failed: {type(error).__name__}: {error}")


def main() -> int:
    _log(f"watching {WATCH_URL} every {INTERVAL:g}s")

    healthy, detail = probe()
    previous = healthy
    _log(f"initial state: {'healthy' if healthy else 'UNHEALTHY'} ({detail})")

    while True:
        time.sleep(INTERVAL)
        healthy, detail = probe()
        if healthy == previous:
            continue

        if healthy:
            message = f"AISEn API is healthy again ({WATCH_URL})"
        else:
            message = f"AISEn API is UNHEALTHY ({WATCH_URL}): {detail}"

        _log(
            f"state change: {'healthy' if previous else 'UNHEALTHY'} -> "
            f"{'healthy' if healthy else 'UNHEALTHY'} ({detail})"
        )
        notify(message)
        previous = healthy


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except KeyboardInterrupt:
        raise SystemExit(0)
