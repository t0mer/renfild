"""Voice activity detection and command capture.

Silero VAD (ONNX) decides where the user's sentence ends. If the model file is
missing the satellite degrades to a plain RMS gate rather than refusing to run —
a slightly worse endpoint is much better than a dead assistant.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

import numpy as np
from loguru import logger

# Silero v5 expects exactly 512 samples per inference step at 16 kHz, prefixed
# with 64 samples of context carried over from the previous chunk. Feeding it a
# bare 512-sample chunk makes it score even clear speech near zero.
CHUNK_SAMPLES = 512
CONTEXT_SAMPLES = 64


class SpeechDetector:
    """Common interface: float32 audio in, speech probability out."""

    def probability(self, samples: np.ndarray) -> float:  # pragma: no cover - interface
        raise NotImplementedError

    def reset(self) -> None:  # pragma: no cover - interface
        raise NotImplementedError


class EnergyDetector(SpeechDetector):
    """RMS fallback. Crude, but dependency-free and predictable."""

    def __init__(self, rms_threshold: float = 0.012) -> None:
        self.rms_threshold = rms_threshold

    def probability(self, samples: np.ndarray) -> float:
        if samples.size == 0:
            return 0.0
        rms = float(np.sqrt(np.mean(np.square(samples.astype(np.float64)))))
        # Map RMS onto a 0..1 range that straddles the caller's threshold.
        return min(1.0, rms / (self.rms_threshold * 2.0))

    def reset(self) -> None:
        return


class SileroVad(SpeechDetector):
    """Silero VAD v5 running on onnxruntime.

    Audio arrives in 80 ms frames but the model wants 512-sample chunks, so
    leftovers are carried over between calls.
    """

    def __init__(self, model_path: Path, sample_rate: int = 16_000) -> None:
        self.model_path = Path(model_path)
        self.sample_rate = sample_rate
        self._session = None
        self._state = None
        self._legacy = False  # v4 models take separate h/c tensors
        self._carry = np.zeros(0, dtype=np.float32)
        self._context = np.zeros(CONTEXT_SAMPLES, dtype=np.float32)

    def load(self) -> None:
        import onnxruntime

        options = onnxruntime.SessionOptions()
        options.inter_op_num_threads = 1
        options.intra_op_num_threads = 1
        self._session = onnxruntime.InferenceSession(
            str(self.model_path), sess_options=options, providers=["CPUExecutionProvider"]
        )
        names = {i.name for i in self._session.get_inputs()}
        self._legacy = "h" in names and "c" in names
        self.reset()
        logger.info(
            "Silero VAD loaded: {} ({})", self.model_path.name, "v4" if self._legacy else "v5"
        )

    def reset(self) -> None:
        self._carry = np.zeros(0, dtype=np.float32)
        self._context = np.zeros(CONTEXT_SAMPLES, dtype=np.float32)
        if self._legacy:
            self._state = (
                np.zeros((2, 1, 64), dtype=np.float32),
                np.zeros((2, 1, 64), dtype=np.float32),
            )
        else:
            self._state = np.zeros((2, 1, 128), dtype=np.float32)

    def probability(self, samples: np.ndarray) -> float:
        """Return the highest speech probability seen across this frame."""
        if self._session is None:
            raise RuntimeError("VAD model not loaded")
        buffer = np.concatenate((self._carry, np.asarray(samples, dtype=np.float32)))
        best = 0.0
        offset = 0
        while offset + CHUNK_SAMPLES <= len(buffer):
            chunk = buffer[offset : offset + CHUNK_SAMPLES].reshape(1, -1)
            best = max(best, self._infer(chunk))
            offset += CHUNK_SAMPLES
        self._carry = buffer[offset:]
        return best

    def _infer(self, chunk: np.ndarray) -> float:
        sr = np.array(self.sample_rate, dtype=np.int64)
        if self._legacy:
            h, c = self._state
            out, h, c = self._session.run(None, {"input": chunk, "h": h, "c": c, "sr": sr})
            self._state = (h, c)
            return float(np.asarray(out).reshape(-1)[0])

        # v5 wants the previous chunk's tail in front of this one.
        window = np.concatenate((self._context, chunk.reshape(-1))).reshape(1, -1)
        out, self._state = self._session.run(
            None, {"input": window, "state": self._state, "sr": sr}
        )
        self._context = chunk.reshape(-1)[-CONTEXT_SAMPLES:].copy()
        return float(np.asarray(out).reshape(-1)[0])


def build_detector(model_path: Path, fallback_rms: float) -> SpeechDetector:
    """Load Silero if we can, otherwise fall back to the energy gate."""
    path = Path(model_path)
    if path.is_file():
        try:
            detector = SileroVad(path)
            detector.load()
            return detector
        except Exception as exc:  # noqa: BLE001 — any load failure must not be fatal
            logger.error("Silero VAD unavailable ({}), falling back to energy gate", exc)
    else:
        logger.warning("VAD model {} not found — falling back to energy gate", path)
    return EnergyDetector(fallback_rms)


@dataclass
class CommandResult:
    """Outcome of listening for a command after the wake word."""

    samples: np.ndarray
    speech_detected: bool
    duration_s: float
    reason: str  # 'silence' | 'timeout' | 'no-speech'


class CommandRecorder:
    """Records the sentence that follows the wake word.

    Stops on ``trailing_silence_ms`` of quiet after speech has started, on the
    hard cap, or immediately when nobody speaks within ``start_timeout_s``.
    """

    def __init__(
        self,
        detector: SpeechDetector,
        sample_rate: int = 16_000,
        threshold: float = 0.5,
        trailing_silence_ms: int = 700,
        max_command_s: float = 10.0,
        start_timeout_s: float = 4.0,
    ) -> None:
        self.detector = detector
        self.sample_rate = sample_rate
        self.threshold = threshold
        self.trailing_silence_ms = trailing_silence_ms
        self.max_command_s = max_command_s
        self.start_timeout_s = start_timeout_s

    def record(self, frames) -> CommandResult:
        """Consume int16 frames from ``frames`` until the command is complete."""
        self.detector.reset()
        collected: list[np.ndarray] = []
        speech_started = False
        silence_ms = 0.0
        elapsed_s = 0.0

        for frame in frames:
            frame = np.asarray(frame, dtype=np.int16)
            collected.append(frame)
            frame_ms = 1000.0 * len(frame) / self.sample_rate
            elapsed_s += frame_ms / 1000.0

            probability = self.detector.probability(frame.astype(np.float32) / 32768.0)
            is_speech = probability >= self.threshold

            if is_speech:
                speech_started = True
                silence_ms = 0.0
            elif speech_started:
                silence_ms += frame_ms

            if speech_started and silence_ms >= self.trailing_silence_ms:
                return self._result(collected, True, elapsed_s, "silence")
            if not speech_started and elapsed_s >= self.start_timeout_s:
                return self._result(collected, False, elapsed_s, "no-speech")
            if elapsed_s >= self.max_command_s:
                return self._result(collected, speech_started, elapsed_s, "timeout")

        return self._result(collected, speech_started, elapsed_s, "timeout")

    def _result(
        self, collected: list[np.ndarray], speech: bool, elapsed_s: float, reason: str
    ) -> CommandResult:
        samples = np.concatenate(collected) if collected else np.zeros(0, dtype=np.int16)
        return CommandResult(
            samples=samples,
            speech_detected=speech,
            duration_s=round(elapsed_s, 3),
            reason=reason,
        )
