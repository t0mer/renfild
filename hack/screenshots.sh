#!/usr/bin/env bash
# Refresh the README screenshots.
#
# Starts the server against a throwaway database with mocked external services,
# seeds it with believable data, then drives a headless Chromium over every page
# in both light and dark mode.
#
#   ./hack/screenshots.sh
#
# Requires: chromium (apt install chromium) and node. Puppeteer-core is
# installed into a gitignored scratch directory on first run.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/assets/screenshots"
TOOLS="$ROOT/.tools/screenshots"
WORK="$(mktemp -d)"
PORT="${SHOT_PORT:-18081}"
MOCK_PORT="${SHOT_MOCK_PORT:-18098}"
PIDS=()

cleanup() {
  for pid in "${PIDS[@]:-}"; do kill "$pid" 2>/dev/null || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT

say()  { printf '\n\033[36m==> %s\033[0m\n' "$1"; }
fail() { printf '\033[31mFAIL: %s\033[0m\n' "$1" >&2; exit 1; }

CHROME="${CHROME:-$(command -v chromium || command -v chromium-browser || command -v google-chrome || true)}"
[ -n "$CHROME" ] || fail "no chromium found — sudo apt install chromium"
command -v node >/dev/null || fail "node is required"

# --------------------------------------------------------------------------
say "building the server and the web UI"
"$ROOT/scripts/web.sh" >/dev/null
(cd "$ROOT/server" && CGO_ENABLED=0 go build -o "$WORK/renfild" .) || fail "server build failed"

# --------------------------------------------------------------------------
say "starting mocked Whisper + embedder"
python3 - "$MOCK_PORT" <<'PY' &
import json, random, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

TRANSCRIPTS = [
    "turn on the lights in the living room",
    "what time is it",
    "good morning",
    "what is the weather going to be like tomorrow",
    "מה השעה",
]
state = {"n": 0, "voice": 0}

def embedding(seed):
    # Deterministic per voice, and near-orthogonal between voices, plus a little
    # jitter so similarity scores do not all come out at exactly 1.00.
    rng = random.Random(seed)
    base = [rng.gauss(0, 1) for _ in range(192)]
    noise = random.Random()
    return [value + noise.gauss(0, 0.25) for value in base]

class Handler(BaseHTTPRequestHandler):
    def _json(self, payload):
        body = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        # /voice/<n> switches which synthetic voice the embedder returns next.
        if self.path.startswith("/voice/"):
            state["voice"] = int(self.path.rsplit("/", 1)[1])
        self._json({"status": "ok"})

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", 0)))
        if self.path.startswith("/embed"):
            self._json({"embedding": embedding(state["voice"]), "duration_s": 2.4, "dims": 192})
        else:
            text = TRANSCRIPTS[state["n"] % len(TRANSCRIPTS)]
            state["n"] += 1
            self._json({"text": text})

    def log_message(self, *_):
        pass

HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
PY
PIDS+=($!)

# A fake Piper: the screenshots do not need real audio.
python3 - "$WORK/audio.wav" <<'PY'
import struct, sys, wave
with wave.open(sys.argv[1], "wb") as out:
    out.setnchannels(1); out.setsampwidth(2); out.setframerate(16000)
    out.writeframes(struct.pack("<8000h", *([0] * 8000)))
PY
printf '#!/usr/bin/env bash\ncat > /dev/null\ncat "%s"\n' "$WORK/audio.wav" > "$WORK/piper"
chmod +x "$WORK/piper"
touch "$WORK/voice.onnx"

cat > "$WORK/config.yaml" <<CONFIG
listen: "127.0.0.1:$PORT"
db: "$WORK/renfild.db"
log_level: "warn"
audio_retention: "none"
audio_dir: "$WORK/audio"
whisper: { url: "http://127.0.0.1:$MOCK_PORT", api: "openai", model: "whisper-1", language: "auto" }
embedder: { url: "http://127.0.0.1:$MOCK_PORT" }
ollama: { url: "http://127.0.0.1:$MOCK_PORT", model: "qwen2.5:7b", fallback: false }
piper: { binary: "$WORK/piper", voice: "$WORK/voice.onnx", timeout: "10s" }
speaker: { default_threshold: 0.45, unknown_policy: "restricted", enrollment_samples: 5, min_sample_similarity: 0.30 }
CONFIG

say "starting the server on :$PORT"
"$WORK/renfild" serve --config "$WORK/config.yaml" &
PIDS+=($!)
for _ in $(seq 1 30); do
  curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null || fail "server never became healthy"

# --------------------------------------------------------------------------
say "seeding demo data"
api() { curl -fsS -H 'Content-Type: application/json' "$@"; }

api -X POST "http://127.0.0.1:$PORT/api/ui/speakers" -d '{"name":"Tomer","role":"owner"}' >/dev/null
api -X POST "http://127.0.0.1:$PORT/api/ui/speakers" -d '{"name":"Dana","role":"member"}' >/dev/null
api -X POST "http://127.0.0.1:$PORT/api/ui/speakers" -d '{"name":"Noam","role":"kid"}' >/dev/null

voice() { curl -fsS "http://127.0.0.1:$MOCK_PORT/voice/$1" >/dev/null; }

for id in 1 2 3; do
  voice "$id"
  for label in wake-1 wake-2 sentence-1 sentence-2 free; do
    curl -fsS -X POST "http://127.0.0.1:$PORT/api/ui/speakers/$id/enrollments" \
      -F "audio=@$WORK/audio.wav" -F "label=$label" >/dev/null
  done
done

api -X POST "http://127.0.0.1:$PORT/api/ui/intents" -d '{
  "name":"living room lights","enabled":true,"match_type":"contains",
  "patterns":["turn on the lights","lights on","הדלק את האור"],
  "min_role":"member","handler":"webhook",
  "handler_config":{"url":"http://127.0.0.1:'"$MOCK_PORT"'/webhook","method":"POST","body":"{\"who\":\"{{.Speaker}}\"}","reply":"Lights on, {{.Speaker}}."},
  "priority":20}' >/dev/null
api -X POST "http://127.0.0.1:$PORT/api/ui/intents" -d '{
  "name":"unlock the front door","enabled":true,"match_type":"regex",
  "patterns":["^unlock the (front )?door$"],
  "min_role":"owner","handler":"webhook",
  "handler_config":{"url":"http://127.0.0.1:'"$MOCK_PORT"'/webhook","method":"POST","reply":"Unlocked."},
  "priority":30}' >/dev/null
api -X POST "http://127.0.0.1:$PORT/api/ui/intents" -d '{
  "name":"bedtime","enabled":false,"match_type":"contains","patterns":["good night"],
  "min_role":"kid","handler":"reply","handler_config":{"template":"Good night {{.Speaker}}."},
  "priority":40}' >/dev/null

# Rotate through the three enrolled voices, with a couple of utterances from a
# fourth, unenrolled one so the "unknown voice" path shows up too.
for round in 1 2 3; do
  for id in 1 2 3; do
    voice "$id"
    curl -fsS -o /dev/null -X POST "http://127.0.0.1:$PORT/api/v1/utterance" \
      -F "wake=@$WORK/audio.wav" -F "command=@$WORK/audio.wav" -F "satellite_id=living-room"
  done
done
voice 99
curl -fsS -o /dev/null -X POST "http://127.0.0.1:$PORT/api/v1/utterance" \
  -F "wake=@$WORK/audio.wav" -F "command=@$WORK/audio.wav" -F "satellite_id=kitchen"

# --------------------------------------------------------------------------
say "capturing screenshots"
mkdir -p "$TOOLS" "$OUT"
if [ ! -d "$TOOLS/node_modules/puppeteer-core" ]; then
  (cd "$TOOLS" && npm install --silent --no-audit --no-fund puppeteer-core@23 >/dev/null)
fi

CHROME="$CHROME" BASE="http://127.0.0.1:$PORT" OUT="$OUT" node "$ROOT/hack/screenshots.mjs" \
  || fail "screenshot capture failed"

printf '\n\033[32mscreenshots written to %s\033[0m\n' "$OUT"
ls -1 "$OUT"
