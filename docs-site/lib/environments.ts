// Ledgerbot environments shown in the docs. Read at request time from the
// container environment, so one image serves any deployment.

export interface Environment {
  id: 'production' | 'staging';
  label: string;
  /** Base URL of the ledgerbot REST API (/v1, /webhooks). */
  apiUrl: string;
  /** Base URL of the host (Easytrade) contract endpoints ledgerbot calls. */
  hostUrl: string;
}

function trim(v: string | undefined): string {
  return (v ?? '').trim().replace(/\/+$/, '');
}

export function getEnvironments(): Environment[] {
  return [
    {
      id: 'production',
      label: process.env.DOCS_PROD_LABEL || 'Production',
      apiUrl: trim(process.env.DOCS_PROD_URL),
      hostUrl: trim(process.env.DOCS_PROD_HOST_URL),
    },
    {
      id: 'staging',
      label: process.env.DOCS_STAGE_LABEL || 'Staging',
      apiUrl: trim(process.env.DOCS_STAGE_URL),
      hostUrl: trim(process.env.DOCS_STAGE_HOST_URL),
    },
  ];
}
