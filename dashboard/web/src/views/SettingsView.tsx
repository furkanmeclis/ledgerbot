import { Check, Minus } from 'lucide-react';
import type { ConsoleStore } from '@/store';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[200px_1fr] gap-3 border-b py-2 text-sm last:border-0">
      <span className="font-mono text-xs text-muted-foreground">{k}</span>
      <span className="min-w-0 break-words">{v === '' || v === undefined || v === null ? <span className="text-muted-foreground">—</span> : v}</span>
    </div>
  );
}

function Json({ v }: { v: unknown }) {
  return <pre className="max-h-[420px] overflow-auto rounded-md bg-slate-950 p-3 text-[12px] leading-relaxed text-slate-200">{JSON.stringify(v, null, 2)}</pre>;
}

export function SettingsView({ store }: { store: ConsoleStore }) {
  const st = store.state;
  if (!st) return null;
  const s = st.settings.settings;
  const m = st.settings.permissions;
  const caps = st.capabilities.capabilities;
  const roles = Object.keys(m.roles);

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>Settings contract</CardTitle>
          <CardDescription>
            Served by the host per company (GET /companies/{'{id}'}/settings and /permissions). ledgerbot caches it, and if the host is unreachable it serves the last
            snapshot in degraded mode where nothing is written. Fetched {new Date(st.settings.fetched_at).toLocaleTimeString()}.
            {st.settings.degraded && <Badge variant="destructive" className="ml-2">degraded</Badge>}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Tabs defaultValue="general">
            <TabsList className="flex-wrap">
              <TabsTrigger value="general">General</TabsTrigger>
              <TabsTrigger value="matrix">Permission matrix</TabsTrigger>
              <TabsTrigger value="users">Users & addresses</TabsTrigger>
              <TabsTrigger value="automation">Rules & reports</TabsTrigger>
              <TabsTrigger value="integrations">Keys, webhooks, plugins</TabsTrigger>
              <TabsTrigger value="prompts">Prompts</TabsTrigger>
              <TabsTrigger value="raw">Raw JSON</TabsTrigger>
            </TabsList>

            <TabsContent value="general">
              <Row k="company_id" v={s.company_id} />
              <Row k="company_name" v={s.company_name} />
              <Row k="version" v={s.version} />
              <Row k="save_mode" v={<Badge variant="success">{s.save_mode}</Badge>} />
              <Row k="locale" v={s.locale} />
              <Row k="timezone" v={s.timezone} />
              <Row k="ai_daily_limit" v={s.ai_daily_limit || 'unlimited'} />
              <Row k="providers.agent" v={s.providers?.agent || 'deployment default'} />
              <Row k="providers.extraction" v={s.providers?.extraction || 'deployment default'} />
              <Row k="providers.stt" v={s.providers?.stt || 'deployment default'} />
              <Row k="signing_secret" v={s.signing_secret || 'not required'} />
            </TabsContent>

            <TabsContent value="matrix">
              <div className="mb-3 flex flex-wrap gap-2">
                {roles.map((r) => (
                  <div key={r} className="rounded-md border px-3 py-2 text-xs">
                    <span className="font-semibold">{r}</span> · save mode {m.roles[r].save_mode || 'company default'} · scope {m.roles[r].scope || 'all'} · channels{' '}
                    {m.roles[r].channels?.join(', ') || 'all'}
                  </div>
                ))}
              </div>
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b text-left text-xs text-muted-foreground">
                      <th className="py-2 pr-3 font-medium">Capability</th>
                      <th className="py-2 pr-3 font-medium">Type</th>
                      {roles.map((r) => (
                        <th key={r} className="py-2 pr-3 text-center font-medium">
                          {r}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {caps.map((c) => (
                      <tr key={c.name} className="border-b last:border-0">
                        <td className="py-2 pr-3">
                          <div className="font-mono text-xs font-medium">{c.name}</div>
                          <div className="text-xs text-muted-foreground">{c.description}</div>
                        </td>
                        <td className="py-2 pr-3">
                          <Badge variant={c.write ? 'warning' : 'secondary'}>{c.write ? 'write' : 'read'}</Badge>
                        </td>
                        {roles.map((r) => (
                          <td key={r} className="py-2 pr-3 text-center">
                            {c.roles.includes(r) ? <Check className="mx-auto h-4 w-4 text-primary" /> : <Minus className="mx-auto h-4 w-4 text-muted-foreground/50" />}
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </TabsContent>

            <TabsContent value="users">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-xs text-muted-foreground">
                    <th className="py-2 pr-3 font-medium">User</th>
                    <th className="py-2 pr-3 font-medium">Role</th>
                    <th className="py-2 pr-3 font-medium">Addresses</th>
                  </tr>
                </thead>
                <tbody>
                  {m.users.map((u) => (
                    <tr key={u.id} className="border-b last:border-0">
                      <td className="py-2 pr-3">
                        {u.name}
                        <div className="font-mono text-xs text-muted-foreground">{u.id}</div>
                      </td>
                      <td className="py-2 pr-3">
                        <Badge variant="secondary">{u.role}</Badge>
                      </td>
                      <td className="py-2 pr-3">
                        <div className="flex flex-wrap gap-1">
                          {Object.entries(u.addresses).map(([k, v]) => (
                            <Badge key={k} variant="outline">
                              {k}: {v}
                            </Badge>
                          ))}
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TabsContent>

            <TabsContent value="automation" className="grid gap-4 md:grid-cols-2">
              <div>
                <p className="mb-2 text-sm font-semibold">Alert rules</p>
                {(s.rules ?? []).map((r: any, i: number) => (
                  <Row key={i} k={r.event} v={`→ ${r.notify_roles.join(', ')}${r.throttle ? ` · at most every ${r.throttle}` : ''}`} />
                ))}
              </div>
              <div>
                <p className="mb-2 text-sm font-semibold">Scheduled reports</p>
                {(s.reports ?? []).map((r: any, i: number) => (
                  <Row
                    key={i}
                    k={r.type}
                    v={`${r.at}${r.weekday ? ` · weekday ${r.weekday}` : ''}${r.day ? ` · day ${r.day}` : ''} → ${r.roles.join(', ')}`}
                  />
                ))}
                {!(s.reports ?? []).length && <p className="text-sm text-muted-foreground">None.</p>}
              </div>
            </TabsContent>

            <TabsContent value="integrations" className="space-y-4">
              <div>
                <p className="mb-2 text-sm font-semibold">API keys (SHA-256 only)</p>
                {(s.api_keys ?? []).map((k: any) => (
                  <Row key={k.id} k={k.id} v={`${k.sha256} · scopes ${k.scopes?.join(', ')}`} />
                ))}
              </div>
              <div>
                <p className="mb-2 text-sm font-semibold">Webhooks</p>
                {(s.webhooks ?? []).length ? (
                  s.webhooks.map((w: any, i: number) => <Row key={i} k={w.url} v={`${w.events.join(', ')} · secret ${w.secret}`} />)
                ) : (
                  <p className="text-sm text-muted-foreground">None. Outbound events are skipped entirely when nobody subscribes.</p>
                )}
              </div>
              <div>
                <p className="mb-2 text-sm font-semibold">Plugin capabilities</p>
                {(s.plugins ?? []).length ? <Json v={s.plugins} /> : <p className="text-sm text-muted-foreground">None configured.</p>}
              </div>
              <div>
                <p className="mb-2 text-sm font-semibold">Channel connections</p>
                {st.channels.channels?.length ? (
                  <Json v={st.channels.channels} />
                ) : (
                  <p className="text-sm text-muted-foreground">
                    None in the demo. Webhook base: <code>{st.channels.webhook_base}</code>
                  </p>
                )}
              </div>
            </TabsContent>

            <TabsContent value="prompts">
              <Row k="prompts.system" v={s.prompts?.system} />
              {Object.entries(s.prompts?.roles ?? {}).map(([r, p]) => (
                <Row key={r} k={`prompts.roles.${r}`} v={String(p)} />
              ))}
              <p className="mt-3 text-xs text-muted-foreground">Host prompts add context only; ledgerbot's output contract and the "code computes numbers" rule cannot be overridden.</p>
            </TabsContent>

            <TabsContent value="raw">
              <Json v={st.settings} />
            </TabsContent>
          </Tabs>
        </CardContent>
      </Card>
    </div>
  );
}
