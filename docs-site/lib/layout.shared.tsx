import type { BaseLayoutProps } from 'fumadocs-ui/layouts/shared';
import { BookOpenText } from 'lucide-react';
import { appName, gitConfig } from './shared';

export function baseOptions(): BaseLayoutProps {
  return {
    nav: {
      title: (
        <span className="inline-flex items-center gap-2 font-semibold">
          <BookOpenText className="size-5" /> {appName} <span className="font-normal text-fd-muted-foreground">Docs</span>
        </span>
      ),
      url: '/docs',
    },
    githubUrl: `https://github.com/${gitConfig.user}/${gitConfig.repo}`,
  };
}
