import { createOpenAPI } from 'fumadocs-openapi/server';
import path from 'node:path';

const contracts = path.join(process.cwd(), '..', 'docs', 'contracts');

// The contract files live in the repository (docs/contracts) and are the single
// source of truth; the API reference pages are generated from them at build time.
export const openapi = createOpenAPI({
  input: {
    ledgerbot: path.join(contracts, 'ledgerbot.openapi.yaml'),
    host: path.join(contracts, 'host.openapi.yaml'),
  },
});
