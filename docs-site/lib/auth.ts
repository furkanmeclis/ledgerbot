// Password gate for the documentation site.
//
// A successful login sets an HttpOnly cookie "v1.<expiry>.<hmac>" signed with
// DOCS_SESSION_SECRET (or, when unset, a key derived from DOCS_PASSWORD, so that
// changing the password signs everyone out). Only Web Crypto is used, so the same
// code runs in the proxy and in route handlers.

export const SESSION_COOKIE = 'ledgerbot_docs';

const enc = new TextEncoder();
const keys = new Map<string, Promise<CryptoKey>>();

function hmacKey(secret: string): Promise<CryptoKey> {
  let key = keys.get(secret);
  if (!key) {
    key = crypto.subtle.importKey('raw', enc.encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
    keys.set(secret, key);
  }
  return key;
}

async function sign(secret: string, data: string): Promise<Uint8Array> {
  return new Uint8Array(await crypto.subtle.sign('HMAC', await hmacKey(secret), enc.encode(data)));
}

function toBase64Url(bytes: Uint8Array): string {
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function equal(a: Uint8Array | string, b: Uint8Array | string): boolean {
  const x = typeof a === 'string' ? enc.encode(a) : a;
  const y = typeof b === 'string' ? enc.encode(b) : b;
  if (x.length !== y.length) return false;
  let diff = 0;
  for (let i = 0; i < x.length; i++) diff |= x[i] ^ y[i];
  return diff === 0;
}

export function authDisabled(): boolean {
  return process.env.DOCS_AUTH_DISABLED === 'true';
}

export function passwordConfigured(): boolean {
  return Boolean(process.env.DOCS_PASSWORD);
}

function sessionSecret(): string | null {
  const s = process.env.DOCS_SESSION_SECRET || process.env.DOCS_PASSWORD;
  return s ? `ledgerbot-docs/session/${s}` : null;
}

export function sessionTTLSeconds(): number {
  const hours = Number(process.env.DOCS_SESSION_TTL_HOURS ?? '12');
  return Math.round((Number.isFinite(hours) && hours > 0 ? hours : 12) * 3600);
}

/** Compares a submitted password with DOCS_PASSWORD in constant time. */
export async function passwordMatches(input: string): Promise<boolean> {
  const password = process.env.DOCS_PASSWORD;
  if (!password) return false;
  const k = 'ledgerbot-docs/password';
  return equal(await sign(k, input), await sign(k, password));
}

export async function createSession(now = Date.now()): Promise<string> {
  const secret = sessionSecret();
  if (!secret) throw new Error('DOCS_PASSWORD is not set');
  const payload = `v1.${Math.floor(now / 1000) + sessionTTLSeconds()}`;
  return `${payload}.${toBase64Url(await sign(secret, payload))}`;
}

export async function verifySession(value: string | undefined, now = Date.now()): Promise<boolean> {
  const secret = sessionSecret();
  if (!secret || !value) return false;
  const i = value.lastIndexOf('.');
  if (i < 0) return false;
  const payload = value.slice(0, i);
  const [version, exp] = payload.split('.');
  if (version !== 'v1' || !(Number(exp) > now / 1000)) return false;
  return equal(value.slice(i + 1), toBase64Url(await sign(secret, payload)));
}

/** Only same-site relative paths are allowed as the post-login destination. */
export function safeNext(next: unknown): string {
  if (typeof next !== 'string' || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return '/docs';
  return next;
}

export function cookieAttributes(secure: boolean, maxAge: number): string {
  return `Path=/; HttpOnly; SameSite=Lax; Max-Age=${maxAge}${secure ? '; Secure' : ''}`;
}

export function isSecureRequest(req: Request): boolean {
  if (process.env.DOCS_COOKIE_SECURE === 'true') return true;
  if (process.env.DOCS_COOKIE_SECURE === 'false') return false;
  return req.headers.get('x-forwarded-proto') === 'https' || new URL(req.url).protocol === 'https:';
}
