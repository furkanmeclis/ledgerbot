import { source } from '@/lib/source';
import { DocsLayout } from 'fumadocs-ui/layouts/docs';
import { LogOut } from 'lucide-react';
import { baseOptions } from '@/lib/layout.shared';
import { EnvironmentProvider } from '@/components/env/environment-context';
import { EnvironmentSwitcher } from '@/components/env/environment-switcher';

export default function Layout({ children }: LayoutProps<'/docs'>) {
  return (
    <EnvironmentProvider>
      <DocsLayout
        tree={source.getPageTree()}
        {...baseOptions()}
        sidebar={{
          banner: <EnvironmentSwitcher />,
          footer: (
            <form action="/api/auth/logout" method="post">
              <button
                type="submit"
                className="inline-flex items-center gap-1.5 text-sm text-fd-muted-foreground hover:text-fd-foreground"
              >
                <LogOut className="size-4" /> Sign out
              </button>
            </form>
          ),
        }}
      >
        {children}
      </DocsLayout>
    </EnvironmentProvider>
  );
}
