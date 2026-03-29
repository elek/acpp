#!/usr/bin/env bash
set -euo pipefail

DB_NAME="${ACPP_DB_NAME:-acpp}"
DB_USER="${ACPP_DB_USER:-acpp}"
DB_PASS="${ACPP_DB_PASS:-acpp}"

echo "Creating database '$DB_NAME' and user '$DB_USER'..."

sudo -u postgres psql <<SQL
CREATE USER ${DB_USER} WITH PASSWORD '${DB_PASS}';
CREATE DATABASE ${DB_NAME} OWNER ${DB_USER};
GRANT ALL PRIVILEGES ON DATABASE ${DB_NAME} TO ${DB_USER};
SQL

echo "Done. Connection string:"
echo "  postgres://${DB_USER}:${DB_PASS}@localhost:5432/${DB_NAME}?sslmode=disable"
