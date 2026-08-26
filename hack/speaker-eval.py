#!/usr/bin/env python3
"""Measure how well speaker recognition actually separates voices.

There is no recording of a real household in this repository, so the corpus is
synthesised: each Piper voice stands in for a person. That is not a substitute
for enrolling your own family — synthetic voices are cleaner and more
consistent than real ones, so the scores here are an optimistic ceiling — but it
does exercise the real enrollment path end to end and shows where the threshold
sits relative to the gap between speakers.

    satellite/.venv/bin/python hack/speaker-eval.py \
        --voices /opt/renfild/piper/voices/*.onnx

Run it with the satellite's interpreter: it needs numpy (and soxr, for decent
resampling), both of which live in that virtualenv. It also needs a running
server with a real — not stubbed — embedder behind it.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
import uuid
import wave
from dataclasses import dataclass
from pathlib import Path

# Enrollment mirrors the wizard: the wake word twice, two fixed sentences and
# one free sentence.
ENROLLMENT_PROMPTS = [
    ("wake-1", "Hey Jarvis."),
    ("wake-2", "Hey Jarvis."),
    ("sentence-1", "Turn on the lights in the living room, please."),
    ("sentence-2", "What is the weather going to be like tomorrow?"),
    ("free", "I think I left my keys on the kitchen table this morning."),
]

# Held-out clips, split by length: identification at runtime runs on the two
# second wake snapshot, which is the hard case.
TEST_CLIPS = [
    ("wake", "Hey Jarvis."),
    ("wake", "Hey Jarvis, are you there?"),
    ("command", "Turn off the lights and lock the front door."),
    ("command", "Play something quiet in the kitchen for half an hour."),
]


@dataclass
class Clip:
    label: str
    kind: str
    path: Path


def api(method: str, url: str, payload: dict | None = None) -> dict:
    data = json.dumps(payload).encode() if payload is not None else None
    request = urllib.request.Request(url, data=data, method=method)
    if data:
        request.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(request, timeout=60) as response:
        body = response.read()
    return json.loads(body) if body else {}


def upload(url: str, audio: Path, fields: dict[str, str]) -> dict:
    """POST a WAV as multipart/form-data without pulling in a dependency."""
    boundary = uuid.uuid4().hex
    parts: list[bytes] = []
    for key, value in fields.items():
        parts.append(
            f'--{boundary}\r\nContent-Disposition: form-data; name="{key}"\r\n\r\n{value}\r\n'.encode()
        )
    parts.append(
        f'--{boundary}\r\nContent-Disposition: form-data; name="audio"; '
        f'filename="{audio.name}"\r\nContent-Type: audio/wav\r\n\r\n'.encode()
    )
    parts.append(audio.read_bytes())
    parts.append(f"\r\n--{boundary}--\r\n".encode())

    request = urllib.request.Request(url, data=b"".join(parts), method="POST")
    request.add_header("Content-Type", f"multipart/form-data; boundary={boundary}")
    with urllib.request.urlopen(request, timeout=120) as response:
        return json.loads(response.read())


def synthesize(piper: Path, voice: Path, text: str, target: Path) -> float:
    """Speak text with Piper and write it as 16 kHz mono, returning its length."""
    subprocess.run(
        [str(piper), "--model", str(voice), "--output_file", str(target.with_suffix(".raw.wav"))],
        input=text.encode(),
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    raw = target.with_suffix(".raw.wav")
    with wave.open(str(raw), "rb") as source:
        rate = source.getframerate()
        frames = source.readframes(source.getnframes())

    import numpy as np

    samples = np.frombuffer(frames, dtype=np.int16).astype(np.float32) / 32768.0
    if rate != 16_000:
        try:
            import soxr

            samples = soxr.resample(samples, rate, 16_000)
        except ImportError:
            length = int(len(samples) * 16_000 / rate)
            samples = np.interp(
                np.linspace(0, len(samples) - 1, length),
                np.arange(len(samples)),
                samples,
            ).astype(np.float32)

    pcm = np.clip(samples, -1, 1)
    with wave.open(str(target), "wb") as out:
        out.setnchannels(1)
        out.setsampwidth(2)
        out.setframerate(16_000)
        out.writeframes((pcm * 32767).astype(np.int16).tobytes())
    raw.unlink(missing_ok=True)
    return len(pcm) / 16_000


def build_corpus(piper: Path, voice: Path, work: Path) -> tuple[list[Clip], list[Clip]]:
    work.mkdir(parents=True, exist_ok=True)
    enrollment = []
    for index, (label, text) in enumerate(ENROLLMENT_PROMPTS):
        path = work / f"enroll-{index}-{label}.wav"
        synthesize(piper, voice, text, path)
        enrollment.append(Clip(label, "enroll", path))

    tests = []
    for index, (kind, text) in enumerate(TEST_CLIPS):
        path = work / f"test-{index}-{kind}.wav"
        length = synthesize(piper, voice, text, path)
        tests.append(Clip(f"{kind}-{index} ({length:.1f}s)", kind, path))
    return enrollment, tests


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", default="http://127.0.0.1:8080")
    parser.add_argument("--piper", type=Path, default=Path("/opt/renfild/piper/piper"))
    parser.add_argument(
        "--voices",
        type=Path,
        nargs="+",
        required=True,
        help="Piper voice models; the last one is treated as an unenrolled stranger",
    )
    parser.add_argument("--work", type=Path, help="where to keep the generated audio")
    parser.add_argument("--keep", action="store_true", help="do not delete the speakers afterwards")
    args = parser.parse_args()

    if not args.piper.is_file():
        print(f"piper not found at {args.piper}", file=sys.stderr)
        return 1
    for voice in args.voices:
        if not voice.is_file():
            print(f"voice not found at {voice}", file=sys.stderr)
            return 1
    if len(args.voices) < 2:
        print("need at least two voices: one to enroll, one stranger", file=sys.stderr)
        return 1

    work = args.work or Path(tempfile.mkdtemp(prefix="renfild-eval-"))
    enrolled_voices, stranger = args.voices[:-1], args.voices[-1]

    try:
        settings = api("GET", f"{args.server}/api/ui/settings")
    except urllib.error.URLError as error:
        print(f"cannot reach the server at {args.server}: {error}", file=sys.stderr)
        return 1
    threshold = settings["runtime"]["speaker_threshold"]
    print(f"server threshold: {threshold}\ncorpus: {work}\n")

    created: list[int] = []
    tests: dict[str, list[Clip]] = {}

    for voice in enrolled_voices:
        name = voice.stem
        print(f"== {name}")
        enrollment, held_out = build_corpus(args.piper, voice, work / name)
        tests[name] = held_out

        speaker = api("POST", f"{args.server}/api/ui/speakers", {"name": name, "role": "member"})
        created.append(speaker["id"])

        for clip in enrollment:
            result = upload(
                f"{args.server}/api/ui/speakers/{speaker['id']}/enrollments",
                clip.path,
                {"label": clip.label},
            )
            verdict = "kept" if result["accepted"] else f"REJECTED ({result.get('reason', '')})"
            print(f"   {clip.label:12s} {result['duration_s']:.1f}s  similarity {result['similarity']:.3f}  {verdict}")
        print()

    stranger_name = stranger.stem
    _, stranger_clips = build_corpus(args.piper, stranger, work / stranger_name)
    tests[f"{stranger_name} (not enrolled)"] = stranger_clips

    names = [voice.stem for voice in enrolled_voices]
    header = "".join(f"{name[:12]:>14s}" for name in names)
    print(f"{'clip':38s}{header}{'verdict':>26s}")
    print("-" * (38 + 14 * len(names) + 26))

    correct = wrong = 0
    margins: list[float] = []

    for speaker_name, clips in tests.items():
        expected = speaker_name.split(" ")[0]
        for clip in clips:
            result = upload(f"{args.server}/api/ui/speakers/identify", clip.path, {})
            scores = result["scores"]
            identity = result["identity"]

            row = "".join(f"{scores.get(name, 0):>14.3f}" for name in names)
            ranked = sorted(scores.values(), reverse=True)
            if len(ranked) > 1:
                margins.append(ranked[0] - ranked[1])

            enrolled = "not enrolled" not in speaker_name
            got = identity["name"] if identity["known"] else "unknown"
            if enrolled:
                ok = got == expected
            else:
                ok = got == "unknown"
            correct, wrong = (correct + 1, wrong) if ok else (correct, wrong + 1)

            verdict = f"{'OK ' if ok else 'MISS'} -> {got}"
            print(f"{expected + ' ' + clip.label:38s}{row}{verdict:>26s}")

    total = correct + wrong
    print(f"\n{correct}/{total} correct", end="")
    if margins:
        margins.sort()
        print(
            f" · best-vs-runner-up margin: min {margins[0]:.3f}, "
            f"median {margins[len(margins) // 2]:.3f}, max {margins[-1]:.3f}"
        )
    else:
        print()

    if not args.keep:
        for speaker_id in created:
            api("DELETE", f"{args.server}/api/ui/speakers/{speaker_id}")
        print("cleaned up the speakers it created")

    return 0 if wrong == 0 else 2


if __name__ == "__main__":
    sys.exit(main())
