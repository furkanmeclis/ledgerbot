# Roadmap

Each item ships as its own pull request with CI green.

## Done

- **Memory and RAG**: "her zamanki gibi [tutar]" repeats the user's latest record;
  "not al: ..." stores company notes; "notlarda X ara" / "X hakkında ne biliyorsun"
  searches notes and committed records. Per-company pgvector chunks, HNSW expression
  index per embedding dimension, embeddings computed by workers off the hot path,
  answers returned verbatim (no generated text).

- **Payment ↔ invoice matching**: collections are allocated to the customer's open
  invoices (exact amount first, then oldest due date); the remainder is an advance.
  Allocations travel with the record to the host.
- **Reports**: weekly P&L, monthly VAT estimate (output − input VAT) and a morning
  digest, on demand ("haftalık rapor", "KDV tahmini") or scheduled per company
  (`settings.reports`, local time zone, sent once per period and user).

- **Plugin capabilities**: `settings.plugins` declares HTTPS capabilities with
  optional regex patterns (no AI needed), signed requests, permission checks and
  save modes like built-ins.
- **OpenTelemetry traces**: HTTP request → queued job → assistant → capability → AI
  call, propagated through job args; enabled with `OTEL_EXPORTER_OTLP_ENDPOINT`.

- **Bank statement import**: CSV and Excel (header detection, Borç/Alacak or
  signed Tutar, Windows-1254) parsed without AI, PDF through the extraction model.
  Movements are matched to customers by invoice number, name in the description or
  a unique open-invoice amount, and committed as one batch draft.
- **Cash-flow forecast**: 30-day projection from cash position and upcoming
  receivables/payables; "nakit akışı" on demand, `cash_forecast` scheduled check
  that only messages when cash may go negative.
- **Anomaly detection**: amounts far above a counterparty's median and same-amount
  repeats within a week are flagged; flagged records always ask for approval and
  emit `anomaly.detected`.

## Next

- **Document indexing**: index extracted receipts and e-invoices into RAG.
- **Multi-step plans with the LLM**: chained commands beyond the rule parser, with a
  bounded tool loop and per-step permission checks.
- **IMAP listener** for mailboxes (the e-mail driver already accepts inbound-parse webhooks and sends via SMTP).
- **Company switching** for users that belong to several companies on a central number.

## Later

- MCP server exposing the same capabilities.
- Demo dashboard (separate compose and image): Node.js + Socket.IO realtime, shadcn UI,
  one view per channel, every settings parameter visible, English UI, "Demo" watermark,
  running on the fake data driver and simulator.
