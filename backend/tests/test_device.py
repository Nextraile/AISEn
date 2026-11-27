"""The embedding device is decided in exactly one place."""

import pytest

from common.device import resolve_device


class FakeCuda:
    def __init__(self, available, count=1):
        self.available = available
        self.count = count

    def is_available(self):
        return self.available

    def device_count(self):
        return self.count


class FakeTorch:
    """Just enough of the torch surface `resolve_device` reads."""

    def __init__(self, available, cuda_version="12.6", count=1):
        self.__version__ = "2.14.1+cu126"
        self.cuda = FakeCuda(available, count)
        self.version = type("Version", (), {"cuda": cuda_version})


class ExplodingTorch:
    @property
    def cuda(self):
        raise AssertionError("EMBED_DEVICE=cpu must not query torch")


def test_cpu_is_forced_without_asking_torch():
    # The point of the check: pinning the CPU must work on a machine whose
    # driver is broken, so it cannot depend on a successful torch query.
    assert resolve_device("cpu", torch_module=ExplodingTorch()) == "cpu"


def test_auto_picks_cuda_when_torch_sees_a_gpu():
    assert resolve_device("auto", torch_module=FakeTorch(available=True)) == "cuda"


def test_auto_falls_back_to_cpu_when_torch_sees_nothing():
    assert resolve_device("auto", torch_module=FakeTorch(available=False)) == "cpu"


def test_cuda_is_returned_when_it_is_available():
    assert resolve_device("cuda", torch_module=FakeTorch(available=True)) == "cuda"


def test_cuda_on_a_cpu_build_raises_instead_of_falling_back():
    # A silent fallback is the failure this setting exists to prevent: the
    # encode would take 10x longer with nothing in the log to explain it.
    with pytest.raises(RuntimeError) as excinfo:
        resolve_device(
            "cuda",
            torch_module=FakeTorch(available=False, cuda_version=None, count=0),
        )

    message = str(excinfo.value)
    assert "EMBED_DEVICE=cuda" in message
    assert "torch 2.14.1+cu126" in message
    assert "torch.version.cuda=None" in message
    assert "devices=0" in message


def test_device_name_is_case_and_space_insensitive():
    assert resolve_device("  CPU ", torch_module=ExplodingTorch()) == "cpu"
    assert resolve_device("CUDA", torch_module=FakeTorch(available=True)) == "cuda"


def test_unknown_device_is_rejected():
    with pytest.raises(ValueError, match="EMBED_DEVICE"):
        resolve_device("gpu", torch_module=FakeTorch(available=True))


def test_empty_value_means_auto():
    assert resolve_device("", torch_module=FakeTorch(available=False)) == "cpu"
