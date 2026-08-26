"""Wake word tests.

The threshold, debounce and muting logic runs against a stub model so it is
always exercised. The detection tests need openWakeWord's models and a fixture
clip, and skip when either is missing:

    python -c "import openwakeword.utils as u; u.download_models()"
"""

from __future__ import annotations

import time
from pathlib import Path

import numpy as np
import pytest

from satellite.wake import Detection, WakeWord, _install_tflite_shim

from .conftest import FRAME_SAMPLES, SAMPLE_RATE, silence

TESTDATA = Path(__file__).parent / "testdata"


def oww_models_dir() -> Path | None:
    """Locate openWakeWord's downloaded model directory, if it exists."""
    try:
        import openwakeword
    except ImportError:
        return None
    directory = Path(openwakeword.__file__).parent / "resources" / "models"
    return directory if (directory / "melspectrogram.onnx").is_file() else None


class StubModel:
    """Stands in for openwakeword.model.Model."""

    def __init__(self, scores: list[float]) -> None:
        self.scores = list(scores)
        self.index = 0
        self.resets = 0

    def predict(self, frame: np.ndarray) -> dict[str, float]:
        assert frame.dtype == np.int16
        score = self.scores[min(self.index, len(self.scores) - 1)]
        self.index += 1
        return {"renfild": score}

    def reset(self) -> None:
        self.resets += 1


def stub_wake(scores: list[float], **kwargs) -> WakeWord:
    wake = WakeWord(Path("models/renfild.onnx"), **kwargs)
    wake._model = StubModel(scores)
    return wake


# --------------------------------------------------------------------------- #
# logic
# --------------------------------------------------------------------------- #


def test_fires_only_above_the_threshold():
    wake = stub_wake([0.1, 0.5, 0.59, 0.61], threshold=0.6)
    frame = silence(1)[0]

    assert wake.process(frame) is None
    assert wake.process(frame) is None
    assert wake.process(frame) is None

    detection = wake.process(frame)
    assert isinstance(detection, Detection)
    assert detection.name == "renfild"
    assert detection.score == pytest.approx(0.61)


def test_debounce_suppresses_immediate_retriggers():
    wake = stub_wake([0.9] * 10, threshold=0.6, debounce_s=3.0)
    frame = silence(1)[0]

    assert wake.process(frame) is not None
    # The same spoken wake word keeps scoring high for several frames; only the
    # first one may count.
    for _ in range(5):
        assert wake.process(frame) is None


def test_debounce_expires():
    wake = stub_wake([0.9] * 10, threshold=0.6, debounce_s=0.05)
    frame = silence(1)[0]

    assert wake.process(frame) is not None
    assert wake.process(frame) is None
    time.sleep(0.06)
    assert wake.process(frame) is not None


def test_mute_for_blocks_detections():
    wake = stub_wake([0.9] * 10, threshold=0.6, debounce_s=0.0)
    frame = silence(1)[0]

    wake.mute_for(5.0)
    assert wake.process(frame) is None


def test_reset_clears_the_model_buffer():
    wake = stub_wake([0.1], threshold=0.6)
    wake.reset()
    assert wake._model.resets == 1


def test_process_without_a_loaded_model_is_an_error():
    wake = WakeWord(Path("models/renfild.onnx"))
    with pytest.raises(RuntimeError):
        wake.process(silence(1)[0])


def test_missing_model_file_is_reported_clearly():
    wake = WakeWord(Path("models/does-not-exist.onnx"))
    with pytest.raises(FileNotFoundError, match="wake word model not found"):
        wake.load()


@pytest.mark.parametrize(
    ("filename", "configured", "expected"),
    [
        ("renfild.tflite", "auto", "tflite"),
        ("renfild.onnx", "auto", "onnx"),
        ("renfild.TFLITE", "auto", "tflite"),
        ("renfild.tflite", "onnx", "onnx"),
        ("renfild.onnx", "tflite", "tflite"),
    ],
)
def test_framework_follows_the_model_extension(filename, configured, expected):
    wake = WakeWord(Path(filename), framework=configured)
    assert wake.resolve_framework() == expected


def test_tflite_shim_provides_an_interpreter():
    """CPython 3.12+ has no tflite-runtime; ai-edge-litert stands in for it."""
    pytest.importorskip("ai_edge_litert")
    _install_tflite_shim()

    import tflite_runtime.interpreter as tflite

    assert hasattr(tflite, "Interpreter")


# --------------------------------------------------------------------------- #
# real models
# --------------------------------------------------------------------------- #

models_dir = oww_models_dir()
needs_models = pytest.mark.skipif(
    models_dir is None,
    reason="openWakeWord models are not downloaded",
)


def frames_from_wav(path: Path) -> list[np.ndarray]:
    """Split a 16 kHz mono WAV into the 80 ms frames the satellite feeds in."""
    from satellite.audio import to_int16, wav_decode

    samples, rate = wav_decode(path.read_bytes())
    assert rate == SAMPLE_RATE, f"{path} is {rate} Hz, expected {SAMPLE_RATE}"
    pcm = to_int16(samples)
    return [pcm[i : i + FRAME_SAMPLES] for i in range(0, len(pcm) - FRAME_SAMPLES, FRAME_SAMPLES)]


def best_score(wake: WakeWord, frames: list[np.ndarray]) -> float:
    """Highest score the model gives across a clip, ignoring the threshold."""
    highest = 0.0
    for frame in frames:
        scores = wake._model.predict(np.asarray(frame, dtype=np.int16))
        highest = max(highest, max(scores.values()))
    return highest


@needs_models
@pytest.mark.parametrize("framework", ["onnx", "tflite"])
def test_pretrained_model_loads_and_scores(framework):
    """Both inference backends load and produce scores for real audio."""
    wake = WakeWord(
        models_dir / f"hey_jarvis_v0.1.{framework}",
        threshold=0.5,
        framework=framework,
        resources_dir=models_dir,
    )
    wake.load()

    # Silence must not look like a wake word under either backend.
    assert best_score(wake, silence(30)) < 0.1


@needs_models
@pytest.mark.skipif(
    not (TESTDATA / "hey_jarvis.wav").is_file(),
    reason="fixture clip missing — run hack/wake-fixtures.sh",
)
def test_pretrained_model_detects_its_wake_word():
    wake = WakeWord(
        models_dir / "hey_jarvis_v0.1.onnx",
        threshold=0.5,
        framework="onnx",
        resources_dir=models_dir,
    )
    wake.load()

    detections = [
        detection
        for frame in frames_from_wav(TESTDATA / "hey_jarvis.wav")
        if (detection := wake.process(frame)) is not None
    ]
    assert detections, "the wake word in the fixture was not detected"
    assert detections[0].score >= 0.5


@needs_models
@pytest.mark.skipif(
    not (TESTDATA / "unrelated_speech.wav").is_file(),
    reason="fixture clip missing — run hack/wake-fixtures.sh",
)
def test_pretrained_model_ignores_unrelated_speech():
    wake = WakeWord(
        models_dir / "hey_jarvis_v0.1.onnx",
        threshold=0.5,
        framework="onnx",
        resources_dir=models_dir,
    )
    wake.load()

    for frame in frames_from_wav(TESTDATA / "unrelated_speech.wav"):
        assert wake.process(frame) is None, "a sentence without the wake word triggered it"
