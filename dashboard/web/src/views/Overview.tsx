import { Activity, Bot, CircleDollarSign, FileCheck2, Gauge, Hourglass, Play, ShieldCheck } from 'lucide-react';
import { useState } from 'react';
import { api } from '@/lib/api';
import { tl, timeAgo } from '@/lib/utils';
import type { ConsoleStore } from '@/store';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { CHANNELS, ChannelIcon } from '@/components/ChannelIcon';
import { StatusBadge } from './ChannelView';

export function Overview({ store, go }: { store: ConsoleStore; go: (v: string) => void }) {
  const s = store.state;
  const drafts = s?.drafts ?? [];
  const today = new Date().toDateString();
  const todays = drafts.filter((d) => new Date(d.created_at).toDateString() === today);
  const committed = todays.filter((d) => d.status === 'committed');
  const pending = drafts.filter((d) => d.status === 'pending');
  const messages = store.audit.filter((a) => a.action === 'message.received').length;
  const recorded = committed.reduce((sum, d) => sum + (d.payload?.amount ?? 0), 0);
  const [sim, setSim] = useState('');

  const kpis = [
    { label: 'Messages (recent)', value: String(messages), icon: Activity },
    { label: 'Recorded today', value: `${committed.length} · ${tl(recorded)}`, icon: FileCheck2 },
    { label: 'Waiting for approval', value: String(pending.length), icon: Hourglass },
    { label: 'AI cost this month', value: `$${(s?.usage.total_cost_usd ?? 0).toFixed(4)}`, icon: CircleDollarSign },
    { label: 'AI quota today', value: `${s?.usage.quota.used_today ?? 0} / ${s?.usage.quota.daily_limit || '∞'}`, icon: Gauge },
    { label: 'Save mode', value: s?.capabilities.save_mode ?? '—', icon: ShieldCheck },
  ];

  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-6">
        {kpis.map((k) => (
          <Card key={k.label}>
            <CardContent className="flex items-center gap-3 p-4">
              <span className="flex h-9 w-9 items-center justify-center rounded-md bg-accent text-accent-foreground">
                <k.icon className="h-4 w-4" />
              </span>
              <div className="min-w-0">
                <p className="truncate text-xs text-muted-foreground">{k.label}</p>
                <p className="truncate text-base font-semibold">{k.value}</p>
              </div>
            </CardContent>
          </Card>
        ))}
      </div>

      <div className="grid gap-4 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader>
            <CardTitle>Channels</CardTitle>
            <CardDescription>Every channel adapter feeds the same assistant core. In the demo they run on the dev channel with fake data.</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-3 sm:grid-cols-2">
            {CHANNELS.map((c) => (
              <button key={c.key} onClick={() => go(c.key)} className="flex items-start gap-3 rounded-lg border p-3 text-left transition-colors hover:bg-accent/60">
                <ChannelIcon channel={c.key} className="h-9 w-9" />
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{c.label}</span>
                    <Badge variant="warning">simulated</Badge>
                  </div>
                  <p className="mt-0.5 text-xs text-muted-foreground">{c.hint}</p>
                </div>
              </button>
            ))}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Bot className="h-4 w-4" /> Event simulator
            </CardTitle>
            <CardDescription>Creates fake sales; low stock and overdue invoices trigger proactive alerts to the roles in the rules.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-2">
            <Button
              className="w-full"
              onClick={async () => {
                const r = await api.simulate(store.company);
                setSim(`${r.events} events queued`);
                store.refresh();
              }}
            >
              <Play /> Run a simulator round
            </Button>
            <Button
              className="w-full"
              variant="outline"
              onClick={async () => {
                await api.event(store.company, { type: 'stock.low', data: { product_id: 'P002', name: 'Toner 85A', unit: 'adet', stock: 3000, min_stock: 15000 } });
                setSim('stock.low pushed as a host event');
              }}
            >
              Push host event: stock.low
            </Button>
            {sim && <p className="text-xs text-muted-foreground">{sim}. Alerts arrive in the owner's chat.</p>}
          </CardContent>
        </Card>
      </div>

      <div className="grid gap-4 xl:grid-cols-2">
        <Card>
          <CardHeader className="flex-row items-center justify-between">
            <CardTitle>Latest drafts</CardTitle>
            <Button size="sm" variant="ghost" onClick={() => go('approvals')}>
              View all
            </Button>
          </CardHeader>
          <CardContent className="space-y-2">
            {drafts.slice(0, 6).map((d) => (
              <div key={d.id} className="flex items-start justify-between gap-2 rounded-md border p-2 text-sm">
                <div className="min-w-0">
                  <p className="truncate font-medium">{d.summary.split('\n')[0]}</p>
                  <p className="text-xs text-muted-foreground">
                    {d.capability} · {d.sender} · {timeAgo(d.created_at)}
                  </p>
                </div>
                <StatusBadge status={d.status} />
              </div>
            ))}
            {!drafts.length && <p className="text-sm text-muted-foreground">No drafts yet. Send a collection from a channel view.</p>}
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex-row items-center justify-between">
            <CardTitle>Live activity</CardTitle>
            <Button size="sm" variant="ghost" onClick={() => go('activity')}>
              View all
            </Button>
          </CardHeader>
          <CardContent className="space-y-1">
            {store.audit.slice(0, 10).map((a) => (
              <div key={a.id} className="flex items-center justify-between text-sm">
                <span className="font-mono text-xs">{a.action}</span>
                <span className="text-xs text-muted-foreground">
                  {a.actor_id} · {timeAgo(a.at)}
                </span>
              </div>
            ))}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
