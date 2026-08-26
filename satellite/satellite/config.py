"""Satellite configuration.

Values resolve in this order: environment (prefix ``RENFILD_SAT_``, nested keys
joined with ``__``, e.g. ``RENFILD_SAT_WAKE__THRESHOLD``) > ``config.yaml`` >
built-in defaults.
"""

from __future__ import annotations

import os
from pathlib import Path
from typing import Any

import yaml
from pydantic import BaseModel, Field
from pydantic_settings import BaseSettings, SettingsConfigDict

CONFIG_PATH_ENV = "RENFILD_SAT_CONFIG"
DEFAULT_CONFIG_PATHS = (
    Path("config.yaml"),
    Path("/opt/renfild/etc/satellite.yaml"),
)


class ServerConfig(BaseModel):
    """Where the brain lives."""

    url: str = "http://127.0.0.1:8080"
    timeout_s: float = 15.0
    # Retried once on a connection error; a dropped Wi-Fi packet should not
    # cost the user their sentence.
    retries: int = 1


class AudioConfig(BaseModel):
    """Capture and playback devices.

    Devices are matched by *name substring*, never by index: ALSA indexes shift
    between reboots and USB re-plugs.
    """

    input_device: str = "USB"
    output_device: str = "USB"
    sample_rate: int = 16_000
    # 1280 samples at 16 kHz — openWakeWord's expected chunk size.
    frame_ms: int = 80
    ring_seconds: float = 2.0
    input_gain: float = 1.0
    output_volume: float = 1.0


class WakeConfig(BaseModel):
    """openWakeWord settings."""

    model_path: Path = Path("models/hey_renfild.onnx")
    threshold: float = 0.6
    debounce_s: float = 3.0
    # 'auto' picks tflite or onnx from the model file extension.
    inference_framework: str = "auto"
    # Directory holding openWakeWord's shared melspectrogram/embedding models.
    resources_dir: Path | None = None
    enabled: bool = True


class VadConfig(BaseModel):
    """Silero VAD settings governing end-of-command detection."""

    model_path: Path = Path("models/silero_vad.onnx")
    threshold: float = 0.5
    trailing_silence_ms: int = 700
    max_command_s: float = 10.0
    # If nobody speaks within this window after the wake word, treat the
    # detection as a false trigger and go back to listening.
    start_timeout_s: float = 4.0
    # Energy floor used only when the Silero model is unavailable.
    fallback_rms: float = 0.012


class ChimeConfig(BaseModel):
    """Local feedback sounds, played before any network round trip."""

    enabled: bool = True
    listening: Path | None = None
    ack: Path | None = None
    error: Path | None = None
    volume: float = 0.35


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_prefix="RENFILD_SAT_",
        env_nested_delimiter="__",
        extra="ignore",
    )

    @classmethod
    def settings_customise_sources(
        cls,
        settings_cls,
        init_settings,
        env_settings,
        dotenv_settings,
        file_secret_settings,
    ):
        # YAML arrives as init kwargs; environment must still win over it.
        return env_settings, dotenv_settings, init_settings, file_secret_settings

    satellite_id: str = "living-room"
    log_level: str = "INFO"

    server: ServerConfig = Field(default_factory=ServerConfig)
    audio: AudioConfig = Field(default_factory=AudioConfig)
    wake: WakeConfig = Field(default_factory=WakeConfig)
    vad: VadConfig = Field(default_factory=VadConfig)
    chimes: ChimeConfig = Field(default_factory=ChimeConfig)

    # Milestone-2 / debugging aid: when set, wake.wav and command.wav are
    # written here for every detection.
    dump_dir: Path | None = None
    # Capture and dump only — never contact the server. Useful for tuning the
    # wake word by ear before the server exists.
    offline: bool = False

    @property
    def frame_samples(self) -> int:
        return int(self.audio.sample_rate * self.audio.frame_ms / 1000)


def _load_yaml(path: Path) -> dict[str, Any]:
    with path.open("r", encoding="utf-8") as handle:
        data = yaml.safe_load(handle) or {}
    if not isinstance(data, dict):
        raise ValueError(f"{path}: expected a mapping at the top level")
    return data


def find_config_file() -> Path | None:
    """Locate config.yaml from the env var or the conventional locations."""
    explicit = os.environ.get(CONFIG_PATH_ENV)
    if explicit:
        path = Path(explicit)
        if not path.is_file():
            raise FileNotFoundError(f"{CONFIG_PATH_ENV}={explicit} does not exist")
        return path
    return next((p for p in DEFAULT_CONFIG_PATHS if p.is_file()), None)


def load(path: Path | None = None) -> Settings:
    """Build Settings from YAML (if present) with environment overrides on top."""
    config_file = path or find_config_file()
    base: dict[str, Any] = _load_yaml(config_file) if config_file else {}
    return Settings(**base)
