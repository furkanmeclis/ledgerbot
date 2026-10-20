# Architecture

## Principles

1. **The model understands, code computes.** The LLM only returns an intent and raw
   argument strings. Amounts, VAT, FX, totals and balances are computed in Go from
   integer kuruş. Capability output is sent to the user verbatim.
2. **Rules before models.** Common phrasings are matched by deterministic rules in
   microseconds; the LLM is a fallback. Structured e-invoices never touch a model.
3. **Contracts, not coupling.** ledgerbot never reads the host database. It consumes
   four contracts (settings, permission matrix, data provider, events) and exposes one
   REST API. The host team decides if and how to integrate.
4. **Fail safe.** If settings are unreachable, the last snapshot is served in degraded
   mode and nothing is written. Roles can only make the save mode stricter.
5. **Performance first.** Hot paths avoid allocations and round trips: cached tenant
   snapshots (singleflight, stale-while-revalidate), batched audit writes (COPY),
   events published only when someone subscribed, media kept out of job rows.

## Packages

| Package | Responsibility |
|---|---|
| `domain` | Core types, money parsing/formatting |
| `tenant` | Settings + permission matrix contracts, cache, directory routing |
| `dataprovider` | Data contract; `fake` (deterministic seed + ledger) and `apidriver` (host REST) |
| `ai` | LLM / STT / embedding interfaces, LangChainGo adapter, OpenAI-compatible HTTP, fakes, registry with per-purpose profiles and pricing |
| `nlu` | Rule parser, LLM intent fallback, approval parsing, language detection |
| `assistant` | Engine: identity → intents → permission → capability → save mode → reply |
| `einvoice` | UBL-TR invoice parsing via go-ubltr |
| `fx` | TCMB daily rates with per-day cache |
| `channels` | Driver/Poller interfaces; WuzAPI, WABA, Telegram, e-mail, dev |
| `queue` | Job definitions on River |
| `worker` | Job workers, fairness limiter, leased listeners, simulator |
| `store` | PostgreSQL + in-memory stores, batched audit |
| `api` | HTTP: /v1, webhooks, Meta deletion, health, metrics |

## Message flow

1. A webhook (or poller, or `/v1/messages`) normalizes the payload to `InboundMessage`.
2. Media bytes go to a short-lived blob; the job row carries only the blob id.
3. A worker claims the job (`FOR UPDATE SKIP LOCKED`). If the company already has
   `PER_COMPANY_INFLIGHT` jobs running in this process, the job is snoozed for 250 ms.
4. The engine resolves the sender through the permission matrix, transcribes audio,
   routes documents to extraction, parses intents (rules → LLM), checks the role,
   runs each capability, applies duplicate detection and the save mode.
5. Replies become outbound jobs, retried independently of the AI work.
6. Outbound events (`draft.*`, `message.handled`) are enqueued only if a webhook subscribes.

## Scaling to thousands of companies

- **api**: stateless; scale horizontally behind any balancer.
- **worker**: identical replicas; River distributes jobs without a leader. Periodic jobs
  (cleanup, simulator) run once cluster-wide through River's leader election.
- **listeners** (Telegram polling, IMAP): each connection is leased in
  `channel_leases` (20 s TTL, renewed every 5 s). A dead replica's listeners move to
  another replica within one TTL. `LEDGERBOT_MAX_LISTENERS` caps a replica's share.
- **fairness**: per-company in-flight cap per process plus separate queues for inbound
  AI work, outbound sends, events and webhooks, so slow AI calls never block deliveries.
- **dead letters**: jobs that exhaust retries are `discarded`; list them with
  `GET /admin/jobs?state=discarded`.

## Data protection

- Channel credentials are encrypted with AES-256-GCM (`LEDGERBOT_SECRET_KEY`).
- Raw media is deleted after processing; only structured drafts remain.
- `POST /v1/deletion-requests` and the Meta callback erase a subject's drafts and redact
  audit entries that mention them.
- API keys are stored by the host as SHA-256 digests only.
