#!/usr/bin/env bash
# Build and install whisper.cpp as an OpenAI-compatible speech-to-text endpoint.
#
# Renfild treats Whisper as an external service — `whisper.url` is configuration,
# not code — so this is a convenience, not part of install.sh. Point it at any
# OpenAI-compatible endpoint instead if you already have one.
#
# whisper.cpp's server speaks the right shape out of the box; it just calls the
# route /inference by default, so this runs it with --inference-path set to the
# OpenAI one and Renfild's `whisper.api: openai` works unchanged.
#
#   sudo ./hack/whisper-server.sh                 # build, install, write a unit
#   sudo MODEL=tiny ./hack/whisper-server.sh      # a smaller model
#   sudo ./hack/whisper-server.sh --no-service    # build and install only
#
# Read the "Speech to text" section of the README before running this on the Pi
# itself: even the tiny model costs about five seconds per command there.
set -euo pipefail

PREFIX="${PREFIX:-/opt/whisper}"
MODEL="${MODEL:-base}"
PORT="${PORT:-9000}"
HOST="${HOST:-127.0.0.1}"
THREADS="${THREADS:-$(nproc)}"
REPO="${REPO:-https://github.com/ggml-org/whisper.cpp.git}"
MODELS_BASE="${MODELS_BASE:-https://huggingface.co/ggerganov/whisper.cpp/resolve/main}"
SERVICE_USER="${SERVICE_USER:-renfild}"
# The build wants several gigabytes and a real filesystem; /tmp on Raspberry Pi
# OS is a RAM disk, and filling it takes the rest of the machine with it.
WORK="${WORK:-/var/tmp/whisper-build}"

INSTALL_SERVICE=1
[ "${1:-}" = "--no-service" ] && INSTALL_SERVICE=0

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
die() { printf '\033[31merror: %s\033[0m\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run this with sudo"
command -v cmake >/dev/null || die "cmake is missing: apt-get install -y cmake"
command -v git   >/dev/null || die "git is missing"

say "building whisper.cpp in $WORK"
mkdir -p "$WORK"
if [ ! -d "$WORK/whisper.cpp" ]; then
  git clone --depth 1 "$REPO" "$WORK/whisper.cpp"
fi
cd "$WORK/whisper.cpp"
TMPDIR="$WORK" cmake -B build -DCMAKE_BUILD_TYPE=Release -DWHISPER_BUILD_TESTS=OFF >/dev/null
# Leave a core for the rest of the machine; this takes 20 minutes on a Pi 4.
TMPDIR="$WORK" cmake --build build --target whisper-server -j"$(( $(nproc) > 1 ? $(nproc) - 1 : 1 ))"

say "installing into $PREFIX"
install -d -m 0755 "$PREFIX/bin" "$PREFIX/lib" "$PREFIX/models"
install -m 0755 build/bin/whisper-server "$PREFIX/bin/"
cp -P build/bin/libwhisper.so* build/bin/libggml*.so* "$PREFIX/lib/"

if [ ! -f "$PREFIX/models/ggml-$MODEL.bin" ]; then
  say "downloading ggml-$MODEL"
  curl -fL --progress-bar -o "$PREFIX/models/ggml-$MODEL.bin" \
    "$MODELS_BASE/ggml-$MODEL.bin" || die "could not download ggml-$MODEL"
fi

if [ "$INSTALL_SERVICE" -eq 0 ]; then
  say "done — run it with"
  echo "  LD_LIBRARY_PATH=$PREFIX/lib $PREFIX/bin/whisper-server \\"
  echo "    --model $PREFIX/models/ggml-$MODEL.bin --host $HOST --port $PORT \\"
  echo "    --inference-path /v1/audio/transcriptions --threads $THREADS"
  exit 0
fi

say "writing /etc/systemd/system/whisper-server.service"
cat > /etc/systemd/system/whisper-server.service <<UNIT
[Unit]
Description=whisper.cpp speech to text (OpenAI-compatible)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
Environment=LD_LIBRARY_PATH=$PREFIX/lib
ExecStart=$PREFIX/bin/whisper-server \\
  --model $PREFIX/models/ggml-$MODEL.bin \\
  --host $HOST --port $PORT \\
  --inference-path /v1/audio/transcriptions \\
  --threads $THREADS
Restart=always
RestartSec=5

NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=full
ProtectHome=yes

[Install]
WantedBy=multi-user.target
UNIT

chown -R "$SERVICE_USER:$SERVICE_USER" "$PREFIX"
systemctl daemon-reload
systemctl enable --now whisper-server.service

say "done"
echo "  endpoint: http://$HOST:$PORT/v1/audio/transcriptions"
echo "  set whisper.url to http://$HOST:$PORT and whisper.api to openai"
