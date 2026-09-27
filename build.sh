#!/usr/bin/env bash
# Builds the omamessagesd daemon into bin/. Needs Go 1.26+ (omarchy pkg add go,
# or `mise use -g go@latest`).
set -euo pipefail
cd "$(dirname "$0")"
if ! command -v go >/dev/null 2>&1; then
  if [ -x "$HOME/.local/share/mise/shims/go" ]; then
    export PATH="$HOME/.local/share/mise/shims:$PATH"
  else
    echo "go is not installed. Run: omarchy pkg add go" >&2
    exit 1
  fi
fi
mkdir -p bin
# WhatsApp's store is SQLite through cgo, so a C compiler (gcc) is needed.
if ! command -v gcc >/dev/null 2>&1; then
  echo "gcc is not installed (needed for WhatsApp's SQLite store). Run: omarchy pkg add gcc" >&2
  exit 1
fi
( cd daemon && CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o ../bin/omamessagesd . )
echo "built bin/omamessagesd"
# Pick up the new binary in the running shell, if the running daemon is this
# copy's (a build of another copy must not restart it).
if pgrep -f "^$PWD/bin/omamessagesd serve" >/dev/null 2>&1; then
  bin/omamessagesd quit >/dev/null 2>&1 || true
  omarchy-shell -q shell rescanPlugins || true
fi
