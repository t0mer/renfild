"""HTTP client for the Renfild server."""

from __future__ import annotations

from dataclasses import dataclass
from urllib.parse import unquote

import httpx
from loguru import logger


class ServerError(RuntimeError):
    """The server could not be reached, or answered with something unusable."""


@dataclass
class UtteranceReply:
    """What came back from ``POST /api/v1/utterance``."""

    audio: bytes | None
    speaker: str
    transcript: str
    intent: str

    @property
    def has_audio(self) -> bool:
        return bool(self.audio)


class ServerClient:
    """Posts wake+command audio and returns the spoken reply.

    Every failure mode is converted into ``ServerError`` — the daemon reacts by
    playing an error chirp and going back to listening, never by dying.
    """

    def __init__(self, base_url: str, satellite_id: str, timeout_s: float = 15.0, retries: int = 1):
        self.base_url = base_url.rstrip("/")
        self.satellite_id = satellite_id
        self.timeout_s = timeout_s
        self.retries = max(0, retries)
        self._client = httpx.Client(timeout=timeout_s, follow_redirects=False)

    def close(self) -> None:
        self._client.close()

    def __enter__(self) -> ServerClient:
        return self

    def __exit__(self, *_exc) -> None:
        self.close()

    def health(self) -> bool:
        """True when the server answers /healthz. Used once at startup."""
        try:
            response = self._client.get(f"{self.base_url}/healthz", timeout=3.0)
            return response.status_code == 200
        except httpx.HTTPError:
            return False

    def utterance(self, wake_wav: bytes, command_wav: bytes) -> UtteranceReply:
        """Submit one utterance and return the reply audio plus its metadata."""
        files = {
            "wake": ("wake.wav", wake_wav, "audio/wav"),
            "command": ("command.wav", command_wav, "audio/wav"),
        }
        data = {"satellite_id": self.satellite_id}
        url = f"{self.base_url}/api/v1/utterance"

        last_error: Exception | None = None
        for attempt in range(self.retries + 1):
            try:
                response = self._client.post(url, files=files, data=data)
            except httpx.HTTPError as exc:
                last_error = exc
                logger.warning("utterance POST failed (attempt {}): {}", attempt + 1, exc)
                continue

            if response.status_code == 204:
                return self._reply(response, audio=None)
            if response.status_code == 200:
                return self._reply(response, audio=response.content)
            raise ServerError(f"server returned {response.status_code}: {response.text[:200]}")

        raise ServerError(f"server unreachable: {last_error}")

    @staticmethod
    def _reply(response: httpx.Response, audio: bytes | None) -> UtteranceReply:
        # HTTP headers are latin-1, so the server percent-encodes UTF-8 values —
        # Hebrew transcripts would be unrepresentable otherwise.
        def header(name: str) -> str:
            return unquote(response.headers.get(name, ""))

        return UtteranceReply(
            audio=audio,
            speaker=header("X-Speaker"),
            transcript=header("X-Transcript"),
            intent=header("X-Intent"),
        )
