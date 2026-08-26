"""Fixtures: synthetic audio frames standing in for a microphone."""

from __future__ import annotations

import numpy as np
import pytest

SAMPLE_RATE = 16_000
FRAME_SAMPLES = 1280  # 80 ms, openWakeWord's chunk size


@pytest.fixture
def sample_rate() -> int:
    return SAMPLE_RATE


@pytest.fixture
def frame_samples() -> int:
    return FRAME_SAMPLES


def silence(frames: int = 1) -> list[np.ndarray]:
    return [np.zeros(FRAME_SAMPLES, dtype=np.int16) for _ in range(frames)]


def speech(frames: int = 1, amplitude: float = 0.3, seed: int = 7) -> list[np.ndarray]:
    """Noise bursts loud enough to trip an energy gate — a stand-in for a voice."""
    rng = np.random.default_rng(seed)
    return [
        (rng.normal(0.0, amplitude, FRAME_SAMPLES) * 32767).clip(-32768, 32767).astype(np.int16)
        for _ in range(frames)
    ]
