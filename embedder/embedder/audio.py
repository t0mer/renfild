"""Audio decoding helpers.

Everything the model sees is 16 kHz mono float32 in ``[-1, 1]``; this module is
the single place where that normalisation happens.
"""

from __future__ import annotations

import io

import numpy as np
import soundfile as sf

TARGET_SR = 16_000


class DecodeError(ValueError):
    """Raised when the request body is not audio we can read."""


def decode(data: bytes) -> tuple[np.ndarray, float]:
    """Decode a WAV (or any libsndfile-readable) payload.

    Returns the samples as 16 kHz mono float32 together with the *original*
    duration in seconds.
    """
    if not data:
        raise DecodeError("empty request body")
    try:
        samples, sample_rate = sf.read(io.BytesIO(data), dtype="float32", always_2d=True)
    except Exception as exc:  # noqa: BLE001 — libsndfile raises a family of errors
        raise DecodeError(f"cannot decode audio: {exc}") from exc

    if samples.size == 0:
        raise DecodeError("audio contains no samples")

    mono = samples.mean(axis=1)
    duration_s = len(mono) / float(sample_rate)
    return resample(mono, sample_rate, TARGET_SR), duration_s


def resample(samples: np.ndarray, src_sr: int, dst_sr: int = TARGET_SR) -> np.ndarray:
    """Resample ``samples`` to ``dst_sr``, preferring soxr when it is available."""
    if src_sr == dst_sr:
        return samples.astype(np.float32, copy=False)
    try:
        import soxr

        return soxr.resample(samples, src_sr, dst_sr).astype(np.float32, copy=False)
    except ImportError:
        # Linear interpolation is noticeably worse but keeps the service usable
        # if the optional resampler is missing on an unusual platform.
        duration = len(samples) / float(src_sr)
        target_len = max(1, int(round(duration * dst_sr)))
        src_idx = np.linspace(0.0, len(samples) - 1, num=len(samples), dtype=np.float64)
        dst_idx = np.linspace(0.0, len(samples) - 1, num=target_len, dtype=np.float64)
        return np.interp(dst_idx, src_idx, samples).astype(np.float32, copy=False)


def centre_crop(
    samples: np.ndarray, max_seconds: float, sample_rate: int = TARGET_SR
) -> np.ndarray:
    """Trim over-long audio around its midpoint, where the speech usually is."""
    limit = int(max_seconds * sample_rate)
    if limit <= 0 or len(samples) <= limit:
        return samples
    start = (len(samples) - limit) // 2
    return samples[start : start + limit]
