#!/usr/bin/env bash
# Heavy performance suite: runs the full stack under a total RAM budget per
# profile (default 2, 4, 6, 8 GB) and drives stepped k6 load against it.
#
#   scripts/perf/run-profiles.sh                       # all profiles, defaults
#   PERF_PROFILES="2 4" PERF_STEPS="10 50" PERF_STEP_DURATION=15s scripts/perf/run-profiles.sh
#
# A profile token with a "t" suffix (e.g. "2t") runs the same budget with a
# small-memory tuning: fewer PostgreSQL connections per process and lower
# worker concurrency, so the default and tuned behaviour can be compared.
#
# Per profile it records, under $PERF_OUT/<N>gb/:
#   step-<vus>.json   k6 summary (throughput, latency percentiles, errors)
#   river-<vus>.json  queue wait / run / end-to-end times of jobs created in the step
#   stats.jsonl       docker stats samples (memory, CPU) tagged with the step
#   containers.json   OOM kills and restarts at the end of the profile
#   profile.json      the limits that were applied
# It never aborts on a failed step: whatever was measured is kept for the report.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
OUT=${PERF_OUT:-$ROOT/reports/raw/perf}
PROFILES=${PERF_PROFILES:-"2 4 6 8 2t"}
STEPS=${PERF_STEPS:-"10 50 100 200 400 800"}
DURATION=${PERF_STEP_DURATION:-30s}
COMPANIES=${PERF_COMPANIES:-4000}
DRAIN_TIMEOUT=${PERF_DRAIN_TIMEOUT:-180}
K6_IMAGE=${K6_IMAGE:-grafana/k6:1.3.0}
export PERF_PROJECT=${PERF_PROJECT:-ledgerbot-perf}
export PERF_IMAGE=${PERF_IMAGE:-ledgerbot:perf}
export PERF_SEED_DIR=$ROOT/loadtest/seed
COMPOSE=(docker compose -f "$ROOT/loadtest/perf/compose.yml")
NET="${PERF_PROJECT}_default"

log() { printf '[perf %s] %s\n' "$(date +%H:%M:%S)" "$*"; }
psql_q() { "${COMPOSE[@]}" exec -T postgres psql -U ledgerbot -d ledgerbot -At -c "$1" 2>/dev/null; }

mkdir -p "$OUT"
host_mem=$(docker info --format '{{.MemTotal}}' 2>/dev/null || echo 0)
host_cpus=$(docker info --format '{{.NCPU}}' 2>/dev/null || echo 0)
cat >"$OUT/meta.json" <<EOF
{"started":"$(date -u +%FT%TZ)","host_mem_bytes":$host_mem,"host_cpus":$host_cpus,
 "docker":"$(docker version --format '{{.Server.Version}}' 2>/dev/null)","os":"$(uname -sm)",
 "steps":"$STEPS","step_duration":"$DURATION","companies":$COMPANIES,"runner":"${RUNNER_NAME:-local}",
 "commit":"$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null)"}
EOF

if [ ! -f "$ROOT/loadtest/seed/load$(printf %04d "$COMPANIES").json" ]; then
  log "generating $COMPANIES companies"
  (cd "$ROOT" && go run ./cmd/seedgen -n "$COMPANIES" -out loadtest/seed) || log "seedgen failed"
fi
if [ -z "${PERF_SKIP_BUILD:-}" ]; then
  log "building $PERF_IMAGE"
  docker build -q -t "$PERF_IMAGE" "$ROOT" >/dev/null || { log "image build failed"; echo '{"error":"image build failed"}' >"$OUT/build-error.json"; exit 0; }
fi

sample_stats() { # $1 = file, $2 = step label file
  while :; do
    local step; step=$(cat "$2" 2>/dev/null)
    docker stats --no-stream --format '{{json .}}' $(docker ps -q --filter "label=com.docker.compose.project=$PERF_PROJECT") 2>/dev/null |
      while read -r line; do printf '{"t":%s,"step":"%s","s":%s}\n' "$(date +%s)" "$step" "$line"; done >>"$1"
    sleep 1
  done
}

for token in $PROFILES; do
  gb=${token%t}; tuned=false
  if [ "$token" != "$gb" ]; then
    tuned=true; dir="$OUT/${gb}gb-tuned"
    export PERF_DB_MAX_CONNS=${PERF_TUNED_DB_MAX_CONNS:-10} PERF_WORKER_CONCURRENCY=${PERF_TUNED_WORKER_CONCURRENCY:-32}
  else
    dir="$OUT/${gb}gb"
    export PERF_DB_MAX_CONNS=40 PERF_WORKER_CONCURRENCY=64
  fi
  rm -rf "$dir"; mkdir -p "$dir"
  mb=$((gb * 1024))
  pg=$((mb * 35 / 100)); api=$((mb * 10 / 100)); wk=$((mb * 12 / 100)); gw=$((mb * 6 / 100))
  [ "$gw" -lt 160 ] && gw=160
  export PERF_PG_MEM="${pg}m" PERF_API_MEM="${api}m" PERF_WORKER_MEM="${wk}m" PERF_GATEWAY_MEM="${gw}m"
  export PERF_PG_SHARED_BUFFERS="$((pg / 4))MB" PERF_PG_CACHE="$((pg / 2))MB" PERF_PG_MAX_CONNS=250
  export PERF_API_GOMEMLIMIT="$((api * 85 / 100))MiB" PERF_WORKER_GOMEMLIMIT="$((wk * 85 / 100))MiB"
  export PERF_API_REPLICAS=${PERF_API_REPLICAS:-2} PERF_WORKER_REPLICAS=${PERF_WORKER_REPLICAS:-3}
  capped=false; [ "$host_mem" -gt 0 ] && [ $((mb * 1024 * 1024)) -gt "$host_mem" ] && capped=true
  cat >"$dir/profile.json" <<EOF
{"gb":$gb,"tuned":$tuned,"name":"$(basename "$dir")","budget_mb":$mb,"db_max_conns":$PERF_DB_MAX_CONNS,"worker_concurrency":$PERF_WORKER_CONCURRENCY,"postgres_mb":$pg,"api_mb":$api,"api_replicas":$PERF_API_REPLICAS,
 "worker_mb":$wk,"worker_replicas":$PERF_WORKER_REPLICAS,"gateway_mb":$gw,
 "pg_shared_buffers":"$PERF_PG_SHARED_BUFFERS","api_gomemlimit":"$PERF_API_GOMEMLIMIT",
 "worker_gomemlimit":"$PERF_WORKER_GOMEMLIMIT","capped_by_host":$capped}
EOF
  log "profile $(basename "$dir"): postgres ${pg}m, api ${PERF_API_REPLICAS}x${api}m, worker ${PERF_WORKER_REPLICAS}x${wk}m, gateway ${gw}m"
  "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1
  if ! "${COMPOSE[@]}" up -d >"$dir/up.log" 2>&1; then log "compose up failed"; fi
  if ! docker run --rm --network "$NET" -v "$ROOT/loadtest/perf:/scripts:ro" "$K6_IMAGE" run -q /scripts/ready.js >"$dir/ready.log" 2>&1; then
    log "stack not ready, skipping profile"; echo '{"error":"stack not ready"}' >"$dir/error.json"
    "${COMPOSE[@]}" logs --tail 200 >"$dir/compose.log" 2>&1; "${COMPOSE[@]}" down -v >/dev/null 2>&1; continue
  fi
  echo idle >"$dir/.step"
  sample_stats "$dir/stats.jsonl" "$dir/.step" &
  sampler=$!
  docker events --format '{{json .}}' --filter type=container --filter event=oom --filter event=die \
    --filter "label=com.docker.compose.project=$PERF_PROJECT" >"$dir/events.jsonl" 2>/dev/null &
  watcher=$!
  for vus in $STEPS; do
    echo "$vus" >"$dir/.step"
    start=$(psql_q "SELECT to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')")
    log "  step $vus VUs for $DURATION"
    docker run --rm --network "$NET" -v "$ROOT/loadtest/perf:/scripts:ro" -v "$dir:/out" \
      -e VUS="$vus" -e DURATION="$DURATION" -e COMPANIES="$COMPANIES" -e OUT="step-$vus.json" \
      "$K6_IMAGE" run -q /scripts/step.js >"$dir/k6-$vus.log" 2>&1 || log "  k6 exited non-zero"
    stop=$(psql_q "SELECT to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')")
    echo "drain-$vus" >"$dir/.step"
    t0=$(date +%s); drained=false
    while [ $(( $(date +%s) - t0 )) -lt "$DRAIN_TIMEOUT" ]; do
      left=$(psql_q "SELECT count(*) FROM river_job WHERE created_at >= '$start' AND state IN ('available','running','retryable','scheduled','pending')")
      [ "${left:-1}" = "0" ] && { drained=true; break; }
      sleep 1
    done
    drain=$(( $(date +%s) - t0 ))
    psql_q "SELECT json_build_object('start','$start','stop','$stop','drain_s',$drain,'drained',$drained,'queues',
      coalesce(json_agg(q),'[]'::json)) FROM (
        SELECT queue, count(*) AS jobs,
          count(*) FILTER (WHERE state='completed') AS completed,
          count(*) FILTER (WHERE state IN ('discarded','cancelled')) AS failed,
          count(*) FILTER (WHERE attempt > 1) AS retried,
          percentile_cont(ARRAY[0.5,0.95,0.99]) WITHIN GROUP (ORDER BY extract(epoch FROM attempted_at-created_at)*1000) AS wait_ms,
          percentile_cont(ARRAY[0.5,0.95,0.99]) WITHIN GROUP (ORDER BY extract(epoch FROM finalized_at-attempted_at)*1000) AS run_ms,
          percentile_cont(ARRAY[0.5,0.95,0.99]) WITHIN GROUP (ORDER BY extract(epoch FROM finalized_at-created_at)*1000) AS e2e_ms,
          max(extract(epoch FROM finalized_at-created_at)*1000) AS e2e_max_ms
        FROM river_job WHERE created_at >= '$start' AND created_at <= '$stop' GROUP BY queue) q" >"$dir/river-$vus.json"
    log "  drained=$drained in ${drain}s"
  done
  kill "$sampler" "$watcher" 2>/dev/null; wait "$sampler" "$watcher" 2>/dev/null
  docker inspect $(docker ps -aq --filter "label=com.docker.compose.project=$PERF_PROJECT") \
    --format '{"name":"{{.Name}}","service":"{{index .Config.Labels "com.docker.compose.service"}}","oom_killed":{{.State.OOMKilled}},"restarts":{{.RestartCount}},"status":"{{.State.Status}}","exit_code":{{.State.ExitCode}}}' \
    2>/dev/null | paste -sd, - | sed 's/^/[/;s/$/]/' >"$dir/containers.json"
  "${COMPOSE[@]}" logs --no-color --tail 300 >"$dir/compose.log" 2>&1
  "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1
  rm -f "$dir/.step"
done
log "done: $OUT"
