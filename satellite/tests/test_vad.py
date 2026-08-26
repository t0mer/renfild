"""Command segmentation tests.

The recorder is driven with a scripted detector so the expectations are about
segmentation logic, not about Silero's acoustic judgement.
"""

from __future__ import annotations

import numpy as np

from satellite.vad import CommandRecorder, EnergyDetector, SpeechDetector

from .conftest import FRAME_SAMPLES, SAMPLE_RATE, silence, speech

FRAME_MS = 1000 * FRAME_SAMPLES / SAMPLE_RATE  # 80 ms


class ScriptedDetector(SpeechDetector):
    """Returns a pre-programmed probability per frame."""

    def __init__(self, probabilities: list[float]) -> None:
        self.probabilities = list(probabilities)
        self.index = 0
        self.resets = 0

    def probability(self, samples: np.ndarray) -> float:
        value = self.probabilities[min(self.index, len(self.probabilities) - 1)]
        self.index += 1
        return value

    def reset(self) -> None:
        self.resets += 1
        self.index = 0


def recorder(detector: SpeechDetector, **kwargs) -> CommandRecorder:
    defaults = {
        "sample_rate": SAMPLE_RATE,
        "threshold": 0.5,
        "trailing_silence_ms": 700,
        "max_command_s": 10.0,
        "start_timeout_s": 4.0,
    }
    defaults.update(kwargs)
    return CommandRecorder(detector, **defaults)


def frames(count: int):
    return iter(silence(count))


def test_stops_after_trailing_silence():
    # 10 frames of speech, then silence. 700 ms of silence is 9 frames of 80 ms.
    script = [0.9] * 10 + [0.0] * 20
    detector = ScriptedDetector(script)
    result = recorder(detector).record(frames(30))

    assert result.reason == "silence"
    assert result.speech_detected is True
    # 10 speech frames + the 9 silent frames it took to reach 700 ms.
    assert detector.index == 19
    assert len(result.samples) == 19 * FRAME_SAMPLES


def test_ignores_silence_before_speech_starts():
    script = [0.0] * 5 + [0.9] * 5 + [0.0] * 20
    detector = ScriptedDetector(script)
    result = recorder(detector).record(frames(40))

    assert result.reason == "silence"
    assert detector.index == 19  # 5 + 5 + 9


def test_short_gaps_do_not_end_the_command():
    # A 320 ms pause mid-sentence must not terminate the recording.
    script = [0.9] * 5 + [0.0] * 4 + [0.9] * 5 + [0.0] * 20
    detector = ScriptedDetector(script)
    result = recorder(detector).record(frames(50))

    assert result.reason == "silence"
    assert detector.index == 23  # 5 + 4 + 5 + 9


def test_no_speech_within_start_timeout_is_a_false_trigger():
    detector = ScriptedDetector([0.0] * 100)
    result = recorder(detector).record(frames(200))

    assert result.reason == "no-speech"
    assert result.speech_detected is False
    assert result.duration_s >= 4.0


def test_hard_cap_stops_a_monologue():
    detector = ScriptedDetector([0.9] * 500)
    result = recorder(detector, max_command_s=2.0).record(frames(200))

    assert result.reason == "timeout"
    assert result.speech_detected is True
    assert result.duration_s <= 2.0 + FRAME_MS / 1000


def test_recorder_resets_the_detector_for_every_command():
    detector = ScriptedDetector([0.9] * 3 + [0.0] * 20)
    rec = recorder(detector)
    rec.record(frames(30))
    rec.record(frames(30))
    assert detector.resets == 2


def test_exhausted_frame_source_returns_what_was_captured():
    detector = ScriptedDetector([0.9] * 5)
    result = recorder(detector).record(frames(5))
    assert result.reason == "timeout"
    assert len(result.samples) == 5 * FRAME_SAMPLES


def test_energy_detector_separates_speech_from_silence():
    detector = EnergyDetector(rms_threshold=0.012)
    quiet = detector.probability(silence(1)[0].astype(np.float32) / 32768.0)
    loud = detector.probability(speech(1)[0].astype(np.float32) / 32768.0)
    assert quiet < 0.5 <= loud


def test_energy_detector_drives_a_real_recording():
    detector = EnergyDetector(rms_threshold=0.012)
    audio = speech(6) + silence(15)
    result = recorder(detector).record(iter(audio))
    assert result.reason == "silence"
    assert result.speech_detected is True
