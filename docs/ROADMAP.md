# Roadmap

Each item ships as its own pull request with CI green.

## Done

- **Memory and RAG**: "her zamanki gibi [tutar]" repeats the user's latest record;
  "not al: ..." stores company notes; "notlarda X ara" / "X hakkında ne biliyorsun"
  searches notes and committed records. Per-company pgvector chunks, HNSW expression
  index per embedding dimension, embeddings computed by workers off the hot path,
  answers returned verbatim (no generated text).

## Next

- **Document indexing**: index extracted receipts and e-invoices into RAG.
- **Multi-step plans with the LLM**: chained commands beyond the rule parser, with a
  bounded tool loop and per-step permission checks.
- **Payment ↔ invoice matching**: match incoming payments to open invoices.
- **Scheduled reports**: weekly P&L summary, monthly VAT estimate, morning digest.
- **Plugin capabilities**: register capabilities as webhooks from settings
  (`capabilities: [{name, url, secret, write}]`).
- **OpenTelemetry traces** next to the existing Prometheus metrics.
- **IMAP listener** for mailboxes (the e-mail driver already accepts inbound-parse webhooks and sends via SMTP).
- **Company switching** for users that belong to several companies on a central number.

## Accounting

- **Bank statement import** (PDF / Excel) with transaction matching.
- **Cash-flow forecast**: "your cash may go negative in the next 30 days".
- **Anomaly detection**: unusual amounts, frequently repeated expenses.

## Later

- MCP server exposing the same capabilities.
- Demo dashboard (separate compose and image): Node.js + Socket.IO realtime, shadcn UI,
  one view per channel, every settings parameter visible, English UI, "Demo" watermark,
  running on the fake data driver and simulator.
