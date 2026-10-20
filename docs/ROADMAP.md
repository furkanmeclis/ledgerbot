# Roadmap

Each item ships as its own pull request with CI green.

## Next

- **Memory and RAG**: per-company embeddings in `rag_chunks` (pgvector, HNSW per
  embedding dimension), indexed from documents, committed records and company notes.
  "Her zamanki gibi" suggests the usual supplier and category.
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
