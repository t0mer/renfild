#!/usr/bin/env bash
#
# Renfild native installer for Raspberry Pi OS / Debian (arm64 and armv7).
#
# Installs everything under /opt/renfild and registers three systemd units.
# There is no Docker anywhere in this project. Re-run it to upgrade: it is
# idempotent and never overwrites a config file you have edited.
#
#   sudo ./install.sh                     # everything
#   sudo ./install.sh --skip-satellite    # e.g. a server-only box
#   sudo ./install.sh --help
set -euo pipefail

PREFIX="${PREFIX:-/opt/renfild}"
DATA_DIR="${DATA_DIR:-/var/lib/renfild}"
SERVICE_USER="${SERVICE_USER:-renfild}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

PIPER_VERSION="${PIPER_VERSION:-2023.11.14-2}"
PIPER_BASE_URL="${PIPER_BASE_URL:-https://github.com/rhasspy/piper/releases/download}"
PIPER_VOICE="${PIPER_VOICE:-en_US-lessac-medium}"
PIPER_VOICE_URL="${PIPER_VOICE_URL:-https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium}"
SILERO_VAD_URL="${SILERO_VAD_URL:-https://raw.githubusercontent.com/snakers4/silero-vad/master/src/silero_vad/data/silero_vad.onnx}"

INSTALL_SERVER=1
INSTALL_SATELLITE=1
INSTALL_EMBEDDER=1
INSTALL_PIPER=1
START_SERVICES=1

# ---------------------------------------------------------------------------
# output helpers
# ---------------------------------------------------------------------------
say()  { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
info() { printf '    %s\n' "$*"; }
warn() { printf '\033[33m    warning: %s\033[0m\n' "$*"; }
die()  { printf '\033[31merror: %s\033[0m\n' "$*" >&2; exit 1; }

usage() {
  sed -n '3,10p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  cat <<'USAGE'

Options:
  --skip-server       do not install the Go server
  --skip-satellite    do not install the satellite daemon
  --skip-embedder     do not install the speaker embedder
  --skip-piper        do not download the Piper binary or voice
  --no-start          install the units but do not start them
  --help              show this message

Environment overrides:
  PREFIX (default /opt/renfild), DATA_DIR (/var/lib/renfild),
  SERVICE_USER (renfild), PIPER_VERSION, PIPER_VOICE, SILERO_VAD_URL
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --skip-server)    INSTALL_SERVER=0 ;;
    --skip-satellite) INSTALL_SATELLITE=0 ;;
    --skip-embedder)  INSTALL_EMBEDDER=0 ;;
    --skip-piper)     INSTALL_PIPER=0 ;;
    --no-start)       START_SERVICES=0 ;;
    --help|-h)        usage; exit 0 ;;
    *)                die "unknown option: $1 (try --help)" ;;
  esac
  shift
done

[ "$(id -u)" -eq 0 ] || die "run as root: sudo ./install.sh"

# ---------------------------------------------------------------------------
# platform
# ---------------------------------------------------------------------------
detect_arch() {
  case "$(uname -m)" in
    aarch64|arm64) echo "arm64" ;;
    armv7l|armv6l) echo "armv7" ;;
    x86_64|amd64)  echo "amd64" ;;
    *) die "unsupported architecture: $(uname -m)" ;;
  esac
}
ARCH="$(detect_arch)"

piper_asset() {
  case "$ARCH" in
    arm64) echo "piper_linux_aarch64.tar.gz" ;;
    armv7) echo "piper_linux_armv7l.tar.gz" ;;
    amd64) echo "piper_linux_x86_64.tar.gz" ;;
  esac
}

require() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }

download() { # url dest
  info "fetching $(basename "$2")"
  if curl -fsSL --retry 3 -o "$2.part" "$1"; then
    mv "$2.part" "$2"
    return 0
  fi
  rm -f "$2.part"
  return 1
}

# ---------------------------------------------------------------------------
# prerequisites
# ---------------------------------------------------------------------------
say "checking prerequisites (arch: $ARCH)"
require curl
require tar
require python3
require systemctl

APT_PACKAGES=(python3-venv)
if [ "$INSTALL_SATELLITE" -eq 1 ]; then
  APT_PACKAGES+=(libportaudio2 alsa-utils)
fi
if command -v apt-get >/dev/null 2>&1; then
  missing=()
  for package in "${APT_PACKAGES[@]}"; do
    dpkg -s "$package" >/dev/null 2>&1 || missing+=("$package")
  done
  if [ ${#missing[@]} -gt 0 ]; then
    info "installing system packages: ${missing[*]}"
    DEBIAN_FRONTEND=noninteractive apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${missing[@]}"
  fi
else
  warn "apt-get not found — make sure python3-venv and libportaudio2 are installed"
fi

# ---------------------------------------------------------------------------
# user and directories
# ---------------------------------------------------------------------------
say "creating the $SERVICE_USER user and directory layout"
if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  useradd --system --home-dir "$PREFIX" --shell /usr/sbin/nologin "$SERVICE_USER"
  info "created system user $SERVICE_USER"
else
  info "user $SERVICE_USER already exists"
fi
# The satellite needs the sound devices.
if getent group audio >/dev/null; then
  usermod -aG audio "$SERVICE_USER"
fi

install -d -m 0755 "$PREFIX" "$PREFIX/etc" "$PREFIX/server" "$PREFIX/piper/voices"
install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR"

# ---------------------------------------------------------------------------
# server
# ---------------------------------------------------------------------------
install_server() {
  say "installing the server"
  local binary=""
  # Prefer a locally built binary, then a release artifact in dist/.
  if [ -x "$SOURCE_DIR/server/renfild" ]; then
    binary="$SOURCE_DIR/server/renfild"
  else
    binary="$(find "$SOURCE_DIR/dist" -maxdepth 1 -name "renfild-*-linux-$ARCH*" -type f 2>/dev/null | sort | tail -1 || true)"
  fi

  if [ -z "$binary" ] && command -v go >/dev/null 2>&1; then
    info "no prebuilt binary found — building from source"
    (cd "$SOURCE_DIR/server" && CGO_ENABLED=0 go build -trimpath -o "$SOURCE_DIR/server/renfild" .)
    binary="$SOURCE_DIR/server/renfild"
  fi
  [ -n "$binary" ] || die "no server binary: run 'make build' first, or download a release into dist/"

  install -m 0755 "$binary" "$PREFIX/server/renfild"
  info "installed $(basename "$binary") -> $PREFIX/server/renfild"

  if [ ! -f "$PREFIX/etc/server.yaml" ]; then
    install -m 0640 -o "$SERVICE_USER" -g "$SERVICE_USER" \
      "$SOURCE_DIR/server/config.example.yaml" "$PREFIX/etc/server.yaml"
    info "wrote $PREFIX/etc/server.yaml — review it before starting"
  else
    info "keeping the existing $PREFIX/etc/server.yaml"
  fi
  install -m 0644 "$SOURCE_DIR/server/renfild-server.service" "$SYSTEMD_DIR/renfild-server.service"
}

# ---------------------------------------------------------------------------
# python components
# ---------------------------------------------------------------------------
sync_python_component() { # name source_dir
  local name="$1" source="$2" target="$PREFIX/$1"
  install -d -m 0755 "$target"
  # Copy the package and its requirements; leave .venv and models in place.
  cp -r "$source/$name" "$target/"
  cp "$source"/requirements*.txt "$target/"
  if [ -f "$source/pyproject.toml" ]; then
    cp "$source/pyproject.toml" "$target/"
  fi

  if [ ! -x "$target/.venv/bin/python" ]; then
    info "creating the virtualenv (this takes a while on a Pi)"
    python3 -m venv "$target/.venv"
  fi
  "$target/.venv/bin/pip" install --quiet --upgrade pip wheel
  info "installing Python dependencies"
  "$target/.venv/bin/pip" install --quiet -r "$target/requirements.txt"
}

install_embedder() {
  say "installing the embedder (SpeechBrain ECAPA — pulls in PyTorch, be patient)"
  sync_python_component embedder "$SOURCE_DIR/embedder"
  install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" \
    "$PREFIX/embedder/models" \
    "$PREFIX/embedder/models/huggingface" \
    "$PREFIX/embedder/models/cache"

  if [ ! -f "$PREFIX/etc/embedder.env" ]; then
    install -m 0640 "$SOURCE_DIR/embedder/.env.example" "$PREFIX/etc/embedder.env"
    info "wrote $PREFIX/etc/embedder.env"
  fi
  install -m 0644 "$SOURCE_DIR/embedder/renfild-embedder.service" "$SYSTEMD_DIR/renfild-embedder.service"
  chown -R "$SERVICE_USER:$SERVICE_USER" "$PREFIX/embedder"
}

install_satellite() {
  say "installing the satellite"
  sync_python_component satellite "$SOURCE_DIR/satellite"
  # openWakeWord's metadata demands tflite-runtime, which has no wheel beyond
  # CPython 3.11; ai-edge-litert (already pinned) provides the interpreter, so
  # the package itself is installed without its dependency resolution.
  "$PREFIX/satellite/.venv/bin/pip" install --quiet --no-deps \
    -r "$PREFIX/satellite/requirements-openwakeword.txt"

  install -d -m 0755 "$PREFIX/satellite/models"
  # Copy any wake word models shipped alongside this checkout.
  find "$SOURCE_DIR/satellite/models" -maxdepth 1 \( -name '*.tflite' -o -name '*.onnx' \) \
    -exec install -m 0644 {} "$PREFIX/satellite/models/" \; 2>/dev/null || true

  if [ ! -f "$PREFIX/satellite/models/silero_vad.onnx" ]; then
    download "$SILERO_VAD_URL" "$PREFIX/satellite/models/silero_vad.onnx" \
      || warn "could not download Silero VAD — the satellite will fall back to an energy gate"
  fi

  info "fetching openWakeWord's shared feature models"
  "$PREFIX/satellite/.venv/bin/python" - <<'PY' || warn "openWakeWord model download failed — run it again later"
import openwakeword.utils as utils
utils.download_models(model_names=[])
PY

  if [ ! -f "$PREFIX/etc/satellite.yaml" ]; then
    install -m 0640 "$SOURCE_DIR/satellite/config.example.yaml" "$PREFIX/etc/satellite.yaml"
    info "wrote $PREFIX/etc/satellite.yaml — set your audio devices and wake word model"
  else
    info "keeping the existing $PREFIX/etc/satellite.yaml"
  fi
  install -m 0644 "$SOURCE_DIR/satellite/renfild-satellite.service" "$SYSTEMD_DIR/renfild-satellite.service"
  chown -R "$SERVICE_USER:$SERVICE_USER" "$PREFIX/satellite"
}

# ---------------------------------------------------------------------------
# piper
# ---------------------------------------------------------------------------
install_piper() {
  say "installing Piper ($PIPER_VERSION, $ARCH)"
  if [ -x "$PREFIX/piper/piper" ]; then
    info "piper is already installed — skipping the download"
  else
    local asset tmp
    asset="$(piper_asset)"
    tmp="$(mktemp -d)"
    if download "$PIPER_BASE_URL/$PIPER_VERSION/$asset" "$tmp/$asset"; then
      tar -xzf "$tmp/$asset" -C "$tmp"
      # The archive contains a piper/ directory with the binary and its libs.
      cp -r "$tmp/piper/." "$PREFIX/piper/"
      chmod 0755 "$PREFIX/piper/piper"
      info "installed piper -> $PREFIX/piper/piper"
    else
      warn "could not download Piper — install it manually into $PREFIX/piper"
    fi
    rm -rf "$tmp"
  fi

  if [ ! -f "$PREFIX/piper/voices/$PIPER_VOICE.onnx" ]; then
    download "$PIPER_VOICE_URL/$PIPER_VOICE.onnx" "$PREFIX/piper/voices/$PIPER_VOICE.onnx" \
      && download "$PIPER_VOICE_URL/$PIPER_VOICE.onnx.json" "$PREFIX/piper/voices/$PIPER_VOICE.onnx.json" \
      || warn "could not download the voice — see https://huggingface.co/rhasspy/piper-voices"
  else
    info "voice $PIPER_VOICE is already present"
  fi
  chown -R "$SERVICE_USER:$SERVICE_USER" "$PREFIX/piper"
}

# ---------------------------------------------------------------------------
# run
# ---------------------------------------------------------------------------
if [ "$INSTALL_SERVER" -eq 1 ];    then install_server;    fi
if [ "$INSTALL_EMBEDDER" -eq 1 ];  then install_embedder;  fi
if [ "$INSTALL_SATELLITE" -eq 1 ]; then install_satellite; fi
if [ "$INSTALL_PIPER" -eq 1 ];     then install_piper;     fi

chown -R "$SERVICE_USER:$SERVICE_USER" "$PREFIX/etc" "$DATA_DIR"

say "registering systemd units"
systemctl daemon-reload

UNITS=()
if [ "$INSTALL_EMBEDDER" -eq 1 ];  then UNITS+=(renfild-embedder);  fi
if [ "$INSTALL_SERVER" -eq 1 ];    then UNITS+=(renfild-server);    fi
if [ "$INSTALL_SATELLITE" -eq 1 ]; then UNITS+=(renfild-satellite); fi

# A fresh install has no wake word model yet, and the satellite cannot run
# without one. Enable it so it comes up after a reboot once the model is in
# place, but do not start it now just to have it fail.
wake_model_present() {
  find "$PREFIX/satellite/models" -maxdepth 1 \( -name '*.tflite' -o -name '*.onnx' \) \
    ! -name 'silero_vad.onnx' -print -quit 2>/dev/null | grep -q .
}

for unit in "${UNITS[@]}"; do
  systemctl enable "$unit" >/dev/null 2>&1 || warn "could not enable $unit"

  if [ "$START_SERVICES" -eq 0 ]; then
    info "$unit enabled (not started)"
    continue
  fi
  if [ "$unit" = "renfild-satellite" ] && ! wake_model_present; then
    info "$unit enabled but not started — it needs a wake word model first"
    continue
  fi

  systemctl restart "$unit" || warn "$unit failed to start — check: journalctl -u $unit -n 50"
  info "$unit enabled and started"
done

cat <<DONE

$(say "Renfild is installed")
    Configuration : $PREFIX/etc/{server,satellite}.yaml, $PREFIX/etc/embedder.env
    Data          : $DATA_DIR/renfild.db
    Web UI        : http://$(hostname -I 2>/dev/null | awk '{print $1}'):8080

Next steps:
    1. Drop your wake word model in $PREFIX/satellite/models/ and point
       $PREFIX/etc/satellite.yaml at it (wake.model_path), then:
           sudo systemctl start renfild-satellite
    2. Set the audio devices in $PREFIX/etc/satellite.yaml — 'arecord -L' lists them.
    3. Check the Whisper and Ollama URLs in $PREFIX/etc/server.yaml.
    4. Open the web UI and enroll yourself under Speakers.

    systemctl status renfild-server renfild-embedder renfild-satellite
    journalctl -u renfild-satellite -f
DONE
