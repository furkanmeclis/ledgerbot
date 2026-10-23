import { NextRequest, NextResponse } from 'next/server';
import { isMarkdownPreferred, rewritePath } from 'fumadocs-core/negotiation';
import { docsContentRoute, docsRoute } from '@/lib/shared';
import { SESSION_COOKIE, authDisabled, verifySession } from '@/lib/auth';

const { rewrite: rewriteDocs } = rewritePath(`${docsRoute}{/*path}`, `${docsContentRoute}{/*path}/content.md`);
const { rewrite: rewriteSuffix } = rewritePath(`${docsRoute}{/*path}.md`, `${docsContentRoute}{/*path}/content.md`);

const publicPaths = new Set(['/login', '/api/auth/login', '/api/health']);

export default async function proxy(request: NextRequest) {
  const { pathname, search } = request.nextUrl;

  if (!publicPaths.has(pathname) && !authDisabled() && !(await verifySession(request.cookies.get(SESSION_COOKIE)?.value))) {
    if (pathname.startsWith('/api/') || pathname.startsWith('/llms')) {
      return NextResponse.json({ error: 'unauthorized' }, { status: 401 });
    }
    const login = request.nextUrl.clone();
    login.pathname = '/login';
    login.search = `?next=${encodeURIComponent(pathname + search)}`;
    const res = NextResponse.redirect(login, 307);
    res.headers.set('Cache-Control', 'no-store');
    return res;
  }

  const result = rewriteSuffix(pathname);
  if (result) {
    return NextResponse.rewrite(new URL(result, request.nextUrl));
  }

  if (isMarkdownPreferred(request)) {
    const result = rewriteDocs(pathname);

    if (result) {
      return NextResponse.rewrite(new URL(result, request.nextUrl), {
        // this URL has two representations, selected by `Accept`
        headers: { Vary: 'Accept' },
      });
    }
  }

  return NextResponse.next();
}

export const config = {
  matcher: ['/((?!_next/static|_next/image|favicon.ico|icon.svg).*)'],
};
