"""Ring buffer and WAV helper tests."""

from __future__ import annotations

import numpy as np
import pytest

from satellite.audio import RingBuffer, to_float32, to_int16, wav_decode, wav_encode

from .conftest import FRAME_SAMPLES, SAMPLE_RATE


def test_ring_buffer_reports_partial_fill():
    ring = RingBuffer(1000)
    ring.extend(np.arange(300, dtype=np.int16))
    assert len(ring) == 300
    assert np.array_equal(ring.snapshot(), np.arange(300, dtype=np.int16))


def test_ring_buffer_keeps_the_most_recent_audio():
    ring = RingBuffer(1000)
    ring.extend(np.arange(1500, dtype=np.int16))
    snapshot = ring.snapshot()
    assert len(snapshot) == 1000
    # Oldest-first ordering: the last 1000 of 0..1499.
    assert np.array_equal(snapshot, np.arange(500, 1500, dtype=np.int16))


def test_ring_buffer_wraps_across_many_writes():
    ring = RingBuffer(FRAME_SAMPLES * 3)
    for start in range(0, FRAME_SAMPLES * 10, FRAME_SAMPLES):
        ring.extend(np.arange(start, start + FRAME_SAMPLES, dtype=np.int16))
    expected = np.arange(FRAME_SAMPLES * 7, FRAME_SAMPLES * 10, dtype=np.int16)
    assert np.array_equal(ring.snapshot(), expected)


def test_ring_buffer_two_seconds_of_16k_audio():
    """The size the satellite actually uses: 2.0 s at 16 kHz."""
    ring = RingBuffer(2 * SAMPLE_RATE)
    for _ in range(100):  # 8 seconds of 80 ms frames
        ring.extend(np.ones(FRAME_SAMPLES, dtype=np.int16))
    assert len(ring.snapshot()) == 32_000


def test_ring_buffer_clear():
    ring = RingBuffer(100)
    ring.extend(np.ones(50, dtype=np.int16))
    ring.clear()
    assert len(ring) == 0
    assert ring.snapshot().size == 0


def test_ring_buffer_rejects_bad_capacity():
    with pytest.raises(ValueError):
        RingBuffer(0)


def test_wav_round_trip_preserves_audio():
    original = (np.sin(np.linspace(0, 40 * np.pi, SAMPLE_RATE)) * 0.5).astype(np.float32)
    blob = wav_encode(original, SAMPLE_RATE)
    decoded, rate = wav_decode(blob)
    assert rate == SAMPLE_RATE
    assert len(decoded) == len(original)
    assert np.max(np.abs(decoded - original)) < 1e-3


def test_wav_encode_produces_a_riff_header():
    blob = wav_encode(np.zeros(160, dtype=np.int16), SAMPLE_RATE)
    assert blob[:4] == b"RIFF"
    assert blob[8:12] == b"WAVE"


def test_int16_float_conversions_round_trip():
    values = np.array([-1.0, -0.5, 0.0, 0.5, 0.999], dtype=np.float32)
    assert np.max(np.abs(to_float32(to_int16(values)) - values)) < 1e-3
