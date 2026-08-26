"""openWakeWord integration.

The satellite feeds every 80 ms frame through the wake model and reports the
first score that crosses the configured threshold. A debounce window keeps a
single spoken wake word from firing several detections in a row.
"""

from __future__ import annotations

import sys
import time
import types
from dataclasses import dataclass
from pathlib import Path

import numpy as np
from loguru import logger


def _install_tflite_shim() -> None:
    """Let openWakeWord's TFLite path work on Pythons with no tflite-runtime.

    ``tflite-runtime`` stops at CPython 3.11; Google's replacement package,
    ``ai-edge-litert``, ships the same Interpreter API for newer versions.
    openWakeWord imports ``tflite_runtime.interpreter`` by name, so we alias it.
    """
    try:
        import tflite_runtime.interpreter  # noqa: F401

        return
    except ImportError:
        pass
    try:
        from ai_edge_litert import interpreter as litert
    except ImportError:
        return

    package = types.ModuleType("tflite_runtime")
    module = types.ModuleType("tflite_runtime.interpreter")
    module.Interpreter = litert.Interpreter
    package.interpreter = module
    sys.modules.setdefault("tflite_runtime", package)
    sys.modules.setdefault("tflite_runtime.interpreter", module)
    logger.debug("tflite_runtime shimmed onto ai-edge-litert")


@dataclass(frozen=True)
class Detection:
    """A wake word firing."""

    name: str
    score: float
    at: float


class WakeWord:
    """Thin wrapper over ``openwakeword.model.Model``."""

    def __init__(
        self,
        model_path: Path,
        threshold: float = 0.6,
        debounce_s: float = 3.0,
        framework: str = "auto",
        resources_dir: Path | None = None,
    ) -> None:
        self.model_path = Path(model_path)
        self.threshold = threshold
        self.debounce_s = debounce_s
        self.framework = framework
        self.resources_dir = resources_dir
        self._model = None
        self._muted_until = 0.0

    def resolve_framework(self) -> str:
        """Pick the inference backend, defaulting to the model file's extension."""
        if self.framework != "auto":
            return self.framework
        return "tflite" if self.model_path.suffix.lower() == ".tflite" else "onnx"

    def load(self) -> None:
        if not self.model_path.is_file():
            raise FileNotFoundError(
                f"wake word model not found: {self.model_path}. "
                "Train one with the openWakeWord notebook and drop it in satellite/models/."
            )
        framework = self.resolve_framework()
        if framework == "tflite":
            _install_tflite_shim()

        from openwakeword.model import Model

        kwargs = {
            "wakeword_models": [str(self.model_path)],
            "inference_framework": framework,
        }
        if self.resources_dir is not None:
            # openWakeWord looks for its shared feature-extraction models here;
            # pointing at them explicitly keeps the satellite fully offline.
            resources = self.resources_dir
            kwargs["melspec_model_path"] = str(resources / f"melspectrogram.{framework}")
            kwargs["embedding_model_path"] = str(resources / f"embedding_model.{framework}")
        self._model = Model(**kwargs)
        logger.info(
            "wake word loaded: {} ({}, threshold {})",
            self.model_path.name,
            framework,
            self.threshold,
        )

    def process(self, frame: np.ndarray) -> Detection | None:
        """Score one int16 frame; return a Detection when it crosses threshold."""
        if self._model is None:
            raise RuntimeError("wake model not loaded")
        now = time.monotonic()
        scores = self._model.predict(np.asarray(frame, dtype=np.int16))
        if now < self._muted_until:
            return None
        name, score = max(scores.items(), key=lambda item: item[1])
        if score < self.threshold:
            return None
        self._muted_until = now + self.debounce_s
        return Detection(name=name, score=float(score), at=now)

    def mute_for(self, seconds: float) -> None:
        """Suppress detections, e.g. while our own reply is being played back."""
        self._muted_until = max(self._muted_until, time.monotonic() + seconds)

    def reset(self) -> None:
        """Clear the model's internal prediction buffer after a detection."""
        if self._model is not None:
            self._model.reset()
