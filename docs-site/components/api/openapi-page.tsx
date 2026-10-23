'use client';

import { createOpenAPIPage } from 'fumadocs-openapi/ui';
import { useMemo, type ComponentProps } from 'react';
import { resolveUrl, useEnvironments } from '@/components/env/environment-context';

// Reference only: requests are never sent from the docs (no playground).
const BasePage = createOpenAPIPage({ playground: { enabled: false } });

type Props = ComponentProps<typeof BasePage>;

/** Renders an API reference page with the server URL of the selected environment. */
export function OpenAPIPage(props: Props) {
  const { selected } = useEnvironments();
  const preloaded = 'preloaded' in props ? props.preloaded : undefined;

  const docs = useMemo(() => {
    if (!preloaded) return undefined;
    const out: typeof preloaded.docs = {};
    for (const [id, doc] of Object.entries(preloaded.docs)) {
      const { url } = resolveUrl(selected, id === 'host' ? 'host' : 'api');
      out[id] = { ...doc, servers: [{ url, description: selected.label }] };
    }
    return out;
  }, [preloaded, selected]);

  if (!preloaded || !docs) return <BasePage {...props} />;
  return <BasePage {...(props as Extract<Props, { preloaded: unknown }>)} preloaded={{ ...preloaded, docs }} />;
}
