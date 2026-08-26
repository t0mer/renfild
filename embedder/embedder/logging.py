"""Loguru setup shared by the embedder entrypoints."""

from __future__ import annotations

import sys

from loguru import logger


def configure(level: str) -> None:
    """Reconfigure loguru to a single stderr sink at ``level``."""
    logger.remove()
    logger.add(
        sys.stderr,
        level=level.upper(),
        format=(
            "<green>{time:YYYY-MM-DD HH:mm:ss.SSS}</green> | "
            "<level>{level: <8}</level> | "
            "<cyan>{name}</cyan>:<cyan>{function}</cyan> - <level>{message}</level>"
        ),
        backtrace=False,
        diagnose=False,
    )
