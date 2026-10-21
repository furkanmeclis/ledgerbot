// ledgerbot demo dashboard server.
//
// A small backend-for-frontend: the browser never sees the ledgerbot API key.
// It proxies a few ledgerbot REST calls, simulates channels on top of the dev
// channel, and pushes live updates over Socket.IO:
//   - "outbox": replies and alerts sent to a demo user (PostgreSQL LISTEN on
//     ledgerbot_dev, then GET /v1/dev/outbox)
//   - "audit":  new audit log entries (polled)
//   - "chat":   messages typed in any browser, so every viewer sees the same demo
import express from 'express';
import { createServer } from 'node:http';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import pg from 'pg';
import { Server } from 'socket.io';

const PORT = Number(process.env.PORT || 3000);
const LEDGERBOT_URL = (process.env.LEDGERBOT_URL || 'http://localhost:8080').replace(/\/$/, '');
const COMPANIES = (process.env.DEMO_COMPANIES || 'demo:lb_demo_demosecret0000000000000000,demo2:lb_demo2_demosecret0000000000000000')
  .split(',')
  .map((s) => s.trim().split(':'))
  .filter(([id, key]) => id && key);
const KEYS = Object.fromEntries(COMPANIES);
const DATABASE_URL = process.env.DATABASE_URL || '';

const app = express();
app.use(express.json({ limit: '25mb' }));
const http = createServer(app);
const io = new Server(http, { cors: { origin: false } });

async function lb(company, method, p, body) {
  const key = KEYS[company];
  if (!key) throw Object.assign(new Error('unknown company'), { status: 404 });
  const res = await fetch(LEDGERBOT_URL + p, {
    method,
    headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  });
  const json = await res.json().catch(() => ({}));
  if (!res.ok) throw Object.assign(new Error(json?.error?.message || res.statusText), { status: res.status });
  return json.data;
}

const wrap = (fn) => async (req, res) => {
  try {
    res.json(await fn(req));
  } catch (e) {
    res.status(e.status || 500).json({ error: e.message });
  }
};

app.get('/api/companies', (_req, res) => res.json(COMPANIES.map(([id]) => id)));

app.get('/api/:company/state', wrap(async (req) => {
  const c = req.params.company;
  const [settings, caps, usage, drafts, channels] = await Promise.all([
    lb(c, 'GET', '/v1/settings'),
    lb(c, 'GET', '/v1/capabilities'),
    lb(c, 'GET', '/v1/usage'),
    lb(c, 'GET', '/v1/drafts?limit=50'),
    lb(c, 'GET', '/v1/channels'),
  ]);
  return { settings, capabilities: caps, usage, drafts: drafts.drafts || [], channels };
}));

app.get('/api/:company/audit', wrap((req) => lb(req.params.company, 'GET', '/v1/audit?limit=100')));

app.get('/api/:company/outbox', wrap((req) =>
  lb(req.params.company, 'GET', `/v1/dev/outbox?to=${encodeURIComponent(req.query.to || '')}`)));

// Channels are simulated on the dev channel; `channel` only shapes the UI.
app.post('/api/:company/send', wrap(async (req) => {
  const c = req.params.company;
  const { sender, text, media, channel } = req.body || {};
  const at = new Date().toISOString();
  io.emit('chat', { company: c, channel, sender, direction: 'in', text: text || (media ? `📎 ${media.filename || media.mime}` : ''), at });
  const reply = await lb(c, 'POST', '/v1/dev/messages', { sender, text, media });
  return reply;
}));

app.post('/api/:company/drafts/:id/:action', wrap((req) => {
  const { company, id, action } = req.params;
  if (action !== 'approve' && action !== 'reject') throw Object.assign(new Error('bad action'), { status: 400 });
  return lb(company, 'POST', `/v1/drafts/${encodeURIComponent(id)}/${action}`, { actor_id: req.body?.actor_id });
}));

app.post('/api/:company/simulate', wrap((req) => lb(req.params.company, 'POST', '/v1/simulator/run')));

app.post('/api/:company/events', wrap((req) => lb(req.params.company, 'POST', '/v1/events', req.body)));

// Static web app.
const here = path.dirname(fileURLToPath(import.meta.url));
const dist = path.join(here, '..', 'dist');
app.use(express.static(dist, { maxAge: '1h', index: false }));
app.get(/^(?!\/api|\/socket\.io).*/, (_req, res) => res.sendFile(path.join(dist, 'index.html')));

// --- live updates ------------------------------------------------------------

const lastAudit = new Map(); // company -> last seen audit id
async function pollAudit() {
  for (const [c] of COMPANIES) {
    try {
      const { entries = [] } = await lb(c, 'GET', '/v1/audit?limit=30');
      const seen = lastAudit.get(c) ?? 0;
      const fresh = entries.filter((e) => e.id > seen).reverse();
      if (fresh.length) {
        lastAudit.set(c, fresh[fresh.length - 1].id);
        if (seen !== 0) io.emit('audit', { company: c, entries: fresh });
      } else if (!lastAudit.has(c)) {
        lastAudit.set(c, entries[0]?.id ?? 0);
      }
    } catch {
      /* ledgerbot not up yet */
    }
  }
}
setInterval(pollAudit, 1500);

async function listen() {
  if (!DATABASE_URL) return;
  const client = new pg.Client({ connectionString: DATABASE_URL });
  client.on('error', () => setTimeout(listen, 3000));
  try {
    await client.connect();
    await client.query('LISTEN ledgerbot_dev');
    client.on('notification', async (n) => {
      const [company, to] = String(n.payload || '').split('|');
      if (!KEYS[company]) return;
      try {
        const { messages = [] } = await lb(company, 'GET', `/v1/dev/outbox?to=${encodeURIComponent(to)}`);
        io.emit('outbox', { company, to, messages });
      } catch {
        /* ignore */
      }
    });
    console.log('listening for dev outbox notifications');
  } catch (e) {
    console.warn('LISTEN failed, retrying:', e.message);
    setTimeout(listen, 3000);
  }
}
listen();

http.listen(PORT, () => console.log(`dashboard on :${PORT} → ${LEDGERBOT_URL}`));
