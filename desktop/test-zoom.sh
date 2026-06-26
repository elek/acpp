#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

BIN="$SCRIPT_DIR/build/bin/acpp-desktop"
BEFORE="/tmp/acpp-zoom-before.png"
AFTER="/tmp/acpp-zoom-after.png"

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
  rm -f "$BEFORE" "$AFTER"
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
sleep 5

echo "=== Taking BEFORE screenshot ==="
import -window "$WINDOW_ID" "$BEFORE"

echo "=== Sending Ctrl+Shift++ (zoom in) ==="
xdotool windowactivate --sync "$WINDOW_ID"
sleep 0.5
# Send Ctrl+Shift+= three times for noticeable zoom change (+30%)
xdotool key --clearmodifiers ctrl+shift+equal
sleep 0.3
xdotool key --clearmodifiers ctrl+shift+equal
sleep 0.3
xdotool key --clearmodifiers ctrl+shift+equal
sleep 1

echo "=== Taking AFTER screenshot ==="
import -window "$WINDOW_ID" "$AFTER"

echo "=== Comparing screenshots ==="
# Count the number of different pixels between the two images.
# A significant zoom change should produce many differing pixels.
DIFF=$(compare -metric AE "$BEFORE" "$AFTER" /dev/null 2>&1) || true
echo "Pixel difference: $DIFF"

if [ "$DIFF" -gt 1000 ]; then
  echo "PASS: Screenshots differ significantly ($DIFF pixels) — zoom is working"
  exit 0
else
  echo "FAIL: Screenshots are too similar ($DIFF pixels) — zoom did not apply"
  echo "  Before: $BEFORE"
  echo "  After:  $AFTER"
  exit 1
fi
