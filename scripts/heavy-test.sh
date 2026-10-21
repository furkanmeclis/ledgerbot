#!/usr/bin/env bash
# Runs every test suite, the benchmarks and the RAM-profile load test, then
# builds reports/index.html. Each stage runs even if an earlier one failed,
# and the report is always generated.
#
#   scripts/heavy-test.sh                 # everything (≈30 min on a laptop)
#   HEAVY_SKIP_PERF=1 scripts/heavy-test.sh
#   PERF_PROFILES="2 4" PERF_STEP_DURATION=15s scripts/heavy-test.sh
#
# Exit code: 0 when the report verdict is PASS or INCOMPLETE, 1 when any test
# failed (set HEAVY_STRICT=0 to always exit 0).
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
OUT=${REPORT_DIR:-$ROOT/reports}
RAW=$OUT/raw
rm -rf "$OUT"; mkdir -p "$RAW"
log() { printf '[heavy %s] %s\n' "$(date +%H:%M:%S)" "$*"; }

PG_NAME=""
cleanup() { [ -n "$PG_NAME" ] && docker rm -f "$PG_NAME" >/dev/null 2>&1; }
trap cleanup EXIT
if [ -z "${LEDGERBOT_TEST_DATABASE_URL:-}" ]; then
  PG_NAME=ledgerbot-heavy-pg-$$
  PORT=$((55000 + RANDOM % 900))
  log "starting PostgreSQL on 127.0.0.1:$PORT"
  if docker run -d --rm --name "$PG_NAME" -e POSTGRES_USER=ledgerbot -e POSTGRES_PASSWORD=ledgerbot \
      -e POSTGRES_DB=ledgerbot_test -p "127.0.0.1:$PORT:5432" pgvector/pgvector:pg17 >/dev/null; then
    for _ in $(seq 1 60); do docker exec "$PG_NAME" pg_isready -U ledgerbot >/dev/null 2>&1 && break; sleep 1; done
    sleep 1
    export LEDGERBOT_TEST_DATABASE_URL="postgres://ledgerbot:ledgerbot@127.0.0.1:$PORT/ledgerbot_test?sslmode=disable"
  else
    echo "PostgreSQL could not be started; integration tests were skipped." >>"$RAW/notes.txt"
  fi
fi

log "unit tests (race, coverage)"
go test -race -count=1 -json -coverprofile="$RAW/cover.out" ./internal/... ./cmd/... >"$RAW/go-unit.json" 2>"$RAW/go-unit.stderr"
go tool cover -func="$RAW/cover.out" >"$RAW/coverage.txt" 2>/dev/null

if [ -d tests/integration ]; then
  log "integration tests"
  go test -race -count=1 -json -p 1 ./tests/integration/... >"$RAW/go-integration.json" 2>"$RAW/go-integration.stderr"
fi
if [ -d tests/feature ]; then
  log "feature tests"
  go test -race -count=1 -json ./tests/feature/... >"$RAW/go-feature.json" 2>"$RAW/go-feature.stderr"
fi
if [ -d tests/simulation ]; then
  log "simulations (scale ${LEDGERBOT_SIM_SCALE:-1})"
  go test -count=1 -json -timeout 30m ./tests/simulation/... >"$RAW/go-simulation.json" 2>"$RAW/go-simulation.stderr"
fi

log "benchmarks"
go test -run '^$' -bench . -benchmem -benchtime "${BENCHTIME:-1s}" ./internal/... >"$RAW/bench.txt" 2>&1

if [ -z "${HEAVY_SKIP_PERF:-}" ]; then
  log "load test under RAM budgets"
  PERF_OUT="$RAW/perf" "$ROOT/scripts/perf/run-profiles.sh"
else
  echo "The RAM-profile load test was skipped (HEAVY_SKIP_PERF)." >>"$RAW/notes.txt"
fi

log "building report"
go run ./cmd/testreport -in "$RAW" -out "$OUT"
if [ "${HEAVY_STRICT:-1}" = "1" ] && grep -q '"verdict": "FAIL"' "$OUT/summary.json" 2>/dev/null; then
  exit 1
fi
exit 0
