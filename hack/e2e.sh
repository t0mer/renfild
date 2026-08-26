#!/usr/bin/env bash
# End-to-end check of the server pipeline with every external service mocked.
#
# Starts fake Whisper, embedder and Piper endpoints, runs the real server
# against them, enrolls a speaker, posts an utterance and asserts that the
# speaker, transcript, intent and reply all came out right.
#
#   ./hack/e2e.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
PORT="${E2E_PORT:-18080}"
MOCK_PORT="${E2E_MOCK_PORT:-18099}"
PIDS=()

cleanup() {
  for pid in "${PIDS[@]:-}"; do kill "$pid" 2>/dev/null || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT

say() { printf '\n\033[36m==> %s\033[0m\n' "$1"; }
fail() { printf '\033[31mFAIL: %s\033[0m\n' "$1" >&2; exit 1; }

# --------------------------------------------------------------------------
# Fixtures: a one-second silent WAV, and a fake Piper that emits one too.
# --------------------------------------------------------------------------
say "building fixtures in $WORK"
python3 - "$WORK/audio.wav" <<'PY'
import struct, sys, wave
with wave.open(sys.argv[1], "wb") as out:
    out.setnchannels(1); out.setsampwidth(2); out.setframerate(16000)
    out.writeframes(struct.pack("<16000h", *([0] * 16000)))
PY

cat > "$WORK/piper" <<PIPER
#!/usr/bin/env bash
cat > /dev/null
cat "$WORK/audio.wav"
PIPER
chmod +x "$WORK/piper"
touch "$WORK/voice.onnx"

# --------------------------------------------------------------------------
# Mock Whisper + embedder. The embedder returns a stable vector per request so
# enrollment and recognition line up.
# --------------------------------------------------------------------------
say "starting mock whisper + embedder on :$MOCK_PORT"
python3 - "$MOCK_PORT" <<'PY' &
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

EMBEDDING = [0.05 * ((i % 7) - 3) for i in range(192)]

class Handler(BaseHTTPRequestHandler):
    def _json(self, payload, status=200):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self._json({"status": "ok"})

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        self.rfile.read(length)
        if self.path.startswith("/embed"):
            self._json({"embedding": EMBEDDING, "duration_s": 1.0, "dims": 192})
        else:
            self._json({"text": "turn on the lights"})

    def log_message(self, *_):
        pass

HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
PY
PIDS+=($!)

# --------------------------------------------------------------------------
# Config + server
# --------------------------------------------------------------------------
cat > "$WORK/config.yaml" <<CONFIG
listen: "127.0.0.1:$PORT"
db: "$WORK/renfild.db"
log_level: "warn"
audio_retention: "none"
audio_dir: "$WORK/audio"
whisper: { url: "http://127.0.0.1:$MOCK_PORT", api: "openai", model: "whisper-1", language: "en" }
embedder: { url: "http://127.0.0.1:$MOCK_PORT" }
ollama: { url: "http://127.0.0.1:$MOCK_PORT", model: "test", fallback: false }
piper: { binary: "$WORK/piper", voice: "$WORK/voice.onnx", timeout: "10s" }
speaker: { default_threshold: 0.45, unknown_policy: "restricted", enrollment_samples: 1, min_sample_similarity: 0.3 }
CONFIG

if command -v ss >/dev/null 2>&1 && ss -lnt 2>/dev/null | grep -q ":$PORT "; then
  fail "port $PORT is already in use — set E2E_PORT to something else"
fi

say "building the server"
# Build once rather than using `go run`, so the process we start is the one we
# can stop, and startup is not competing with the compiler.
(cd "$ROOT/server" && CGO_ENABLED=0 go build -o "$WORK/renfild" .) || fail "build failed"

say "starting the server on :$PORT"
"$WORK/renfild" serve --config "$WORK/config.yaml" &
PIDS+=($!)

for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null || fail "server never became healthy"

# --------------------------------------------------------------------------
# Exercise the pipeline
# --------------------------------------------------------------------------
say "enrolling a speaker"
curl -fsS -X POST "http://127.0.0.1:$PORT/api/ui/speakers" \
  -H 'Content-Type: application/json' \
  -d '{"name":"tomer","role":"owner"}' > "$WORK/speaker.json"
SPEAKER_ID="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["id"])' "$WORK/speaker.json")"

curl -fsS -X POST "http://127.0.0.1:$PORT/api/ui/speakers/$SPEAKER_ID/enrollments" \
  -F "audio=@$WORK/audio.wav" -F "label=wake-1" > "$WORK/enroll.json"
grep -q '"accepted":true' "$WORK/enroll.json" || fail "enrollment was rejected: $(cat "$WORK/enroll.json")"

say "adding an intent"
curl -fsS -X POST "http://127.0.0.1:$PORT/api/ui/intents" \
  -H 'Content-Type: application/json' \
  -d '{"name":"lights","enabled":true,"match_type":"contains","patterns":["lights"],
       "min_role":"member","handler":"reply","handler_config":{"template":"Lights on, {{.Speaker}}."},
       "priority":1}' > /dev/null

say "posting an utterance"
curl -fsS -D "$WORK/headers.txt" -o "$WORK/reply.wav" \
  -X POST "http://127.0.0.1:$PORT/api/v1/utterance" \
  -F "wake=@$WORK/audio.wav" -F "command=@$WORK/audio.wav" -F "satellite_id=e2e"

grep -qi '^x-speaker: tomer' "$WORK/headers.txt" || fail "speaker not identified: $(grep -i '^x-' "$WORK/headers.txt")"
grep -qi '^x-intent: lights' "$WORK/headers.txt" || fail "intent did not fire: $(grep -i '^x-' "$WORK/headers.txt")"
grep -qi 'content-type: audio/wav' "$WORK/headers.txt" || fail "no audio returned"
[ -s "$WORK/reply.wav" ] || fail "reply audio is empty"
head -c 4 "$WORK/reply.wav" | grep -q RIFF || fail "reply is not a WAV"

say "checking the history"
curl -fsS "http://127.0.0.1:$PORT/api/ui/history?limit=1" > "$WORK/history.json"
python3 - "$WORK/history.json" <<'PY'
import json, sys
page = json.load(open(sys.argv[1]))
item = page["items"][0]
assert item["speaker"] == "tomer", item
assert item["transcript"] == "turn on the lights", item
assert item["intent"] == "lights", item
assert item["reply"] == "Lights on, tomer.", item
assert item["latency_ms_total"] >= 0, item
print("history row looks right")
PY

printf '\n\033[32mE2E PASSED\033[0m\n'
