"""Entrypoint: ``python -m embedder``."""

from __future__ import annotations

import uvicorn

from .app import create_app
from .config import settings
from .logging import configure


def main() -> None:
    configure(settings.log_level)
    uvicorn.run(
        create_app(settings),
        host=settings.host,
        port=settings.port,
        log_config=None,
        access_log=False,
    )


if __name__ == "__main__":
    main()
