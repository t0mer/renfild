"""Configuration precedence: environment beats YAML beats defaults."""

from __future__ import annotations

from pathlib import Path

from satellite import config


def write_yaml(tmp_path: Path, body: str) -> Path:
    path = tmp_path / "config.yaml"
    path.write_text(body, encoding="utf-8")
    return path


def test_defaults_match_the_spec():
    settings = config.Settings()
    assert settings.audio.sample_rate == 16_000
    assert settings.audio.frame_ms == 80
    assert settings.frame_samples == 1280
    assert settings.audio.ring_seconds == 2.0
    assert settings.wake.threshold == 0.6
    assert settings.wake.debounce_s == 3.0
    assert settings.vad.trailing_silence_ms == 700
    assert settings.vad.max_command_s == 10.0
    assert settings.vad.start_timeout_s == 4.0


def test_yaml_overrides_defaults(tmp_path):
    path = write_yaml(
        tmp_path,
        """
satellite_id: kitchen
audio:
  input_device: Samson
wake:
  threshold: 0.75
""",
    )
    settings = config.load(path)
    assert settings.satellite_id == "kitchen"
    assert settings.audio.input_device == "Samson"
    assert settings.wake.threshold == 0.75
    # Untouched keys keep their defaults.
    assert settings.vad.trailing_silence_ms == 700


def test_environment_overrides_yaml(tmp_path, monkeypatch):
    path = write_yaml(tmp_path, "satellite_id: kitchen\nwake:\n  threshold: 0.75\n")
    monkeypatch.setenv("RENFILD_SAT_SATELLITE_ID", "office")
    monkeypatch.setenv("RENFILD_SAT_WAKE__THRESHOLD", "0.42")
    settings = config.load(path)
    assert settings.satellite_id == "office"
    assert settings.wake.threshold == 0.42


def test_frame_samples_follow_the_frame_length():
    settings = config.Settings(audio={"frame_ms": 40})
    assert settings.frame_samples == 640
