#!/bin/sh
# Only disposable test services, loopback ports 15439/16389, no app volumes.
set -eu
cd "$(dirname "$0")/.."
FLEETRELAY_TEST_BIN_DIR=$(mktemp -d /tmp/fleetrelay-integration-bin.XXXXXX)
export FLEETRELAY_TEST_BIN_DIR
cleanup() {
  docker compose -f tests/integration/compose.yml down --volumes
  rm -rf "$FLEETRELAY_TEST_BIN_DIR"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
docker compose -f tests/integration/compose.yml up -d --wait
export FLEETRELAY_REQUIRE_INTEGRATION=1
export FLEETRELAY_TEST_DATABASE_URL='postgres://fleetrelay:integration-only@127.0.0.1:15439/fleetrelay_test?sslmode=disable'
export FLEETRELAY_TEST_REDIS_ADDR='127.0.0.1:16389'
psql -X "$FLEETRELAY_TEST_DATABASE_URL" --set ON_ERROR_STOP=1 --file tests/integration/migrations.sql
go build -race -o "$FLEETRELAY_TEST_BIN_DIR/" ./cmd/...
go test -race -count=1 -v ./internal/control/postgres ./cmd/dispatcher ./tests/integration
