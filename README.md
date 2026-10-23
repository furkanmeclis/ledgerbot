# ledgerbot

An independent assistant layer for B2B pre-accounting software. Users talk to it over
WhatsApp, Telegram or e-mail (text, photos, PDFs, e-invoice XML, voice notes). It works
out what the user wants, checks permissions, computes every number in code, and either
answers, asks for approval, or records the result through a pluggable data provider.

It has no UI. Everything is configured through four contracts the host system provides,
and everything is reachable through a versioned REST API.

```
 WhatsApp (WuzAPI per company / central, Meta Cloud)   Telegram   E-mail   REST /v1
                         │                                 │         │        │
                         ▼                                 ▼         ▼        ▼
              ┌──────────────────────── api (stateless, N replicas) ─────────────────┐
              │ webhooks → verify → durable queue        /v1 auth · rate limit · idem │
              └───────────────────────────────┬──────────────────────────────────────┘
                                              │ River (PostgreSQL, SKIP LOCKED)
              ┌───────────────────────────────▼──────────────────────────────────────┐
              │ worker (N replicas): understand → permit → capability → save mode     │
              │ rules & alerts · webhooks · listeners (leased) · deletion · simulator │
              └──────┬───────────────┬──────────────────┬───────────────┬────────────┘
                     │               │                  │               │
             settings + matrix   data provider     AI providers     PostgreSQL
             (host contract)     (fake | host API) (LLM/STT/vision) (+pgvector)
```

## Quick start

```bash
cp .env.example .env
docker compose up -d --build        # postgres, migrate, 2× api, 3× worker, gateway on :8080
./scripts/smoke.sh                  # end-to-end check against the demo company
```

Talk to it as the demo accountant (dev channel, fake data, offline AI):

```bash
KEY=lb_demo_demosecret0000000000000000
curl -s localhost:8080/v1/dev/messages -H "Authorization: Bearer $KEY" \
  -d '{"sender":"accountant","text":"Ahmet Yılmaz'\''dan 5.000 TL tahsilat"}'
curl -s localhost:8080/v1/dev/messages -H "Authorization: Bearer $KEY" \
  -d '{"sender":"accountant","text":"evet"}'
```

Real AI providers: copy `providers.example.json` to `providers.json`, set the API keys in
`.env`, and point `LEDGERBOT_PROVIDERS_FILE` at it.

## What it does today

| Area | Status |
|---|---|
| Intents | Deterministic Turkish/English rules first (≈2.5 µs, no cost); LLM fallback through LangChainGo (OpenAI, Anthropic, Ollama, and any OpenAI-compatible API such as Gemini, Groq, OpenRouter) |
| Capabilities | collection, payment, expense (photo, PDF, UBL-TR XML via go-ubltr), B2B order, balance, statement, stock, low stock, overdue, daily summary, help; chained commands ("X ve Y") |
| Reports & matching | Weekly P&L, monthly VAT estimate, morning digest (on demand or scheduled); collections matched to open invoices |
| Extensibility | Per-company plugin capabilities over signed HTTPS, with regex triggers and the same permission and save-mode rules |
| Observability | Prometheus metrics, OpenTelemetry traces across API → queue → assistant → AI (`docker compose --profile observability up` adds Jaeger on :16686) |
| Finance | Bank statement import (CSV, Excel, PDF) with matching, 30-day cash-flow forecast, anomaly flags on unusual or repeated amounts |
| Memory | "her zamanki gibi" repeats the last record; notes and records searchable through pgvector RAG, isolated per company |
| Save modes | `reply_only`, `ask_approval` (yes/no parsed without AI), `auto_save`; roles can only tighten; degraded settings never write |
| Media | Item-level receipt extraction (vision model), e-invoice line items, voice notes through pluggable STT |
| Safety | Duplicate documents, verbatim tool output, per-company AI quota, cost tracking per purpose/provider/model |
| Money | Integer kuruş everywhere, Turkish notation parser, daily FX to TRY (TCMB) |
| Channels | WuzAPI (per company / central with directory routing and "firma değiştir"), Meta WABA, Telegram (webhook or long polling), e-mail (IMAP polling or inbound JSON, SMTP replies), dev |
| Platform | REST API with OpenAPI, API keys + optional HMAC, idempotency keys, rate limits, signed outbound webhooks, audit log, KVKK/Meta data deletion, Prometheus metrics, health checks |
| Scale | Stateless api, redundant workers, per-company fairness, leased listeners with failover, dead letters, k6 load test with 4,000 companies |

See [docs/ROADMAP.md](docs/ROADMAP.md) for what comes next.

## Demo console

`dashboard/` is a separate demo-only web console (own image and compose) for
presenting ledgerbot without a live accounting system: channel views with live
replies, approvals, audit log, AI usage and all settings, with a DEMO watermark.

```bash
cd dashboard && docker compose up -d --build   # http://localhost:3000
```

## Documentation site

`docs-site/` is a password-protected documentation site (Next.js + Fumadocs, own image and
compose) covering the Easytrade integration, the contracts, the REST API (generated from the
OpenAPI files below) and operations. Production and staging base URLs are set by environment.

```bash
cd docs-site && cp .env.example .env    # set DOCS_PASSWORD, DOCS_PROD_URL, DOCS_STAGE_URL ...
docker compose up -d --build            # http://localhost:3001
```

## Contracts

| Contract | File |
|---|---|
| ledgerbot REST API | [docs/contracts/ledgerbot.openapi.yaml](docs/contracts/ledgerbot.openapi.yaml) |
| Host endpoints (settings, permissions, identities, data provider) | [docs/contracts/host.openapi.yaml](docs/contracts/host.openapi.yaml) |
| Settings | [docs/contracts/settings.schema.json](docs/contracts/settings.schema.json) |
| Permission matrix | [docs/contracts/permissions.schema.json](docs/contracts/permissions.schema.json) |
| Events and webhooks | [docs/contracts/event.schema.json](docs/contracts/event.schema.json) |

## Development

```bash
make test      # unit tests
make race      # race detector + PostgreSQL integration (needs compose postgres)
make lint      # gofmt, vet, golangci-lint
make bench     # benchmarks
make load      # 4,000-company k6 run (see docs/PERFORMANCE.md)
```

Processes: `ledgerbot migrate | api | worker | all`. Configuration is environment only
(see `.env.example`). Architecture notes: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
