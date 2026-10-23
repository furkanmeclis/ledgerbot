// Smoke test for a running docs site: the login gate, every page, and every
// internal link and anchor. Usage: DOCS_URL=http://localhost:3000 DOCS_PASSWORD=… node scripts/check-site.mjs
const base = process.env.DOCS_URL ?? 'http://localhost:3000';
const password = process.env.DOCS_PASSWORD;
if (!password) throw new Error('DOCS_PASSWORD is required');

const failures = [];
const check = (ok, msg) => ok || failures.push(msg);

// 1. The gate.
let res = await fetch(`${base}/docs`, { redirect: 'manual' });
check(res.status === 307 && res.headers.get('location')?.includes('/login'), `unauthenticated /docs: ${res.status}`);
res = await fetch(`${base}/api/environments`);
check(res.status === 401, `unauthenticated /api/environments: ${res.status}`);
res = await fetch(`${base}/api/health`);
check(res.status === 200, `/api/health: ${res.status}`);

const login = (pw, next = '/docs') =>
  fetch(`${base}/api/auth/login`, { method: 'POST', body: new URLSearchParams({ password: pw, next }), redirect: 'manual' });
res = await login('definitely-wrong');
check(res.status === 303 && res.headers.get('location')?.includes('error=invalid'), `wrong password: ${res.status}`);
res = await login(password, '//evil.example');
check(res.headers.get('location') === '/docs', `open redirect guard: ${res.headers.get('location')}`);
const cookie = res.headers.get('set-cookie')?.split(';')[0];
check(Boolean(cookie), 'no session cookie');
const get = (path) => fetch(base + path, { headers: { cookie }, redirect: 'manual' });

res = await get('/api/environments');
check(res.status === 200 && Array.isArray(await res.json()), `/api/environments: ${res.status}`);

// 2. Every page and every internal link.
const index = await (await get('/llms.txt')).text();
const pages = [...new Set([...index.matchAll(/\]\((\/docs[^)\s]*)\)/g)].map((m) => m[1]))];
check(pages.length > 20, `only ${pages.length} pages listed`);

const html = new Map();
async function page(path) {
  if (!html.has(path)) {
    const r = await get(path);
    html.set(path, r.status === 200 ? await r.text() : null);
    check(r.status === 200, `${path}: ${r.status}`);
  }
  return html.get(path);
}

for (const p of pages) {
  const body = await page(p);
  if (!body) continue;
  for (const [, href] of body.matchAll(/href="(\/docs[^"]*)"/g)) {
    const [path, anchor] = href.replace(/&amp;/g, '&').split('#');
    const target = await page(path || p);
    if (target && anchor) check(target.includes(`id="${anchor}"`), `${p} → ${href}: missing anchor`);
  }
}

if (failures.length) {
  console.error(`✗ ${failures.length} problem(s):\n  ${failures.join('\n  ')}`);
  process.exit(1);
}
console.log(`✓ gate ok, ${html.size} pages and their links ok`);
