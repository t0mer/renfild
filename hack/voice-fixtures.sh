#!/usr/bin/env bash
# Generate the WAV fixtures the satellite's wake-word tests use.
#
# There is no microphone in CI and no recording of a human in this repository,
# so the fixtures are synthesised with Piper and resampled to the 16 kHz mono
# the models expect. Re-run this only if the fixtures need to change.
#
#   ./hack/voice-fixtures.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/satellite/tests/testdata"
PIPER="${PIPER:-/opt/renfild/piper/piper}"
VOICE="${VOICE:-/opt/renfild/piper/voices/en_US-lessac-medium.onnx}"
PYTHON="${PYTHON:-$ROOT/satellite/.venv/bin/python}"

[ -x "$PIPER" ] || { echo "piper not found at $PIPER" >&2; exit 1; }
[ -f "$VOICE" ] || { echo "voice not found at $VOICE" >&2; exit 1; }

mkdir -p "$OUT"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# name|text — "hey jarvis" is one of openWakeWord's pretrained models, which is
# what lets the tests check detection without shipping a custom wake word.
FIXTURES=(
  "hey_jarvis|Hey Jarvis."
  "unrelated_speech|Turn on the kitchen lights please, and close the blinds."
)

for fixture in "${FIXTURES[@]}"; do
  name="${fixture%%|*}"
  text="${fixture#*|}"
  echo "  $name: \"$text\""
  printf '%s\n' "$text" | "$PIPER" --model "$VOICE" --output_file "$WORK/$name.raw.wav" 2>/dev/null

  # Pad with a little silence at each end, mirroring how a satellite frames a
  # detection, and resample to what the models expect.
  # Pad with a little silence at each end, mirroring how a satellite frames a
  # detection, and resample to what the models expect. This uses the satellite's
  # own audio helpers, so the fixtures go through the same code path the daemon
  # does.
  (cd "$ROOT/satellite" && "$PYTHON" - "$WORK/$name.raw.wav" "$OUT/$name.wav" <<'PY'
import sys
from pathlib import Path

import numpy as np

from satellite.audio import resample, to_int16, wav_decode, wav_encode

source, target = Path(sys.argv[1]), Path(sys.argv[2])
samples, rate = wav_decode(source.read_bytes())
resampled = resample(samples, rate, 16_000)

pad = np.zeros(int(0.4 * 16_000), dtype=np.float32)
padded = np.concatenate((pad, resampled, pad))
target.write_bytes(wav_encode(to_int16(padded), 16_000))
print(f"    -> {target} ({len(padded) / 16_000:.2f}s)")
PY
  )
done

echo "done"
