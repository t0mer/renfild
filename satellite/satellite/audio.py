"""Audio capture, buffering and playback.

``sounddevice`` (PortAudio) is imported lazily so the pure-logic parts of this
module — the ring buffer and the WAV helpers — stay importable on a machine with
no sound card, which is what the unit tests run on.
"""

from __future__ import annotations

import io
import queue
import threading
import wave
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import numpy as np
from loguru import logger


class AudioError(RuntimeError):
    """Raised when a device cannot be opened or a stream dies."""


# --------------------------------------------------------------------------- #
# WAV helpers
# --------------------------------------------------------------------------- #


def wav_encode(samples: np.ndarray, sample_rate: int) -> bytes:
    """Encode mono ``samples`` (int16 or float32 in [-1, 1]) as a PCM WAV blob."""
    pcm = to_int16(samples)
    buf = io.BytesIO()
    with wave.open(buf, "wb") as out:
        out.setnchannels(1)
        out.setsampwidth(2)
        out.setframerate(sample_rate)
        out.writeframes(pcm.tobytes())
    return buf.getvalue()


def wav_decode(data: bytes) -> tuple[np.ndarray, int]:
    """Decode a PCM WAV blob into float32 mono samples plus its sample rate."""
    with wave.open(io.BytesIO(data), "rb") as src:
        channels = src.getnchannels()
        width = src.getsampwidth()
        rate = src.getframerate()
        frames = src.readframes(src.getnframes())
    if width != 2:
        raise AudioError(f"unsupported sample width: {width * 8} bit")
    samples = np.frombuffer(frames, dtype=np.int16).astype(np.float32) / 32768.0
    if channels > 1:
        samples = samples.reshape(-1, channels).mean(axis=1)
    return samples, rate


def to_int16(samples: np.ndarray) -> np.ndarray:
    """Convert float samples to int16, leaving int16 input untouched."""
    if samples.dtype == np.int16:
        return samples
    clipped = np.clip(samples.astype(np.float32), -1.0, 1.0)
    return (clipped * 32767.0).astype(np.int16)


def to_float32(samples: np.ndarray) -> np.ndarray:
    """Convert int16 samples to float32 in [-1, 1], leaving floats untouched."""
    if samples.dtype == np.float32:
        return samples
    return samples.astype(np.float32) / 32768.0


def resample(samples: np.ndarray, src_sr: int, dst_sr: int) -> np.ndarray:
    """Resample float32 audio, preferring soxr and falling back to interpolation."""
    if src_sr == dst_sr:
        return samples.astype(np.float32, copy=False)
    try:
        import soxr

        return soxr.resample(samples, src_sr, dst_sr).astype(np.float32, copy=False)
    except ImportError:
        target_len = max(1, int(round(len(samples) * dst_sr / src_sr)))
        src_idx = np.arange(len(samples), dtype=np.float64)
        dst_idx = np.linspace(0.0, len(samples) - 1, num=target_len, dtype=np.float64)
        return np.interp(dst_idx, src_idx, samples).astype(np.float32, copy=False)


# --------------------------------------------------------------------------- #
# Ring buffer
# --------------------------------------------------------------------------- #


class RingBuffer:
    """Fixed-length circular buffer of the most recent audio.

    The satellite keeps the last two seconds at all times so that when the wake
    word fires, the audio *containing* the wake word is still available — that
    snapshot is what speaker identification runs on.
    """

    def __init__(self, capacity: int, dtype: Any = np.int16) -> None:
        if capacity <= 0:
            raise ValueError("capacity must be positive")
        self._buf = np.zeros(capacity, dtype=dtype)
        self._capacity = capacity
        self._write = 0
        self._filled = 0
        self._lock = threading.Lock()

    @property
    def capacity(self) -> int:
        return self._capacity

    def __len__(self) -> int:
        return self._filled

    def extend(self, frame: np.ndarray) -> None:
        """Append a frame, discarding whatever falls off the far end."""
        data = np.asarray(frame, dtype=self._buf.dtype).reshape(-1)
        if len(data) >= self._capacity:
            data = data[-self._capacity :]
        with self._lock:
            end = self._write + len(data)
            if end <= self._capacity:
                self._buf[self._write : end] = data
            else:
                split = self._capacity - self._write
                self._buf[self._write :] = data[:split]
                self._buf[: end - self._capacity] = data[split:]
            self._write = end % self._capacity
            self._filled = min(self._capacity, self._filled + len(data))

    def snapshot(self) -> np.ndarray:
        """Return the buffered audio oldest-first, as a copy."""
        with self._lock:
            if self._filled < self._capacity:
                return self._buf[: self._filled].copy()
            return np.concatenate((self._buf[self._write :], self._buf[: self._write]))

    def clear(self) -> None:
        with self._lock:
            self._buf.fill(0)
            self._write = 0
            self._filled = 0


# --------------------------------------------------------------------------- #
# Devices
# --------------------------------------------------------------------------- #


def _sd():
    """Import sounddevice on demand, with an actionable error if it is missing."""
    try:
        import sounddevice

        return sounddevice
    except OSError as exc:  # PortAudio shared library missing
        raise AudioError(
            "PortAudio is unavailable — install libportaudio2 (apt install libportaudio2)"
        ) from exc
    except ImportError as exc:
        raise AudioError("sounddevice is not installed in this environment") from exc


def find_device(name: str, kind: str) -> int | None:
    """Resolve a device index from a case-insensitive name substring.

    ``kind`` is ``"input"`` or ``"output"``. Returns ``None`` for an empty name,
    meaning "use the system default".
    """
    if not name:
        return None
    sd = _sd()
    channels_key = "max_input_channels" if kind == "input" else "max_output_channels"
    needle = name.casefold()
    matches = [
        (index, dev)
        for index, dev in enumerate(sd.query_devices())
        if dev[channels_key] > 0 and needle in dev["name"].casefold()
    ]
    if not matches:
        available = ", ".join(
            dev["name"] for dev in sd.query_devices() if dev[channels_key] > 0
        )
        raise AudioError(f"no {kind} device matching {name!r}. Available: {available}")
    index, dev = matches[0]
    logger.info("{} device: [{}] {}", kind, index, dev["name"])
    return index


class Microphone:
    """Blocking frame source over a PortAudio input stream.

    Frames are pushed by PortAudio's callback thread into a bounded queue; the
    main loop pulls them out. A bounded queue means that if the pipeline stalls
    we drop old audio instead of growing without limit.
    """

    def __init__(
        self,
        sample_rate: int,
        frame_samples: int,
        device: int | None = None,
        gain: float = 1.0,
        max_queued_frames: int = 64,
    ) -> None:
        self.sample_rate = sample_rate
        self.frame_samples = frame_samples
        self.device = device
        self.gain = gain
        self._queue: queue.Queue[np.ndarray] = queue.Queue(maxsize=max_queued_frames)
        self._stream = None
        self._dropped = 0

    def _callback(self, indata, _frames, _time, status) -> None:
        if status:
            logger.warning("input stream status: {}", status)
        frame = np.frombuffer(bytes(indata), dtype=np.int16).copy()
        try:
            self._queue.put_nowait(frame)
        except queue.Full:
            self._dropped += 1
            if self._dropped % 50 == 1:
                logger.warning("input queue full — dropped {} frames", self._dropped)

    def __enter__(self) -> Microphone:
        sd = _sd()
        self._stream = sd.RawInputStream(
            samplerate=self.sample_rate,
            blocksize=self.frame_samples,
            device=self.device,
            channels=1,
            dtype="int16",
            callback=self._callback,
        )
        self._stream.start()
        logger.info(
            "microphone open: {} Hz, {} sample frames", self.sample_rate, self.frame_samples
        )
        return self

    def __exit__(self, *_exc) -> None:
        self.close()

    def close(self) -> None:
        if self._stream is not None:
            self._stream.stop()
            self._stream.close()
            self._stream = None

    def frames(self) -> Iterator[np.ndarray]:
        """Yield int16 frames forever, applying the configured input gain."""
        while True:
            frame = self._queue.get()
            if self.gain != 1.0:
                frame = to_int16(to_float32(frame) * self.gain)
            yield frame

    def flush(self) -> None:
        """Drop everything captured so far (used after playback)."""
        while True:
            try:
                self._queue.get_nowait()
            except queue.Empty:
                return


class FileMicrophone:
    """Replays a WAV file as if it were the microphone.

    This is how the wake word and the capture loop get tested on a machine with
    no microphone attached — a headless Pi, or CI. After the file runs out it
    emits a little trailing silence (so the VAD can close the command) and then
    stops, which ends the daemon's loop.
    """

    def __init__(
        self,
        path: Path,
        sample_rate: int,
        frame_samples: int,
        tail_silence_s: float = 3.0,
        gain: float = 1.0,
    ) -> None:
        self.path = Path(path)
        self.sample_rate = sample_rate
        self.frame_samples = frame_samples
        self.tail_silence_s = tail_silence_s
        self.gain = gain
        self._pcm = np.zeros(0, dtype=np.int16)

    def __enter__(self) -> FileMicrophone:
        samples, rate = wav_decode(self.path.read_bytes())
        if rate != self.sample_rate:
            logger.info("resampling {} from {} Hz to {} Hz", self.path.name, rate, self.sample_rate)
            samples = resample(samples, rate, self.sample_rate)
        if self.gain != 1.0:
            samples = samples * self.gain
        self._pcm = to_int16(samples)
        logger.info(
            "replaying {} ({:.2f}s) as microphone input",
            self.path.name,
            len(self._pcm) / self.sample_rate,
        )
        return self

    def __exit__(self, *_exc) -> None:
        self.close()

    def close(self) -> None:
        self._pcm = np.zeros(0, dtype=np.int16)

    def frames(self) -> Iterator[np.ndarray]:
        """Yield the file's frames, then trailing silence, then stop."""
        for start in range(0, len(self._pcm) - self.frame_samples + 1, self.frame_samples):
            yield self._pcm[start : start + self.frame_samples]
        for _ in range(int(self.tail_silence_s * self.sample_rate / self.frame_samples)):
            yield np.zeros(self.frame_samples, dtype=np.int16)

    def flush(self) -> None:
        """Nothing to drop: a file has no live queue."""


class NullSpeaker:
    """Swallows playback. Used with FileMicrophone so a test needs no soundcard."""

    def play_wav(self, data: bytes) -> None:
        logger.info("playback suppressed ({} bytes of audio)", len(data))

    def play(self, samples: np.ndarray, sample_rate: int) -> None:
        logger.debug("playback suppressed ({:.2f}s)", len(samples) / sample_rate)


class Speaker:
    """Playback of WAV blobs on the configured output device."""

    def __init__(self, device: int | None = None, volume: float = 1.0) -> None:
        self.device = device
        self.volume = volume

    def play_wav(self, data: bytes) -> None:
        """Decode and play a WAV blob, blocking until playback finishes."""
        samples, rate = wav_decode(data)
        self.play(samples, rate)

    def play(self, samples: np.ndarray, sample_rate: int) -> None:
        """Play float32 mono ``samples``, resampling if the device refuses the rate."""
        sd = _sd()
        audio = to_float32(samples)
        if self.volume != 1.0:
            audio = np.clip(audio * self.volume, -1.0, 1.0)
        try:
            sd.play(audio, samplerate=sample_rate, device=self.device, blocking=True)
            return
        except Exception as exc:  # noqa: BLE001 — PortAudio raises several types
            logger.warning("playback at {} Hz failed ({}), resampling to 48 kHz", sample_rate, exc)
        fallback = resample(audio, sample_rate, 48_000)
        sd.play(fallback, samplerate=48_000, device=self.device, blocking=True)
