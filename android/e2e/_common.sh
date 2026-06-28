#!/usr/bin/env bash
# Shared configuration and helpers for the Android e2e backend harness.
# Sourced by start-backend.sh, stop-backend.sh and run-e2e.sh.
set -euo pipefail

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$E2E_DIR/../.." && pwd)"
RUN_DIR="$E2E_DIR/.run"
PROJECTS_DIR="$E2E_DIR/projects"
COMPOSE_FILE="$E2E_DIR/docker-compose.yml"

# Off-default ports/credentials so this never collides with a developer's own
# postgres (5432) or acpp server (e.g. :6060).
DB_HOST=127.0.0.1
DB_PORT=5433
DB_USER=acpp
DB_PASS=acpp
DB_NAME=acpp
DB_DSN="postgres://${DB_USER}:${DB_PASS}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable"

SERVER_ADDR=":6061"
SERVER_HEALTH="http://127.0.0.1:6061/api/health"

# Keep the built binary out of $RUN_DIR/acpp — that path is the rendered config
# DIRECTORY ($XDG_CONFIG_HOME/acpp/config.yaml).
ACPP_BIN="$RUN_DIR/bin/acpp"

compose() { docker compose -f "$COMPOSE_FILE" "$@"; }

require() {
  command -v "$1" >/dev/null 2>&1 || { echo "error: '$1' not found in PATH" >&2; exit 1; }
}

db_up() {
  require docker
  echo ">> starting postgres (docker, host port ${DB_PORT})…"
  compose up -d
  echo ">> waiting for postgres…"
  for _ in $(seq 1 30); do
    if compose exec -T postgres pg_isready -U "$DB_USER" -d "$DB_NAME" >/dev/null 2>&1; then
      echo ">> postgres ready"
      return 0
    fi
    sleep 1
  done
  echo "error: postgres did not become ready in time" >&2
  compose logs postgres >&2 || true
  exit 1
}

build_acpp() {
  require go
  echo ">> building acpp -> $ACPP_BIN"
  mkdir -p "$(dirname "$ACPP_BIN")"
  ( cd "$REPO_ROOT" && go build -o "$ACPP_BIN" . )
}

render_config() {
  require rai
  local rai_bin go_bin cfg
  rai_bin="$(command -v rai)"
  go_bin="$(dirname "$rai_bin")"
  cfg="$RUN_DIR/acpp/config.yaml"
  mkdir -p "$RUN_DIR/acpp"
  echo ">> rendering config -> $cfg (agent: '${rai_bin} acp fake')"
  sed \
    -e "s#__DB_DSN__#${DB_DSN}#g" \
    -e "s#__WEB_ADDR__#${SERVER_ADDR}#g" \
    -e "s#__AGENT__#${rai_bin} acp fake#g" \
    -e "s#__GO_BIN__#${go_bin}#g" \
    -e "s#__PROJECTS_DIR__#${PROJECTS_DIR}#g" \
    "$E2E_DIR/config/acpp/config.yaml" > "$cfg"
}

wait_health() {
  require curl
  echo ">> waiting for acpp health at ${SERVER_HEALTH}…"
  for _ in $(seq 1 30); do
    if curl -fsS "$SERVER_HEALTH" >/dev/null 2>&1; then
      echo ">> acpp is up"
      return 0
    fi
    sleep 1
  done
  echo "error: acpp did not become healthy in time" >&2
  [ -f "$RUN_DIR/server.log" ] && tail -n 40 "$RUN_DIR/server.log" >&2 || true
  exit 1
}
