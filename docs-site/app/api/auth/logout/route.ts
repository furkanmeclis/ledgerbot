import { SESSION_COOKIE, cookieAttributes, isSecureRequest } from '@/lib/auth';

export const dynamic = 'force-dynamic';

export async function POST(req: Request) {
  return new Response(null, {
    status: 303,
    headers: {
      Location: '/login',
      'Cache-Control': 'no-store',
      'Set-Cookie': `${SESSION_COOKIE}=; ${cookieAttributes(isSecureRequest(req), 0)}`,
    },
  });
}
