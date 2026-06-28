#!/usr/bin/env bash
# Full automated Android end-to-end run:
#   1. bring up postgres + acpp web (running `rai acp fake`)
#   2. install + run the instrumented test on the connected adb device
#      (emulator or real phone)
#   3. tear everything down
#
# A device/emulator must already be connected (check with `adb devices`).
#
# By default the device reaches the server through an `adb reverse` tunnel: the
# device's own 127.0.0.1:PORT is forwarded over USB back to the host's server.
# This works for emulators AND real phones with no extra setup. If you'd rather
# the device talk to the host directly over the LAN (phone on the same network),
# give it the host's IP — no tunnel is set up then:
#
#   HOST_IP=192.168.1.42 ./run-e2e.sh
#
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=_common.sh
source "$DIR/_common.sh"

require adb
require curl

# How the *device* reaches the server. Default: an `adb reverse` tunnel so the
# device's loopback hits the host server over USB (set up below, after a device
# is confirmed). HOST_IP overrides this with a direct LAN address (no tunnel).
SERVER_PORT="${SERVER_ADDR#:}"
if [ -n "${HOST_IP:-}" ]; then
  BASE_URL="http://${HOST_IP}:${SERVER_PORT}"
  USE_REVERSE=0
else
  BASE_URL="http://127.0.0.1:${SERVER_PORT}"
  USE_REVERSE=1
fi
# The session's working directory — a path on THIS host (where acpp runs).
PROJECT_DIR="${PROJECTS_DIR}/demo"

SERVER_PID=""
cleanup() {
  if [ -n "$SERVER_PID" ]; then kill "$SERVER_PID" 2>/dev/null || true; fi
  [ "${USE_REVERSE:-0}" = 1 ] && adb reverse --remove "tcp:${SERVER_PORT}" >/dev/null 2>&1 || true
  adb shell svc power stayon false >/dev/null 2>&1 || true
  compose down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Guarantee a fresh database even if a previous run was interrupted.
compose down -v >/dev/null 2>&1 || true

# --- backend ---
db_up
build_acpp
render_config
export XDG_CONFIG_HOME="$RUN_DIR"
echo ">> starting acpp web (background) -> $RUN_DIR/server.log"
"$ACPP_BIN" web --addr "$SERVER_ADDR" >"$RUN_DIR/server.log" 2>&1 &
SERVER_PID=$!
wait_health

# --- device ---
if ! adb get-state >/dev/null 2>&1; then
  echo "error: no adb device found. Start an emulator or connect a phone (adb devices)." >&2
  exit 1
fi
echo ">> device: $(adb devices | sed -n '2p')"

# Wake and unlock the device. A dozing/locked screen means MainActivity never
# composes, and the instrumented test fails immediately with "No compose
# hierarchies found in the app". `dismiss-keyguard` only clears a non-secure
# lock; a PIN/password must be removed (or the device unlocked) by hand.
echo ">> waking device + dismissing keyguard"
adb shell input keyevent KEYCODE_WAKEUP >/dev/null 2>&1 || true
adb shell wm dismiss-keyguard           >/dev/null 2>&1 || true
adb shell svc power stayon true          >/dev/null 2>&1 || true

# Tunnel the device's loopback back to the host server over USB (unless a LAN
# HOST_IP was given). Lets a real phone — not just an emulator — reach acpp.
if [ "$USE_REVERSE" = 1 ]; then
  echo ">> adb reverse tcp:${SERVER_PORT} -> host:${SERVER_PORT}"
  adb reverse "tcp:${SERVER_PORT}" "tcp:${SERVER_PORT}"
fi

# Fresh app state so the test begins on the Setup screen.
adb uninstall net.anzix.acpp      >/dev/null 2>&1 || true
adb uninstall net.anzix.acpp.test >/dev/null 2>&1 || true

echo ">> running instrumented e2e"
echo ">>   ACPP_BASE_URL=$BASE_URL"
echo ">>   ACPP_PROJECT_DIR=$PROJECT_DIR"
( cd "$REPO_ROOT/android" && ./gradlew --console=plain :app:connectedDebugAndroidTest \
    -Pandroid.testInstrumentationRunnerArguments.ACPP_BASE_URL="$BASE_URL" \
    -Pandroid.testInstrumentationRunnerArguments.ACPP_PROJECT_DIR="$PROJECT_DIR" )

echo
echo ">> PASS"
echo ">> report: android/app/build/reports/androidTests/connected/debug/index.html"
