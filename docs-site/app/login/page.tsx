import type { Metadata } from 'next';
import { BookOpenText, LockKeyhole } from 'lucide-react';
import { passwordConfigured, safeNext } from '@/lib/auth';

export const metadata: Metadata = { title: 'Sign in' };
export const dynamic = 'force-dynamic';

const messages: Record<string, string> = {
  invalid: 'Wrong password. Try again.',
  rate: 'Too many attempts. Wait a few minutes and try again.',
};

export default async function LoginPage(props: PageProps<'/login'>) {
  const params = await props.searchParams;
  const next = safeNext(typeof params.next === 'string' ? params.next : undefined);
  const error = typeof params.error === 'string' ? messages[params.error] : undefined;
  const configured = passwordConfigured();

  return (
    <main className="flex flex-1 items-center justify-center bg-fd-background px-4 py-16">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex items-center justify-center gap-2 text-lg font-semibold">
          <BookOpenText className="size-6" /> Ledgerbot <span className="font-normal text-fd-muted-foreground">Docs</span>
        </div>
        <form action="/api/auth/login" method="post" className="flex flex-col gap-4 rounded-xl border bg-fd-card p-6 shadow-sm">
          <div className="flex items-center gap-2 text-sm text-fd-muted-foreground">
            <LockKeyhole className="size-4" /> This documentation is private.
          </div>
          <input type="hidden" name="next" value={next} />
          <label className="flex flex-col gap-1.5 text-sm font-medium">
            Password
            <input
              type="password"
              name="password"
              required
              autoFocus
              autoComplete="current-password"
              disabled={!configured}
              className="rounded-md border bg-fd-background px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-fd-primary/40"
            />
          </label>
          {error && <p className="text-sm text-red-600 dark:text-red-400">{error}</p>}
          {!configured && (
            <p className="text-sm text-amber-700 dark:text-amber-400">
              Sign-in is disabled: <code>DOCS_PASSWORD</code> is not set on this deployment.
            </p>
          )}
          <button
            type="submit"
            disabled={!configured}
            className="rounded-md bg-fd-primary px-3 py-2 text-sm font-medium text-fd-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50"
          >
            Sign in
          </button>
        </form>
      </div>
    </main>
  );
}
