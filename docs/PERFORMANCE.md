# Performance

## Micro benchmarks (Apple M2)

| Benchmark | Result |
|---|---|
| `nlu.ParseRules` (Turkish collection sentence) | ~2.5 µs, 6 allocs |
| `domain.ParseAmount` | sub-microsecond |
| `einvoice.Parse` (GİB sample invoice) | see `make bench` |

## Load test

Setup: one MacBook (Apple M2, 8 cores, Docker Desktop with 8 GB) running PostgreSQL,
2 api replicas, 3 worker replicas, the gateway and k6 at the same time. 4,000 generated
companies, random company per request. Mix: 60% synchronous queries, 25% asynchronous
collections (auto-save, queued), 15% asynchronous chained commands.

```bash
make load    # go run ./cmd/seedgen -n 4000; compose with loadtest overlay; k6
```

| VUs | Throughput | Errors | p95 sync | p95 async (enqueue) |
|---|---|---|---|---|
| 50 | ~1,600 req/s | 0% | ~96 ms | ~54 ms |
| 300 | ~1,100 req/s | 0% | ~610 ms | ~350 ms |

At 300 VUs the laptop is CPU bound (PostgreSQL and k6 share the same 8 cores), so
latency is queueing. Even the 50-VU rate is far above a realistic peak for 4,000
companies (≈100,000 messages per minute).

## Changes that came out of profiling

- Outbound events are published only when the company has a matching webhook. Before
  this, every message produced ~3 event jobs that nobody consumed; PostgreSQL was the
  bottleneck at 300 VUs.
- Audit rows are written in batches with COPY (500 rows or 100 ms).
- Media is stored as a short-lived blob; job rows stay small.
- Tenant snapshots are cached with singleflight and stale-while-revalidate.

## Tuning knobs

| Variable | Default | Effect |
|---|---|---|
| `LEDGERBOT_WORKER_CONCURRENCY` | 64 | Max concurrent jobs per queue per worker |
| `LEDGERBOT_PER_COMPANY_INFLIGHT` | 4 | Fairness cap per company per worker |
| `LEDGERBOT_DB_MAX_CONNS` | 40 | pgx pool size per process |
| `LEDGERBOT_TENANT_TTL` | 30s | Settings cache lifetime |
| `LEDGERBOT_RATE_RPS` / `_BURST` | 20 / 40 | Per company REST limit per api replica |
