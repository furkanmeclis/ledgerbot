import { io, type Socket } from 'socket.io-client';

export type User = { id: string; name?: string; role: string; addresses: Record<string, string> };
export type Role = { capabilities: string[]; channels?: string[]; save_mode?: string; scope?: string };
export type Draft = {
  id: string;
  actor_id: string;
  capability: string;
  status: 'preview' | 'pending' | 'committed' | 'rejected' | 'failed';
  payload: Record<string, any>;
  summary: string;
  channel: string;
  sender: string;
  created_at: string;
  external_id?: string;
};
export type AuditEntry = { id: number; actor_id?: string; channel?: string; action: string; draft_id?: string; detail?: any; at: string };
export type Capability = { name: string; description: string; args?: string[]; write: boolean; roles: string[] };
export type Outbound = { id?: number; at?: string; to: string; text: string };
export type Reply = { texts: string[]; language: string; drafts?: Draft[]; intents?: { capability: string; source: string; args: Record<string, string> }[] };

export type State = {
  settings: { settings: Record<string, any>; permissions: { roles: Record<string, Role>; users: User[] }; degraded: boolean; fetched_at: string };
  capabilities: { capabilities: Capability[]; save_mode: string; degraded: boolean };
  usage: { rows: any[]; total_calls: number; total_cost_usd: number; quota: { daily_limit: number; used_today: number }; from: string; to: string };
  drafts: Draft[];
  channels: { channels: any[]; webhook_base: string };
};

async function req<T>(method: string, url: string, body?: unknown): Promise<T> {
  const res = await fetch(url, { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
  const json = await res.json();
  if (!res.ok) throw new Error(json.error || res.statusText);
  return json as T;
}

export const api = {
  companies: () => req<string[]>('GET', '/api/companies'),
  state: (c: string) => req<State>('GET', `/api/${c}/state`),
  audit: (c: string) => req<{ entries: AuditEntry[] }>('GET', `/api/${c}/audit`),
  outbox: (c: string, to: string) => req<{ messages: Outbound[] | null }>('GET', `/api/${c}/outbox?to=${encodeURIComponent(to)}`),
  send: (c: string, body: { sender: string; channel: string; text?: string; media?: { mime: string; filename?: string; data: string } }) =>
    req<Reply>('POST', `/api/${c}/send`, body),
  decide: (c: string, id: string, action: 'approve' | 'reject', actor_id: string) => req<any>('POST', `/api/${c}/drafts/${id}/${action}`, { actor_id }),
  simulate: (c: string) => req<{ events: number }>('POST', `/api/${c}/simulate`),
  event: (c: string, body: unknown) => req<any>('POST', `/api/${c}/events`, body),
};

let socket: Socket | null = null;
export function live(): Socket {
  if (!socket) socket = io({ transports: ['websocket'] });
  return socket;
}
