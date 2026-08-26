"""FastAPI application exposing the speaker embedder."""

from __future__ import annotations

import time
from contextlib import asynccontextmanager

import anyio
from fastapi import FastAPI, HTTPException, Request, status
from loguru import logger
from pydantic import BaseModel, Field

from . import __version__, audio, model
from .config import Settings
from .config import settings as default_settings


class EmbedResponse(BaseModel):
    """The one thing this service produces."""

    embedding: list[float] = Field(..., description="L2-normalized ECAPA vector")
    duration_s: float = Field(..., description="Duration of the submitted audio")
    dims: int = Field(model.EMBEDDING_DIM, description="Embedding dimensionality")


class HealthResponse(BaseModel):
    status: str
    version: str
    model_loaded: bool
    stub: bool


def create_app(config: Settings | None = None) -> FastAPI:
    """Build the FastAPI app. Kept as a factory so tests can inject settings."""
    cfg = config or default_settings
    encoder = model.build(
        stub=cfg.stub,
        source=cfg.model_source,
        model_dir=cfg.model_dir,
        device=cfg.device,
        threads=cfg.torch_threads,
    )

    @asynccontextmanager
    async def lifespan(_: FastAPI):
        # Loading ECAPA takes a few seconds on a Pi; do it once, at startup, so
        # the first utterance is not the one that pays for it.
        await anyio.to_thread.run_sync(encoder.load)
        yield

    app = FastAPI(
        title="Renfild Embedder",
        version=__version__,
        description="ECAPA-TDNN speaker embeddings for the Renfild voice assistant.",
        docs_url="/api/docs",
        redoc_url=None,
        lifespan=lifespan,
    )
    app.state.settings = cfg
    app.state.encoder = encoder

    @app.get("/healthz", response_model=HealthResponse, tags=["ops"])
    @app.get("/health", response_model=HealthResponse, include_in_schema=False)
    async def healthz() -> HealthResponse:
        return HealthResponse(
            status="ok" if encoder.loaded else "loading",
            version=__version__,
            model_loaded=encoder.loaded,
            stub=cfg.stub,
        )

    @app.post("/embed", response_model=EmbedResponse, tags=["embedding"])
    async def embed(request: Request) -> EmbedResponse:
        """Embed a raw WAV body into a 192-dim speaker vector."""
        if not encoder.loaded:
            raise HTTPException(
                status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
                detail="model still loading",
            )

        body = await _read_body(request, cfg.max_upload_bytes)
        try:
            samples, duration_s = audio.decode(body)
        except audio.DecodeError as exc:
            raise HTTPException(
                status_code=422, detail=str(exc)
            ) from exc

        if duration_s < cfg.min_duration_s:
            raise HTTPException(
                status_code=422,
                detail=f"audio too short: {duration_s:.2f}s < {cfg.min_duration_s}s",
            )

        samples = audio.centre_crop(samples, cfg.max_duration_s)
        started = time.perf_counter()
        try:
            vector = await anyio.to_thread.run_sync(encoder.embed, samples)
        except RuntimeError as exc:
            logger.exception("embedding failed")
            raise HTTPException(
                status_code=status.HTTP_500_INTERNAL_SERVER_ERROR, detail=str(exc)
            ) from exc
        took_ms = (time.perf_counter() - started) * 1000
        logger.debug("embedded {:.2f}s of audio in {:.0f}ms", duration_s, took_ms)

        return EmbedResponse(
            embedding=[float(x) for x in vector],
            duration_s=round(duration_s, 3),
            dims=len(vector),
        )

    return app


async def _read_body(request: Request, limit: int) -> bytes:
    """Read the request body, refusing anything larger than ``limit`` bytes."""
    chunks: list[bytes] = []
    size = 0
    async for chunk in request.stream():
        size += len(chunk)
        if size > limit:
            raise HTTPException(
                status_code=413,
                detail=f"body exceeds {limit} bytes",
            )
        chunks.append(chunk)
    return b"".join(chunks)
