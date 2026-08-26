"""Runtime configuration for the embedder service."""

from __future__ import annotations

from pathlib import Path

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """Settings resolved from env vars (prefix ``RENFILD_EMB_``) or an env file.

    The sidecar binds to localhost by default: only the Renfild server is meant
    to talk to it, so there is no authentication.
    """

    model_config = SettingsConfigDict(
        env_prefix="RENFILD_EMB_",
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    host: str = "127.0.0.1"
    port: int = 8100
    log_level: str = "INFO"

    # SpeechBrain source (HuggingFace repo id or a local directory) and the
    # cache directory the weights are materialised into. After the first start
    # the service runs fully offline from this directory.
    model_source: str = "speechbrain/spkrec-ecapa-voxceleb"
    model_dir: Path = Path("models/spkrec-ecapa-voxceleb")

    # Inference device. CPU is the only sane choice on a Raspberry Pi.
    device: str = "cpu"

    # Number of torch intra-op threads. The Pi 4 has 4 cores; leaving one for
    # the rest of the stack keeps the server responsive during synthesis.
    torch_threads: int = 3

    # Shortest accepted utterance. Anything below this is rejected with 422 —
    # ECAPA embeddings from sub-half-second clips are noise.
    min_duration_s: float = 0.5

    # Longest audio actually fed to the model; longer input is centre-cropped.
    max_duration_s: float = 30.0

    # Hard cap on the request body, guarding against a runaway upload.
    max_upload_bytes: int = 16 * 1024 * 1024

    # Load a deterministic fake model instead of ECAPA. Used by the contract
    # tests so CI never downloads ~80 MB of weights.
    stub: bool = False


settings = Settings()
