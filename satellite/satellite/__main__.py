"""Satellite daemon entrypoint: ``python -m satellite``.

Listens forever. Wake word → chirp → record command → POST to the server →
play the reply. Network and audio failures are logged and swallowed; the loop
is the one thing that must never stop.
"""

from __future__ import annotations

import argparse
import signal
import sys
import time
from datetime import datetime
from pathlib import Path

from loguru import logger

from . import __version__, chimes, config, vad
from .audio import AudioError, Microphone, RingBuffer, Speaker, find_device, wav_encode
from .client import ServerClient, ServerError
from .wake import WakeWord


def _configure_logging(level: str) -> None:
    logger.remove()
    logger.add(
        sys.stderr,
        level=level.upper(),
        format=(
            "<green>{time:HH:mm:ss.SSS}</green> | <level>{level: <7}</level> | "
            "<level>{message}</level>"
        ),
        backtrace=False,
        diagnose=False,
    )


def _parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(prog="satellite", description="Renfild voice satellite")
    parser.add_argument("--config", type=Path, help="path to config.yaml")
    parser.add_argument("--log-level", help="debug / info / warning / error")
    parser.add_argument(
        "--offline",
        action="store_true",
        help="capture and dump audio without contacting the server",
    )
    parser.add_argument("--dump-dir", type=Path, help="write wake.wav/command.wav here")
    parser.add_argument(
        "--list-devices", action="store_true", help="print audio devices and exit"
    )
    parser.add_argument("--version", action="version", version=f"renfild-satellite {__version__}")
    return parser.parse_args(argv)


def _list_devices() -> int:
    try:
        import sounddevice as sd
    except Exception as exc:  # noqa: BLE001
        print(f"cannot query devices: {exc}", file=sys.stderr)
        return 1
    for index, device in enumerate(sd.query_devices()):
        kinds = []
        if device["max_input_channels"]:
            kinds.append("in")
        if device["max_output_channels"]:
            kinds.append("out")
        print(f"[{index:2d}] {device['name']}  ({'/'.join(kinds) or 'none'})")
    return 0


class Satellite:
    """Wires the pieces together and runs the listen loop."""

    def __init__(self, settings: config.Settings) -> None:
        self.settings = settings
        self.running = True
        self.ring = RingBuffer(int(settings.audio.ring_seconds * settings.audio.sample_rate))
        self.wake = WakeWord(
            model_path=settings.wake.model_path,
            threshold=settings.wake.threshold,
            debounce_s=settings.wake.debounce_s,
            framework=settings.wake.inference_framework,
            resources_dir=settings.wake.resources_dir,
        )
        self.recorder = vad.CommandRecorder(
            detector=vad.build_detector(settings.vad.model_path, settings.vad.fallback_rms),
            sample_rate=settings.audio.sample_rate,
            threshold=settings.vad.threshold,
            trailing_silence_ms=settings.vad.trailing_silence_ms,
            max_command_s=settings.vad.max_command_s,
            start_timeout_s=settings.vad.start_timeout_s,
        )
        self.speaker: Speaker | None = None
        self.chimes: chimes.ChimePlayer | None = None
        self.client: ServerClient | None = None

    def stop(self, *_args) -> None:
        logger.info("shutting down")
        self.running = False

    def run(self) -> int:
        settings = self.settings
        self.wake.load()

        input_device = find_device(settings.audio.input_device, "input")
        output_device = find_device(settings.audio.output_device, "output")
        self.speaker = Speaker(device=output_device, volume=settings.audio.output_volume)
        self.chimes = chimes.ChimePlayer(
            self.speaker,
            enabled=settings.chimes.enabled,
            volume=settings.chimes.volume,
            overrides={
                "listening": settings.chimes.listening,
                "ack": settings.chimes.ack,
                "error": settings.chimes.error,
            },
        )

        if not settings.offline:
            self.client = ServerClient(
                base_url=settings.server.url,
                satellite_id=settings.satellite_id,
                timeout_s=settings.server.timeout_s,
                retries=settings.server.retries,
            )
            if self.client.health():
                logger.info("server reachable at {}", settings.server.url)
            else:
                logger.warning(
                    "server at {} is not answering /healthz — will keep trying per utterance",
                    settings.server.url,
                )

        with Microphone(
            sample_rate=settings.audio.sample_rate,
            frame_samples=settings.frame_samples,
            device=input_device,
            gain=settings.audio.input_gain,
        ) as mic:
            logger.info(
                "listening as satellite '{}' (wake threshold {})",
                settings.satellite_id,
                settings.wake.threshold,
            )
            frames = mic.frames()
            while self.running:
                frame = next(frames)
                self.ring.extend(frame)
                detection = self.wake.process(frame)
                if detection is None:
                    continue
                logger.info("wake: {} ({:.2f})", detection.name, detection.score)
                self._handle_detection(mic, frames)

        if self.client is not None:
            self.client.close()
        return 0

    def _handle_detection(self, mic: Microphone, frames) -> None:
        """Everything that happens between the wake word and the reply."""
        wake_samples = self.ring.snapshot()
        started = time.monotonic()

        # Instant local feedback, before any network round trip.
        self.chimes.play("listening")
        # Drop whatever the microphone picked up of our own chirp.
        mic.flush()

        command = self.recorder.record(frames)
        logger.info(
            "command: {:.1f}s ({}), speech={}",
            command.duration_s,
            command.reason,
            command.speech_detected,
        )

        sample_rate = self.settings.audio.sample_rate
        wake_wav = wav_encode(wake_samples, sample_rate)
        command_wav = wav_encode(command.samples, sample_rate)
        self._dump(wake_wav, command_wav)

        if not command.speech_detected:
            logger.info("no speech after wake word — treating as a false trigger")
            self._recover(mic)
            return

        if self.settings.offline or self.client is None:
            logger.info("offline mode — not contacting the server")
            self._recover(mic)
            return

        try:
            reply = self.client.utterance(wake_wav, command_wav)
        except ServerError as exc:
            logger.error("utterance failed: {}", exc)
            self.chimes.play("error")
            self._recover(mic)
            return

        logger.info(
            "reply: speaker={} intent={} transcript={!r}",
            reply.speaker or "?",
            reply.intent or "-",
            reply.transcript,
        )
        try:
            if reply.has_audio:
                self.speaker.play_wav(reply.audio)
            else:
                self.chimes.play("ack")
        except Exception as exc:  # noqa: BLE001 — playback must not kill the loop
            logger.error("playback failed: {}", exc)

        logger.info("round trip {:.2f}s", time.monotonic() - started)
        self._recover(mic)

    def _recover(self, mic: Microphone) -> None:
        """Return to a clean listening state after handling a detection."""
        self.wake.reset()
        self.ring.clear()
        mic.flush()

    def _dump(self, wake_wav: bytes, command_wav: bytes) -> None:
        directory = self.settings.dump_dir
        if directory is None:
            return
        directory = Path(directory)
        directory.mkdir(parents=True, exist_ok=True)
        stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
        (directory / f"{stamp}-wake.wav").write_bytes(wake_wav)
        (directory / f"{stamp}-command.wav").write_bytes(command_wav)
        logger.info("dumped {}-wake.wav / {}-command.wav to {}", stamp, stamp, directory)


def main(argv: list[str] | None = None) -> int:
    args = _parse_args(argv)
    if args.list_devices:
        return _list_devices()

    settings = config.load(args.config)
    if args.log_level:
        settings.log_level = args.log_level
    if args.offline:
        settings.offline = True
    if args.dump_dir:
        settings.dump_dir = args.dump_dir

    _configure_logging(settings.log_level)
    logger.info("renfild-satellite {}", __version__)

    satellite = Satellite(settings)
    signal.signal(signal.SIGTERM, satellite.stop)
    signal.signal(signal.SIGINT, satellite.stop)

    try:
        return satellite.run()
    except (AudioError, FileNotFoundError) as exc:
        logger.error("{}", exc)
        return 1
    except KeyboardInterrupt:
        return 0


if __name__ == "__main__":
    sys.exit(main())
