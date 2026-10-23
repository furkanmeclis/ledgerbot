import { SESSION_COOKIE, cookieAttributes, createSession, isSecureRequest, passwordMatches, safeNext, sessionTTLSeconds } from '@/lib/auth';

export const dynamic = 'force-dynamic';

// Per-process attempt limit: 10 failures per client per 10 minutes.
const WINDOW_MS = 10 * 60 * 1000;
const MAX_FAILURES = 10;
const failures = new Map<string, { count: number; reset: number }>();

function clientKey(req: Request): string {
  return req.headers.get('x-forwarded-for')?.split(',')[0]?.trim() || req.headers.get('x-real-ip') || 'local';
}

function redirect(location: string, cookie?: string): Response {
  const headers = new Headers({ Location: location, 'Cache-Control': 'no-store' });
  if (cookie) headers.append('Set-Cookie', cookie);
  return new Response(null, { status: 303, headers });
}

export async function POST(req: Request) {
  const form = await req.formData().catch(() => null);
  const next = safeNext(form?.get('next'));
  const back = (error: string) => `/login?error=${error}&next=${encodeURIComponent(next)}`;

  const key = clientKey(req);
  const now = Date.now();
  const entry = failures.get(key);
  if (entry && entry.reset > now && entry.count >= MAX_FAILURES) return redirect(back('rate'));

  const password = form?.get('password');
  if (typeof password !== 'string' || !(await passwordMatches(password))) {
    const e = entry && entry.reset > now ? entry : { count: 0, reset: now + WINDOW_MS };
    e.count++;
    failures.set(key, e);
    if (failures.size > 10_000) for (const [k, v] of failures) if (v.reset <= now) failures.delete(k);
    await new Promise((r) => setTimeout(r, 400));
    return redirect(back('invalid'));
  }

  failures.delete(key);
  const value = await createSession(now);
  return redirect(next, `${SESSION_COOKIE}=${value}; ${cookieAttributes(isSecureRequest(req), sessionTTLSeconds())}`);
}
