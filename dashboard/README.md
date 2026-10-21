# ledgerbot demo console

A separate, demo-only web console for presenting ledgerbot without a live
accounting system. It runs on the fake data provider, the event simulator and
the offline AI stubs. Every screen carries a visible **DEMO** watermark.

- **Channels**: WhatsApp, Telegram, e-mail and REST API views, each simulated on
  ledgerbot's dev channel, with quick commands, sample receipt / e-invoice /
  bank statement, voice notes, and live replies (Socket.IO).
- **What ledgerbot understood**: intents, their source (rules, LLM, plugin,
  document), arguments and the drafts created, with approve and reject.
- **Approvals, Activity (live audit log), AI usage and quota.**
- **Settings**: every settings parameter, the permission matrix (capabilities ×
  roles), users and channel addresses, rules, scheduled reports, keys, webhooks,
  plugins and prompts, as ledgerbot sees them (secrets redacted).

## Run

```bash
docker compose up -d --build              # in the repo root: ledgerbot
cd dashboard && docker compose up -d --build
open http://localhost:3000
```

Development: `npm install`, then `LEDGERBOT_URL=http://localhost:8080 DATABASE_URL=postgres://ledgerbot:ledgerbot@localhost:5432/ledgerbot npm run dev`
(Vite on :5173 proxies to the server on :3000).

Stack: Node.js (Express, Socket.IO, pg), React 19, Tailwind CSS 4, shadcn/ui-style
components (Radix primitives, lucide icons).
