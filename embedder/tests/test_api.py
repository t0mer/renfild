"""Contract tests for the embedder HTTP API.

These run against the stub model (``RENFILD_EMB_STUB``) so CI never downloads
the real ECAPA weights.
"""

from __future__ import annotations

import math

from .conftest import wav_bytes


def test_healthz_reports_loaded_stub(client):
    response = client.get("/healthz")
    assert response.status_code == 200
    body = response.json()
    assert body["status"] == "ok"
    assert body["model_loaded"] is True
    assert body["stub"] is True


def test_embed_returns_192_unit_vector(client):
    response = client.post(
        "/embed", content=wav_bytes(2.0), headers={"Content-Type": "audio/wav"}
    )
    assert response.status_code == 200
    body = response.json()
    assert body["dims"] == 192
    assert len(body["embedding"]) == 192
    assert body["duration_s"] == 2.0
    norm = math.sqrt(sum(x * x for x in body["embedding"]))
    assert math.isclose(norm, 1.0, rel_tol=1e-5)


def test_embed_is_deterministic(client):
    payload = wav_bytes(1.5)
    first = client.post("/embed", content=payload).json()["embedding"]
    second = client.post("/embed", content=payload).json()["embedding"]
    assert first == second


def test_similar_audio_scores_higher_than_dissimilar(client):
    def embed(seconds: float, freq: float) -> list[float]:
        return client.post("/embed", content=wav_bytes(seconds, freq)).json()["embedding"]

    def cosine(a: list[float], b: list[float]) -> float:
        return sum(x * y for x, y in zip(a, b, strict=True))

    base = embed(2.0, 220.0)
    same = embed(2.0, 221.0)
    other = embed(2.0, 900.0)
    assert cosine(base, same) > cosine(base, other)


def test_short_audio_is_rejected(client):
    response = client.post("/embed", content=wav_bytes(0.25))
    assert response.status_code == 422
    assert "too short" in response.json()["detail"]


def test_garbage_body_is_rejected(client):
    response = client.post("/embed", content=b"definitely not a wav file")
    assert response.status_code == 422


def test_empty_body_is_rejected(client):
    response = client.post("/embed", content=b"")
    assert response.status_code == 422


def test_oversized_body_is_rejected(client):
    from fastapi.testclient import TestClient

    from embedder.app import create_app
    from embedder.config import Settings

    with TestClient(create_app(Settings(stub=True, max_upload_bytes=1024))) as small:
        response = small.post("/embed", content=wav_bytes(2.0))
    assert response.status_code == 413
