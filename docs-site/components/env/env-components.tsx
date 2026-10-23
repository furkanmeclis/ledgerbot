'use client';

import { DynamicCodeBlock } from 'fumadocs-ui/components/dynamic-codeblock';
import { Callout } from 'fumadocs-ui/components/callout';
import { resolveUrl, useEnvironments } from './environment-context';

/** Inline base URL of the selected environment, optionally with a path. */
export function BaseUrl({ kind = 'api', path = '' }: { kind?: 'api' | 'host'; path?: string }) {
  const { selected } = useEnvironments();
  const { url } = resolveUrl(selected, kind);
  return <code>{url + path}</code>;
}

/** Shell exports for the selected environment, used by every curl example. */
export function EnvExports({ apiKey = true }: { apiKey?: boolean }) {
  const { selected } = useEnvironments();
  const { url } = resolveUrl(selected, 'api');
  const lines = [`# ${selected.label}`, `export LEDGERBOT_URL=${url}`];
  if (apiKey) lines.push('export LEDGERBOT_KEY=lb_<company>_<secret>');
  return <DynamicCodeBlock lang="bash" code={lines.join('\n')} />;
}

/** Table of every configured environment. */
export function EnvironmentTable() {
  const { environments, selected, loaded } = useEnvironments();
  const anyMissing = environments.some((e) => !e.apiUrl || !e.hostUrl);
  return (
    <>
      <div className="not-prose overflow-x-auto rounded-lg border">
        <table className="w-full text-sm">
          <thead className="bg-fd-muted text-left text-fd-muted-foreground">
            <tr>
              <th className="px-3 py-2 font-medium">Environment</th>
              <th className="px-3 py-2 font-medium">Ledgerbot API</th>
              <th className="px-3 py-2 font-medium">Host contract (Easytrade)</th>
            </tr>
          </thead>
          <tbody>
            {environments.map((env) => {
              const api = resolveUrl(env, 'api');
              const host = resolveUrl(env, 'host');
              return (
                <tr key={env.id} className="border-t">
                  <td className="px-3 py-2 font-medium">
                    {env.label}
                    {env.id === selected.id && <span className="ml-2 rounded bg-fd-primary/10 px-1.5 py-0.5 text-xs text-fd-primary">selected</span>}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs">
                    <span className={api.configured ? '' : 'italic text-fd-muted-foreground'}>{api.url}</span>
                  </td>
                  <td className="px-3 py-2 font-mono text-xs">
                    <span className={host.configured ? '' : 'italic text-fd-muted-foreground'}>{host.url}</span>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {loaded && anyMissing && (
        <Callout type="warn" title="Some URLs are placeholders">
          Italic values are examples. Set <code>DOCS_PROD_URL</code>, <code>DOCS_STAGE_URL</code>, <code>DOCS_PROD_HOST_URL</code> and{' '}
          <code>DOCS_STAGE_HOST_URL</code> on the docs container to show the real ones.
        </Callout>
      )}
    </>
  );
}
