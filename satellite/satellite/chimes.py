"""Local feedback sounds.

Chimes are synthesised rather than shipped as binary assets: they cost nothing
to generate, sound the same on every install, and keep audio blobs out of git.
Any of them can be overridden with a WAV file from config.
"""

from __future__ import annotations

from pathlib import Path

import numpy as np
from loguru import logger

from .audio import wav_decode

SAMPLE_RATE = 16_000


def _tone(freq: float, seconds: float, volume: float = 1.0) -> np.ndarray:
    """A single sine tone with short fades, so it clicks on neither end."""
    samples = np.arange(int(seconds * SAMPLE_RATE), dtype=np.float32)
    wave = np.sin(2.0 * np.pi * freq * samples / SAMPLE_RATE).astype(np.float32)
    fade = max(1, int(0.008 * SAMPLE_RATE))
    envelope = np.ones_like(wave)
    envelope[:fade] = np.linspace(0.0, 1.0, fade)
    envelope[-fade:] = np.linspace(1.0, 0.0, fade)
    return wave * envelope * volume


def _sequence(*tones: np.ndarray) -> np.ndarray:
    return np.concatenate(tones) if tones else np.zeros(0, dtype=np.float32)


def listening() -> np.ndarray:
    """Rising two-note blip: 'I heard my name, go ahead.' ~180 ms."""
    return _sequence(_tone(880.0, 0.07), _tone(1318.0, 0.10))


def ack() -> np.ndarray:
    """Single soft note: 'done, nothing to say.' ~120 ms."""
    return _sequence(_tone(1046.0, 0.12))


def error() -> np.ndarray:
    """Falling two-note buzz: 'something went wrong.' ~240 ms."""
    return _sequence(_tone(440.0, 0.12), _tone(294.0, 0.14))


class ChimePlayer:
    """Plays the three feedback sounds, honouring file overrides and the mute flag."""

    def __init__(
        self,
        speaker,
        enabled: bool = True,
        volume: float = 0.35,
        overrides: dict[str, Path | None] | None = None,
    ) -> None:
        self._speaker = speaker
        self._enabled = enabled
        self._volume = volume
        self._cache: dict[str, tuple[np.ndarray, int]] = {}
        self._overrides = {k: v for k, v in (overrides or {}).items() if v is not None}
        self._generators = {"listening": listening, "ack": ack, "error": error}

    def _load(self, name: str) -> tuple[np.ndarray, int]:
        if name in self._cache:
            return self._cache[name]
        override = self._overrides.get(name)
        if override is not None:
            try:
                samples, rate = wav_decode(Path(override).read_bytes())
            except Exception as exc:  # noqa: BLE001 — bad override must not be fatal
                logger.error("cannot load chime override {}: {}", override, exc)
                samples, rate = self._generators[name](), SAMPLE_RATE
        else:
            samples, rate = self._generators[name](), SAMPLE_RATE
        self._cache[name] = (samples * self._volume, rate)
        return self._cache[name]

    def play(self, name: str) -> None:
        """Play a chime; never raise — feedback sounds must not break the loop."""
        if not self._enabled:
            return
        try:
            samples, rate = self._load(name)
            self._speaker.play(samples, rate)
        except Exception as exc:  # noqa: BLE001
            logger.warning("chime {} failed: {}", name, exc)
