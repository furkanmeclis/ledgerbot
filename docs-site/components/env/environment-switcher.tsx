'use client';

import { cn } from '@/lib/cn';
import { resolveUrl, useEnvironments } from './environment-context';

export function EnvironmentSwitcher() {
  const { environments, selected, select } = useEnvironments();
  const { url, configured } = resolveUrl(selected, 'api');
  return (
    <div className="flex flex-col gap-1.5 rounded-lg border bg-fd-card p-2 text-xs">
      <div className="flex items-center justify-between">
        <span className="font-medium text-fd-muted-foreground">Environment</span>
        <span className={cn('size-2 rounded-full', selected.id === 'production' ? 'bg-red-500' : 'bg-amber-500')} />
      </div>
      <div role="radiogroup" aria-label="Environment" className="grid grid-cols-2 gap-1 rounded-md bg-fd-muted p-0.5">
        {environments.map((env) => (
          <button
            key={env.id}
            type="button"
            role="radio"
            aria-checked={env.id === selected.id}
            onClick={() => select(env.id)}
            className={cn(
              'rounded px-2 py-1 font-medium transition-colors',
              env.id === selected.id ? 'bg-fd-background text-fd-foreground shadow-sm' : 'text-fd-muted-foreground hover:text-fd-foreground',
            )}
          >
            {env.label}
          </button>
        ))}
      </div>
      <code className={cn('truncate font-mono text-[11px]', !configured && 'text-fd-muted-foreground italic')} title={url}>
        {url}
      </code>
    </div>
  );
}
