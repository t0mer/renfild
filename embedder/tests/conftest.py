"""Shared fixtures: a stub-backed app and synthetic WAV payloads."""

from __future__ import annotations

import io
import math
import struct

import pytest
from fastapi.testclient import TestClient

from embedder.app import create_app
from embedder.config import Settings


def wav_bytes(seconds: float, freq: float = 220.0, sample_rate: int = 16_000) -> bytes:
    """Build a mono 16-bit PCM WAV of a sine tone, without touching the disk."""
    frames = int(seconds * sample_rate)
    samples = [
        int(0.4 * 32767 * math.sin(2 * math.pi * freq * (n / sample_rate))) for n in range(frames)
    ]
    payload = struct.pack(f"<{len(samples)}h", *samples)
    buf = io.BytesIO()
    buf.write(b"RIFF")
    buf.write(struct.pack("<I", 36 + len(payload)))
    buf.write(b"WAVEfmt ")
    buf.write(struct.pack("<IHHIIHH", 16, 1, 1, sample_rate, sample_rate * 2, 2, 16))
    buf.write(b"data")
    buf.write(struct.pack("<I", len(payload)))
    buf.write(payload)
    return buf.getvalue()


@pytest.fixture
def client() -> TestClient:
    settings = Settings(stub=True)
    with TestClient(create_app(settings)) as test_client:
        yield test_client
