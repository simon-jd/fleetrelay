#!/usr/bin/env sh
set -eu

SCENARIO=${1:-}
COMPOSE_FILE=${COMPOSE_FILE:-deploy/compose/docker-compose.yml}
RESULT_DIR=${RESULT_DIR:-docs/evidence/failures}
RECOVERY_SERVICE=

usage() {
  echo "usage: $0 gateway|redis|postgres" >&2
  exit 2
}

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 127; }
docker compose version >/dev/null 2>&1 || { echo "Docker Compose v2 is required" >&2; exit 127; }
[ -f "$COMPOSE_FILE" ] || { echo "compose file not found: $COMPOSE_FILE" >&2; exit 2; }

case "$SCENARIO" in
  gateway) RECOVERY_SERVICE=gateway-a ;;
  redis) RECOVERY_SERVICE=redis ;;
  postgres) RECOVERY_SERVICE=postgres ;;
  *) usage ;;
esac

mkdir -p "$RESULT_DIR"
STARTED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
RUN_ID=$(date -u +%Y%m%dT%H%M%SZ)-${SCENARIO}
RESULT_FILE="$RESULT_DIR/${RUN_ID}.json"
RECOVERED=false

recover() {
  if [ -n "$RECOVERY_SERVICE" ]; then
    docker compose -f "$COMPOSE_FILE" up -d "$RECOVERY_SERVICE" >/dev/null 2>&1 || true
  fi
}
trap recover EXIT INT TERM

docker compose -f "$COMPOSE_FILE" ps --status running >/dev/null
docker compose -f "$COMPOSE_FILE" stop "$RECOVERY_SERVICE" >/dev/null

case "$SCENARIO" in
  gateway)
    EXPECTATION="devices reconnect through gateway-b without duplicate side effects"
    sleep 15
    if curl --fail --silent --show-error http://localhost:8083/healthz >/dev/null; then RECOVERED=true; fi
    ;;
  redis)
    EXPECTATION="accepted commands remain durable and transport resumes after Redis recovery"
    sleep 30
    docker compose -f "$COMPOSE_FILE" up -d redis >/dev/null
    sleep 10
    if docker compose -f "$COMPOSE_FILE" exec -T redis redis-cli ping 2>/dev/null | grep -q PONG; then RECOVERED=true; fi
    ;;
  postgres)
    EXPECTATION="new leases and commands fail closed until PostgreSQL recovers"
    sleep 10
    docker compose -f "$COMPOSE_FILE" up -d postgres >/dev/null
    sleep 10
    if docker compose -f "$COMPOSE_FILE" exec -T postgres pg_isready -U "${POSTGRES_USER:-fleetrelay}" 2>/dev/null | grep -q accepting; then RECOVERED=true; fi
    ;;
esac

FINISHED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
cat >"$RESULT_FILE" <<EOF
{
  "schema_version": 1,
  "run_id": "$RUN_ID",
  "scenario": "$SCENARIO",
  "started_at": "$STARTED_AT",
  "finished_at": "$FINISHED_AT",
  "expected_behavior": "$EXPECTATION",
  "dependency_recovered": $RECOVERED,
  "verification_scope": "infrastructure availability only",
  "note": "Inspect Grafana and audit events to verify application-level invariants."
}
EOF

recover
trap - EXIT INT TERM
echo "$RESULT_FILE"
[ "$RECOVERED" = true ]
