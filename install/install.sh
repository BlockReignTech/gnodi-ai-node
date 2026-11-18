#!/usr/bin/env sh
# Gnodi AI Node installer.
#
#   curl -fsSL https://get.gnodi-ai.com | sh
#
# Downloads the daemon, writes a config file, and installs a service. It never
# overwrites an existing config — re-running upgrades the binary and leaves your
# licence key and settings alone.
set -eu

REPO="${GNODI_REPO:-BlockReignTech/gnodi-ai-node}"
VERSION="${GNODI_VERSION:-latest}"
BIN_DIR="${GNODI_BIN_DIR:-/usr/local/bin}"
BIN="$BIN_DIR/gnodi-ai-node"

red()  { printf '\033[31m%s\033[0m\n' "$*"; }
bold() { printf '\033[1m%s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }
die()  { red "error: $*"; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }
need curl
need uname

# --- platform -------------------------------------------------------------
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported architecture: $arch" ;;
esac
case "$os" in
  linux|darwin) ;;
  *) die "unsupported OS: $os (Windows: run install/install.ps1 from PowerShell)" ;;
esac

if [ "$os" = "linux" ]; then
  CONF_DIR="${GNODI_CONF_DIR:-/etc/gnodi-ai-node}"
else
  CONF_DIR="${GNODI_CONF_DIR:-/usr/local/etc/gnodi-ai-node}"
fi
CONF="$CONF_DIR/node.env"

SUDO=""
if [ "$(id -u)" -ne 0 ] && [ "${GNODI_NO_SUDO:-}" != "1" ]; then
  command -v sudo >/dev/null 2>&1 || die "run as root, or install sudo"
  SUDO="sudo"
fi

bold "Gnodi AI Node — installing for ${os}/${arch}"

# --- download -------------------------------------------------------------
# GNODI_BASE_URL exists so this script can be exercised against a local
# directory of artifacts instead of only against a live GitHub release.
if [ -n "${GNODI_BASE_URL:-}" ]; then
  base="$GNODI_BASE_URL"
elif [ "$VERSION" = "latest" ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi
asset="gnodi-ai-node_${os}_${arch}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

info "downloading $asset"
curl -fsSL "$base/$asset" -o "$tmp/gnodi-ai-node" \
  || die "download failed — check that $VERSION has a $os/$arch build"

# Verify against the published checksums. A binary that will hold your licence
# key and run unattended is worth one extra request.
if curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" 2>/dev/null; then
  expected=$(grep " $asset\$" "$tmp/SHA256SUMS" | awk '{print $1}' || true)
  if [ -n "$expected" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      actual=$(sha256sum "$tmp/gnodi-ai-node" | awk '{print $1}')
    else
      actual=$(shasum -a 256 "$tmp/gnodi-ai-node" | awk '{print $1}')
    fi
    [ "$expected" = "$actual" ] || die "checksum mismatch — refusing to install"
    info "checksum verified"
  else
    red "  warning: no checksum published for $asset"
  fi
else
  red "  warning: SHA256SUMS not available; skipping verification"
fi

chmod +x "$tmp/gnodi-ai-node"
$SUDO mkdir -p "$BIN_DIR"
$SUDO mv "$tmp/gnodi-ai-node" "$BIN"
info "installed $BIN"

# --- config ---------------------------------------------------------------
$SUDO mkdir -p "$CONF_DIR"

if [ -f "$CONF" ]; then
  info "keeping existing config at $CONF"
else
  LICENSE_KEY="${LICENSE_KEY:-}"
  GATEWAY_URL="${GATEWAY_URL:-wss://gw.gnodi-ai.com/v1/agent}"
  NODESVC_URL="${NODESVC_URL:-https://nodes.gnodi-ai.com}"
  INFERENCE_URL="${INFERENCE_URL:-http://localhost:11434/v1}"

  if [ -z "$LICENSE_KEY" ]; then
    if [ -t 0 ]; then
      printf '\n  Licence key (XXXX-XXXX-XXXX-XXXX): '
      read -r LICENSE_KEY
    else
      # Piped from curl, so there is no terminal to prompt on.
      die "set LICENSE_KEY, e.g.  curl -fsSL https://get.gnodi-ai.com | LICENSE_KEY=… sh"
    fi
  fi
  [ -n "$LICENSE_KEY" ] || die "a licence key is required"

  umask 077
  $SUDO sh -c "cat > '$CONF'" <<EOF
# Gnodi AI Node configuration. Restart the service after editing.
LICENSE_KEY=$LICENSE_KEY
GATEWAY_URL=$GATEWAY_URL
NODESVC_URL=$NODESVC_URL
INFERENCE_URL=$INFERENCE_URL

# Jobs served at once. Raise it if the GPU is idle under load.
MAX_CONCURRENCY=1

# Leave unset to auto-detect (nvidia-smi). Set it if detection fails.
# VRAM_GB=24

# Optional: narrow to specific catalog model ids. Empty serves everything
# this card can hold.
# MODELS=gnodi/llama3.1-8b
EOF
  $SUDO chmod 600 "$CONF"
  info "wrote $CONF (licence key is readable only by root)"
fi

# --- service --------------------------------------------------------------
if [ "${GNODI_SKIP_SERVICE:-}" = "1" ]; then
  info "skipping service installation (GNODI_SKIP_SERVICE=1)"

elif [ "$os" = "linux" ] && command -v systemctl >/dev/null 2>&1; then
  $SUDO sh -c "cat > /etc/systemd/system/gnodi-ai-node.service" <<EOF
[Unit]
Description=Gnodi AI Node
Documentation=https://www.gnodi-ai.com
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$CONF
ExecStart=$BIN
Restart=always
RestartSec=5
# The daemon holds a licence key and a device key; it needs no privileges.
DynamicUser=yes
StateDirectory=gnodi-ai-node
Environment=STATE_DIR=/var/lib/gnodi-ai-node
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes

[Install]
WantedBy=multi-user.target
EOF
  $SUDO systemctl daemon-reload
  $SUDO systemctl enable --now gnodi-ai-node
  info "service started — journalctl -u gnodi-ai-node -f"

  # --- the engine's context window ----------------------------------------
  #
  # Ollama defaults to a 4096-token window, and that default is not something
  # the daemon can override per model: it proxies to Ollama's
  # OpenAI-compatible endpoint, which re-applies the server default on every
  # request and discards whatever window the model was loaded with. One value
  # for the node, set here, is the only lever there is.
  #
  # Left alone it is a silent fault rather than a loud one. The catalog
  # advertises a larger window, the node serves 4096, and a conversation past
  # that is truncated from the front -- which the person having it experiences
  # as the model forgetting what they said, with nothing on screen to explain
  # it. The daemon logs a "context:" warning at every start while they
  # disagree.
  if command -v ollama >/dev/null 2>&1 && [ -f /lib/systemd/system/ollama.service -o -f /etc/systemd/system/ollama.service ]; then
    OLLAMA_DROPIN=/etc/systemd/system/ollama.service.d/zz-gnodi-context.conf
    if [ -f "$OLLAMA_DROPIN" ]; then
      info "ollama context already configured ($OLLAMA_DROPIN)"
    else
      $SUDO mkdir -p /etc/systemd/system/ollama.service.d
      $SUDO sh -c "cat > '$OLLAMA_DROPIN'" <<EOF
# Written by the Gnodi node installer. Delete this file to go back to
# Ollama's own default. See "context:" lines in journalctl -u gnodi-ai-node.
[Service]
Environment=OLLAMA_CONTEXT_LENGTH=${GNODI_CONTEXT_TOKENS:-16384}
EOF
      $SUDO systemctl daemon-reload
      $SUDO systemctl restart ollama
      info "ollama context window set to ${GNODI_CONTEXT_TOKENS:-16384} tokens"
    fi
  else
    info "ollama is not a systemd service here — set OLLAMA_CONTEXT_LENGTH=${GNODI_CONTEXT_TOKENS:-16384} yourself"
  fi

elif [ "$os" = "darwin" ]; then
  PLIST="/Library/LaunchDaemons/tech.blockreign.gnodi-ai-node.plist"
  $SUDO mkdir -p /usr/local/var/gnodi-ai-node /usr/local/var/log

  # launchd has no EnvironmentFile, so the config is expanded into the plist.
  # Regenerated on every install, which is also how config edits take effect.
  env_xml=$($SUDO sh -c "cat '$CONF'" | while IFS= read -r line; do
    case "$line" in (''|'#'*) continue ;; esac
    key=${line%%=*}
    value=${line#*=}
    case "$key" in (*[!A-Za-z0-9_]*) continue ;; esac
    printf '    <key>%s</key><string>%s</string>\n' "$key" "$value"
  done)

  $SUDO sh -c "cat > '$PLIST'" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>tech.blockreign.gnodi-ai-node</string>
  <key>ProgramArguments</key><array><string>$BIN</string></array>
  <key>EnvironmentVariables</key><dict>
$env_xml    <key>STATE_DIR</key><string>/usr/local/var/gnodi-ai-node</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/usr/local/var/log/gnodi-ai-node.log</string>
  <key>StandardErrorPath</key><string>/usr/local/var/log/gnodi-ai-node.log</string>
</dict>
</plist>
EOF
  # The plist now carries the licence key, so lock it down like the config.
  $SUDO chmod 600 "$PLIST"
  $SUDO launchctl unload "$PLIST" 2>/dev/null || true
  $SUDO launchctl load -w "$PLIST"
  info "service started — tail -f /usr/local/var/log/gnodi-ai-node.log"
  info "after editing $CONF, re-run this installer to apply it"

else
  info "no supported service manager found; run $BIN with the vars from $CONF"
fi

echo
bold "Done."
info "Dashboard:  http://127.0.0.1:8080"
info "Config:     $CONF"
info "Models are pulled automatically from the signed catalog on first start."
