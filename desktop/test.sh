#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

SCREENSHOT="/tmp/acpp-desktop-screenshot.png"
BIN="$SCRIPT_DIR/build/bin/acpp-desktop"

export DISPLAY="${DISPLAY:-:0}"
export WEBKIT_DISABLE_DMABUF_RENDERER=1
export WEBKIT_DISABLE_COMPOSITING_MODE=1
export LIBGL_ALWAYS_SOFTWARE=1

echo "=== Building ==="
wails build -tags webkit2_41

if [ ! -f "$BIN" ]; then
  echo "ERROR: Binary not found at $BIN"
  exit 1
fi

echo "=== Starting app ==="
"$BIN" &
APP_PID=$!

cleanup() {
  echo "=== Cleaning up ==="
  kill "$APP_PID" 2>/dev/null || true
  wait "$APP_PID" 2>/dev/null || true
}
trap cleanup EXIT

echo "=== Waiting for window ==="
WINDOW_ID=""
for i in $(seq 1 30); do
  WINDOW_ID=$(xdotool search --name "ACPP" 2>/dev/null | head -1) || true
  if [ -n "$WINDOW_ID" ]; then
    break
  fi
  sleep 0.5
done

if [ -z "$WINDOW_ID" ]; then
  echo "ERROR: Window not found after 15 seconds"
  exit 1
fi

echo "Found window: $WINDOW_ID"

echo "=== Waiting for content to load ==="
sleep 3

echo "=== Taking screenshot ==="
import -window "$WINDOW_ID" "$SCREENSHOT"

echo "=== Screenshot saved to $SCREENSHOT ==="
