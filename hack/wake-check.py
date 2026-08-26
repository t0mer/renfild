#!/usr/bin/env python3
"""Pick a wake word threshold with evidence instead of guesswork.

Synthesises the wake phrase in as many voices as you point it at, plus a set of
negatives — ordinary household sentences and deliberate near-misses — then
sweeps the threshold and reports how many of each would fire.

    satellite/.venv/bin/python hack/wake-check.py \\
        --model /opt/renfild/satellite/models/hey_renfild.onnx \\
        --phrase "Hey Renfild"

Run it with the satellite's interpreter: it needs numpy and the satellite
package itself. Synthetic speech is cleaner than a room with a television in
it, so treat the suggested threshold as a starting point and confirm it against
your own logs.
"""

from __future__ import annotations

import argparse
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

import numpy as np

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "satellite"))

from satellite.audio import resample, to_int16, wav_decode, wav_encode  # noqa: E402
from satellite.wake import WakeWord  # noqa: E402

SAMPLE_RATE = 16_000
FRAME_SAMPLES = 1280

# Things a household says that must never wake the assistant.
DEFAULT_NEGATIVES = [
    "Turn on the kitchen lights please.",
    "What time is the train tomorrow morning?",
    "I think dinner is nearly ready, can you set the table?",
    "The weather forecast says it will rain all afternoon.",
    "He said he would call back later this evening.",
]

# Speaking rates, as Piper length scales. Higher is slower.
SPEEDS = {"fast": 0.85, "normal": 1.0, "slow": 1.25}


@dataclass
class Clip:
    label: str
    positive: bool
    path: Path
    peak: float = 0.0
    # For negatives: "everyday" household speech, or a deliberate "near-miss".
    kind: str = "positive"


def near_misses(phrase: str) -> list[str]:
    """Phrases that sound like the wake word without being it.

    A sentence that *contains* the wake phrase is a positive, not a false
    alarm — firing on "hey renfild, is that you?" is the whole point — so any
    candidate containing the phrase is discarded.
    """
    words = phrase.split()
    candidates: list[str] = []
    if len(words) > 1:
        # The name on its own, the greeting on its own, and a mangled ending.
        candidates.append(" ".join(words[1:]) + ".")
        candidates.append(words[0] + " there.")
        candidates.append(words[0] + " " + words[-1].rstrip(".!?")[:-1] + "ing.")
    else:
        candidates.append(phrase.rstrip(".!?")[:-1] + "ing.")

    needle = phrase.rstrip(".!?").casefold()
    return [c for c in candidates if needle not in c.casefold()]


def synthesize(piper: Path, voice: Path, text: str, target: Path, length_scale: float) -> None:
    raw = target.with_suffix(".raw.wav")
    subprocess.run(
        [
            str(piper),
            "--model", str(voice),
            "--length_scale", str(length_scale),
            "--output_file", str(raw),
        ],
        input=text.encode(),
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    samples, rate = wav_decode(raw.read_bytes())
    if rate != SAMPLE_RATE:
        samples = resample(samples, rate, SAMPLE_RATE)
    # A little silence at each end, the way a real detection is framed.
    pad = np.zeros(int(0.4 * SAMPLE_RATE), dtype=np.float32)
    target.write_bytes(wav_encode(to_int16(np.concatenate((pad, samples, pad))), SAMPLE_RATE))
    raw.unlink(missing_ok=True)


def peak_score(wake: WakeWord, path: Path) -> float:
    """Highest score the model reaches anywhere in a clip."""
    samples, rate = wav_decode(path.read_bytes())
    assert rate == SAMPLE_RATE
    pcm = to_int16(samples)
    wake.reset()
    highest = 0.0
    for start in range(0, len(pcm) - FRAME_SAMPLES + 1, FRAME_SAMPLES):
        frame = pcm[start : start + FRAME_SAMPLES]
        scores = wake._model.predict(np.asarray(frame, dtype=np.int16))
        highest = max(highest, max(scores.values()))
    return highest


def build_clips(args, work: Path) -> list[Clip]:
    clips: list[Clip] = []
    index = 0

    for voice in args.voices:
        short = voice.stem.replace("en_US-", "").replace("en_GB-", "")
        for speed_name, scale in SPEEDS.items():
            # The phrase alone, and the phrase followed by a command — a wake
            # word has to survive being run into the next sentence.
            for suffix, text in (
                ("alone", args.phrase + "."),
                ("in-sentence", f"{args.phrase}, turn on the lights in the living room."),
            ):
                path = work / f"pos-{index}.wav"
                synthesize(args.piper, voice, text, path, scale)
                clips.append(Clip(f"{short}/{speed_name}/{suffix}", True, path))
                index += 1

        for kind, texts in (("everyday", DEFAULT_NEGATIVES), ("near-miss", near_misses(args.phrase))):
            for text in texts:
                path = work / f"neg-{index}.wav"
                synthesize(args.piper, voice, text, path, 1.0)
                label = text if len(text) <= 34 else text[:31] + "..."
                clips.append(Clip(f"{short}/{label}", False, path, kind=kind))
                index += 1

    return clips


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", type=Path, required=True, help="the wake word model")
    parser.add_argument("--phrase", required=True, help='the spoken phrase, e.g. "Hey Renfild"')
    parser.add_argument("--piper", type=Path, default=Path("/opt/renfild/piper/piper"))
    parser.add_argument(
        "--voices",
        type=Path,
        nargs="+",
        default=[Path("/opt/renfild/piper/voices/en_US-lessac-medium.onnx")],
        help="Piper voices to speak the phrase in; more voices, better evidence",
    )
    parser.add_argument("--resources", type=Path, help="openWakeWord's shared model directory")
    parser.add_argument("--work", type=Path, help="where to keep the generated audio")
    args = parser.parse_args()

    if not args.model.is_file():
        print(f"wake word model not found: {args.model}", file=sys.stderr)
        print("Train one with the openWakeWord notebook — see the README.", file=sys.stderr)
        return 1
    if not args.piper.is_file():
        print(f"piper not found at {args.piper}", file=sys.stderr)
        return 1

    work = args.work or Path(tempfile.mkdtemp(prefix="renfild-wake-"))
    work.mkdir(parents=True, exist_ok=True)

    wake = WakeWord(args.model, threshold=0.5, resources_dir=args.resources)
    wake.load()

    print(f'phrase: "{args.phrase}"\nmodel:  {args.model.name}\ncorpus: {work}\n')
    clips = build_clips(args, work)
    for clip in clips:
        clip.peak = peak_score(wake, clip.path)

    positives = [c for c in clips if c.positive]
    negatives = [c for c in clips if not c.positive]

    everyday = [c for c in negatives if c.kind == "everyday"]
    misses = [c for c in negatives if c.kind == "near-miss"]

    print(f"{'clip':52s}{'peak':>8s}")
    print("-" * 60)
    for clip in positives:
        print(f"{'+ wake  ' + clip.label:52s}{clip.peak:>8.3f}")
    for clip in sorted(misses, key=lambda c: -c.peak):
        print(f"{'~ near  ' + clip.label:52s}{clip.peak:>8.3f}")
    for clip in sorted(everyday, key=lambda c: -c.peak):
        print(f"{'- other ' + clip.label:52s}{clip.peak:>8.3f}")

    worst_positive = min(c.peak for c in positives)
    loudest_everyday = max((c.peak for c in everyday), default=0.0)
    loudest_miss = max((c.peak for c in misses), default=0.0)

    print(f"\nquietest wake word:        {worst_positive:.3f}")
    print(f"loudest everyday sentence: {loudest_everyday:.3f}")
    print(f"loudest near-miss:         {loudest_miss:.3f}")

    print(f"\n{'threshold':>10s}{'detected':>12s}{'everyday':>11s}{'near-miss':>11s}")
    for threshold in [round(0.1 * n, 1) for n in range(1, 10)]:
        detected = sum(1 for c in positives if c.peak >= threshold)
        false_everyday = sum(1 for c in everyday if c.peak >= threshold)
        false_miss = sum(1 for c in misses if c.peak >= threshold)
        marker = ""
        if detected == len(positives) and false_everyday == 0 and false_miss == 0:
            marker = "  <- clean"
        print(
            f"{threshold:>10.1f}{f'{detected}/{len(positives)}':>12s}"
            f"{false_everyday:>11d}{false_miss:>11d}{marker}"
        )

    if worst_positive <= loudest_everyday:
        print(
            "\nOrdinary speech scores as high as the wake word. That model is not usable —\n"
            "retrain it with a longer phrase and more varied samples."
        )
        return 2

    suggested = round((worst_positive + loudest_everyday) / 2, 2)
    print(f"\nsuggested wake.threshold: {suggested}")
    print(
        "That is the midpoint between the quietest wake word and the loudest ordinary\n"
        "sentence, on synthetic speech. Start there, watch journalctl -u renfild-satellite,\n"
        "and adjust: raise it if the television sets it off, lower it if you repeat yourself."
    )

    if worst_positive <= loudest_miss:
        print(
            f"\nNote: a near-miss also reaches {loudest_miss:.3f}, so no threshold tells it\n"
            "apart from the real phrase. Whether that matters is your call — a model that\n"
            "also answers to the bare name is often fine, and sometimes preferable."
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
