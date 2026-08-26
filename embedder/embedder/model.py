"""ECAPA-TDNN speaker embedding.

The model is loaded once at startup and reused for every request. Inference is
CPU-only and single-threaded per request, which is fast enough on a Pi 4 for the
two embeddings a single utterance needs.
"""

from __future__ import annotations

import hashlib
import threading
from pathlib import Path

import numpy as np
from loguru import logger

EMBEDDING_DIM = 192


def l2_normalize(vector: np.ndarray) -> np.ndarray:
    """Scale ``vector`` to unit length so cosine similarity is a plain dot product."""
    norm = float(np.linalg.norm(vector))
    if norm == 0.0:
        return vector.astype(np.float32, copy=False)
    return (vector / norm).astype(np.float32, copy=False)


class Embedder:
    """Thread-safe wrapper around SpeechBrain's ``EncoderClassifier``."""

    def __init__(self, source: str, model_dir: Path, device: str = "cpu", threads: int = 3) -> None:
        self._source = source
        self._model_dir = model_dir
        self._device = device
        self._threads = threads
        self._model = None
        self._lock = threading.Lock()

    @property
    def loaded(self) -> bool:
        return self._model is not None

    def load(self) -> None:
        """Materialise the model. Downloads on first run, offline afterwards."""
        import torch
        from speechbrain.inference.speaker import EncoderClassifier

        torch.set_num_threads(max(1, self._threads))
        self._model_dir.mkdir(parents=True, exist_ok=True)
        logger.info("loading ECAPA model source={} dir={}", self._source, self._model_dir)
        self._model = EncoderClassifier.from_hparams(
            source=self._source,
            savedir=str(self._model_dir),
            run_opts={"device": self._device},
        )
        logger.info("ECAPA model ready ({} dims)", EMBEDDING_DIM)

    def embed(self, samples: np.ndarray) -> np.ndarray:
        """Return the L2-normalized 192-dim embedding for 16 kHz mono ``samples``."""
        if self._model is None:
            raise RuntimeError("model not loaded")
        import torch

        wav = torch.from_numpy(np.ascontiguousarray(samples, dtype=np.float32)).unsqueeze(0)
        # SpeechBrain is not documented as re-entrant; serialise inference.
        with self._lock, torch.no_grad():
            out = self._model.encode_batch(wav)
        vector = out.squeeze().detach().cpu().numpy().astype(np.float32)
        if vector.shape[-1] != EMBEDDING_DIM:
            raise RuntimeError(f"unexpected embedding size {vector.shape[-1]}")
        return l2_normalize(vector)


class StubEmbedder:
    """Deterministic stand-in used by tests, enabled with ``RENFILD_EMB_STUB=1``.

    It derives a vector from the audio's coarse spectral shape, so identical
    audio yields identical embeddings and similar audio yields similar ones —
    enough to exercise the contract without downloading real weights.
    """

    def __init__(self) -> None:
        self._loaded = False

    @property
    def loaded(self) -> bool:
        return self._loaded

    def load(self) -> None:
        logger.warning("RENFILD_EMB_STUB is set — serving fake embeddings")
        self._loaded = True

    def embed(self, samples: np.ndarray) -> np.ndarray:
        spectrum = np.abs(np.fft.rfft(samples))
        if spectrum.size < EMBEDDING_DIM:
            spectrum = np.pad(spectrum, (0, EMBEDDING_DIM - spectrum.size))
        bins = np.array_split(spectrum, EMBEDDING_DIM)
        vector = np.array([float(b.mean()) for b in bins], dtype=np.float32)
        # Mix in a hash of the rounded waveform so silence still produces a
        # stable, non-zero vector.
        seed = int.from_bytes(
            hashlib.sha256(np.round(samples, 3).tobytes()).digest()[:8], "little"
        )
        jitter = np.random.default_rng(seed).normal(0.0, 1e-3, EMBEDDING_DIM).astype(np.float32)
        return l2_normalize(vector + jitter)


def build(*, stub: bool, source: str, model_dir: Path, device: str, threads: int):
    """Return the embedder implementation selected by configuration."""
    if stub:
        return StubEmbedder()
    return Embedder(source=source, model_dir=model_dir, device=device, threads=threads)
