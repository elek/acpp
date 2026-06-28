#!/usr/bin/env bash
# Stop the e2e acpp server (if running) and tear down the postgres container.
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=_common.sh
source "$DIR/_common.sh"

echo ">> stopping acpp web…"
pkill -f "${ACPP_BIN} web" 2>/dev/null || true
# Undo what run-e2e.sh's KEEP_RUNNING path leaves behind (best effort; harmless
# if no device is attached or nothing was set up).
adb reverse --remove "tcp:${SERVER_ADDR#:}" >/dev/null 2>&1 || true
adb shell svc power stayon false             >/dev/null 2>&1 || true
echo ">> tearing down postgres…"
compose down -v
echo ">> done"
