#!/usr/bin/env bash
# Bring up the e2e backend (postgres + acpp serve running `rai acp fake`) and leave
# it running in the foreground. Use this for manual testing: start it, then drive
# the Android app by hand on an emulator or a real phone.
#
#   Emulator:   point the app at  http://10.0.2.2:6061
#   Real phone: point the app at  http://<this-host-LAN-IP>:6061
#               (phone must be on the same network; firewall must allow :6061)
#
# Ctrl-C stops the server. Postgres keeps running; tear it down with
# ./stop-backend.sh.
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=_common.sh
source "$DIR/_common.sh"

db_up
build_acpp
render_config

echo
echo ">> acpp serve starting on ${SERVER_ADDR}"
echo ">>   emulator   -> http://10.0.2.2:6061"
echo ">>   real phone -> http://<this-host-LAN-IP>:6061"
echo ">>   demo project dir (type into 'New session'): ${PROJECTS_DIR}/demo"
echo
export XDG_CONFIG_HOME="$RUN_DIR"
exec "$ACPP_BIN" serve --addr "$SERVER_ADDR"
