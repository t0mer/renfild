"""Silero VAD tests against the real ONNX model.

These skip when the model has not been downloaded — install.sh fetches it into
satellite/models/, and openWakeWord ships a copy too.
"""

from __future__ import annotations

from pathlib import Path

import numpy as np
import pytest

from satellite.audio import to_int16, wav_decode
from satellite.vad import CommandRecorder, EnergyDetector, SileroVad, build_detector

from .conftest import FRAME_SAMPLES, SAMPLE_RATE, silence

MODEL = Path(__file__).parent.parent / "models" / "silero_vad.onnx"
SPEECH = Path(__file__).parent / "testdata" / "unrelated_speech.wav"

needs_model = pytest.mark.skipif(not MODEL.is_file(), reason="silero_vad.onnx is not downloaded")
needs_speech = pytest.mark.skipif(not SPEECH.is_file(), reason="speech fixture missing")


def speech_frames() -> list[np.ndarray]:
    samples, rate = wav_decode(SPEECH.read_bytes())
    assert rate == SAMPLE_RATE
    pcm = to_int16(samples)
    return [pcm[i : i + FRAME_SAMPLES] for i in range(0, len(pcm) - FRAME_SAMPLES, FRAME_SAMPLES)]


@pytest.fixture
def detector() -> SileroVad:
    vad = SileroVad(MODEL)
    vad.load()
    return vad


@needs_model
def test_model_loads(detector):
    assert detector._session is not None


@needs_model
def test_silence_scores_low(detector):
    for frame in silence(10):
        assert detector.probability(frame.astype(np.float32) / 32768.0) < 0.5


@needs_model
@needs_speech
def test_speech_scores_high(detector):
    peak = max(
        detector.probability(frame.astype(np.float32) / 32768.0) for frame in speech_frames()
    )
    assert peak > 0.8, f"speech peaked at only {peak:.2f}"


@needs_model
@needs_speech
def test_recorder_ends_the_command_on_trailing_silence(detector):
    recorder = CommandRecorder(detector, trailing_silence_ms=700, max_command_s=10.0)
    audio = speech_frames() + silence(20)

    result = recorder.record(iter(audio))

    assert result.speech_detected is True
    assert result.reason == "silence"
    # The recording stops shortly after the speech ends, not at the hard cap.
    assert result.duration_s < 10.0
    assert result.duration_s > 1.0


@needs_model
def test_recorder_treats_pure_silence_as_a_false_trigger(detector):
    recorder = CommandRecorder(detector, start_timeout_s=1.0)

    result = recorder.record(iter(silence(200)))

    assert result.speech_detected is False
    assert result.reason == "no-speech"
    assert result.duration_s == pytest.approx(1.0, abs=0.1)


@needs_model
def test_build_detector_prefers_silero():
    assert isinstance(build_detector(MODEL, 0.012), SileroVad)


def test_build_detector_falls_back_when_the_model_is_missing(tmp_path):
    assert isinstance(build_detector(tmp_path / "nope.onnx", 0.012), EnergyDetector)


def test_build_detector_falls_back_on_a_corrupt_model(tmp_path):
    broken = tmp_path / "silero_vad.onnx"
    broken.write_bytes(b"this is not an onnx graph")
    assert isinstance(build_detector(broken, 0.012), EnergyDetector)


# --------------------------------------------------------------------------- #
# v5 context window
# --------------------------------------------------------------------------- #


class FakeSession:
    """Records what gets fed to the model, so the window shape can be asserted."""

    def __init__(self, inputs: list[str]) -> None:
        self._inputs = inputs
        self.windows: list[np.ndarray] = []

    def get_inputs(self):
        return [type("Input", (), {"name": name})() for name in self._inputs]

    def run(self, _outputs, feed):
        self.windows.append(feed["input"].copy())
        if "state" in feed:
            return np.array([[0.5]], dtype=np.float32), feed["state"]
        return (
            np.array([[0.5]], dtype=np.float32),
            feed["h"],
            feed["c"],
        )


def fake_vad(inputs: list[str]) -> tuple[SileroVad, FakeSession]:
    vad = SileroVad(Path("unused.onnx"))
    session = FakeSession(inputs)
    vad._session = session
    vad._legacy = "h" in inputs
    vad.reset()
    return vad, session


def test_v5_prepends_the_previous_chunk_as_context():
    """Silero v5 scores even clear speech near zero without its 64-sample context."""
    vad, session = fake_vad(["input", "state", "sr"])
    samples = np.arange(1024, dtype=np.float32) / 1024.0

    vad.probability(samples)

    assert len(session.windows) == 2
    assert session.windows[0].shape == (1, 576), "v5 expects 64 context + 512 chunk"
    # The second window's context is the tail of the first chunk.
    assert np.allclose(session.windows[1][0, :64], samples[512 - 64 : 512])


def test_v5_context_starts_empty_and_resets():
    vad, session = fake_vad(["input", "state", "sr"])
    samples = np.ones(512, dtype=np.float32)

    vad.probability(samples)
    assert np.allclose(session.windows[0][0, :64], 0.0), "the first window has no history yet"

    vad.reset()
    vad.probability(samples)
    assert np.allclose(session.windows[1][0, :64], 0.0), "reset must clear the context"


def test_v4_is_fed_bare_chunks():
    vad, session = fake_vad(["input", "h", "c", "sr"])

    vad.probability(np.ones(512, dtype=np.float32))

    assert session.windows[0].shape == (1, 512)


def test_leftover_samples_carry_into_the_next_call():
    vad, session = fake_vad(["input", "state", "sr"])

    # 700 samples is one full chunk plus a remainder that must not be dropped.
    vad.probability(np.ones(700, dtype=np.float32))
    assert len(session.windows) == 1

    vad.probability(np.ones(400, dtype=np.float32))
    assert len(session.windows) == 2, "the carried remainder should complete a second chunk"
