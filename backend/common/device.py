"""Resolve which device the embedding model runs on.

One function, used by the API and by both model-loading paths in
`ingestion/ingest.py`, so the two cannot disagree about it. It deliberately does
not import `sentence_transformers`: only `torch` is needed to answer the
question, and tests inject a stand-in so every branch is exercised without a GPU.
"""

from __future__ import annotations

from typing import Any

# Kept in step with `common.config.EMBED_DEVICES`; the settings class validates
# the raw value at import time, this function validates it again because it is
# also callable on its own.
DEVICES = ("auto", "cpu", "cuda")


def _import_torch() -> Any:
    try:
        import torch
    except ImportError as error:  # pragma: no cover - torch is a hard dependency
        raise RuntimeError(
            "cannot resolve EMBED_DEVICE: torch is not installed "
            f"({error}). Install the service requirements first."
        ) from None
    return torch


def resolve_device(requested: str = "auto", torch_module: Any | None = None) -> str:
    """Return the concrete device (`"cpu"` or `"cuda"`) for `requested`.

    * `cpu` returns immediately and never touches torch.
    * `auto` (the default) uses CUDA when this torch build can see a GPU.
    * `cuda` is a promise. A CPU-only torch build raises here instead of falling
      back, because a silent fallback is indistinguishable from a very slow run:
      the caller gets a wall of progress lines and no hint that the GPU was
      never used.

    `torch_module` exists for tests, which must not download torch or require a
    GPU to cover the CUDA branches.
    """
    choice = (requested or "auto").strip().lower()
    if choice not in DEVICES:
        raise ValueError(
            f"EMBED_DEVICE must be one of {', '.join(DEVICES)} (got {requested!r})."
        )
    if choice == "cpu":
        return "cpu"

    torch = torch_module or _import_torch()
    available = bool(torch.cuda.is_available())

    if choice == "auto":
        return "cuda" if available else "cpu"

    if available:
        return "cuda"

    cuda_version = getattr(getattr(torch, "version", None), "cuda", None)
    try:
        device_count = torch.cuda.device_count()
    except Exception:  # noqa: BLE001 - a broken driver build must still explain itself
        device_count = 0
    raise RuntimeError(
        "EMBED_DEVICE=cuda but this torch build cannot use a GPU "
        f"(torch {getattr(torch, '__version__', 'unknown')}, "
        f"torch.version.cuda={cuda_version!r}, devices={device_count}). "
        "The Docker image installs CPU-only torch by default: run ingestion on "
        "the host with a CUDA torch build, or set EMBED_DEVICE=cpu."
    )
