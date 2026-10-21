import { useState } from 'react';
import { Check, X } from 'lucide-react';
import { api } from '@/lib/api';
import { tl, timeAgo } from '@/lib/utils';
import type { ConsoleStore } from '@/store';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { StatusBadge } from './ChannelView';

export function Approvals({ store }: { store: ConsoleStore }) {
  const drafts = store.state?.drafts ?? [];
  const users = store.state?.settings.permissions.users ?? [];
  const [actor, setActor] = useState('');
  const [filter, setFilter] = useState('all');
  const [err, setErr] = useState('');
  const who = actor || users.find((u) => u.role === 'owner')?.id || '';
  const shown = drafts.filter((d) => filter === 'all' || d.status === filter);

  return (
    <Card>
      <CardHeader className="gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <CardTitle>Drafts and approvals</CardTitle>
          <CardDescription>Everything the assistant wanted to record. Approvals obey the permission matrix and the "own" scope.</CardDescription>
        </div>
        <div className="flex gap-2">
          <select className="h-9 rounded-md border bg-card px-2 text-sm" value={filter} onChange={(e) => setFilter(e.target.value)}>
            {['all', 'pending', 'committed', 'preview', 'rejected', 'failed'].map((s) => (
              <option key={s}>{s}</option>
            ))}
          </select>
          <select className="h-9 rounded-md border bg-card px-2 text-sm" value={who} onChange={(e) => setActor(e.target.value)}>
            {users.map((u) => (
              <option key={u.id} value={u.id}>
                Act as {u.name ?? u.id} ({u.role})
              </option>
            ))}
          </select>
        </div>
      </CardHeader>
      <CardContent>
        {err && <p className="mb-2 text-sm text-destructive">{err}</p>}
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-muted-foreground">
              <tr className="border-b">
                <th className="py-2 pr-3 font-medium">When</th>
                <th className="py-2 pr-3 font-medium">Capability</th>
                <th className="py-2 pr-3 font-medium">Summary</th>
                <th className="py-2 pr-3 font-medium">Amount</th>
                <th className="py-2 pr-3 font-medium">By</th>
                <th className="py-2 pr-3 font-medium">Status</th>
                <th className="py-2" />
              </tr>
            </thead>
            <tbody>
              {shown.map((d) => (
                <tr key={d.id} className="border-b align-top last:border-0">
                  <td className="py-2 pr-3 text-xs text-muted-foreground">{timeAgo(d.created_at)}</td>
                  <td className="py-2 pr-3 font-mono text-xs">{d.capability}</td>
                  <td className="max-w-md whitespace-pre-wrap py-2 pr-3">{d.summary}</td>
                  <td className="py-2 pr-3 whitespace-nowrap">{tl(d.payload?.amount)}</td>
                  <td className="py-2 pr-3 text-xs">
                    {d.actor_id}
                    <div className="text-muted-foreground">
                      {d.channel}:{d.sender}
                    </div>
                  </td>
                  <td className="py-2 pr-3">
                    <StatusBadge status={d.status} />
                  </td>
                  <td className="py-2 text-right">
                    {d.status === 'pending' && (
                      <div className="flex justify-end gap-1">
                        <Button
                          size="sm"
                          onClick={async () => {
                            try {
                              await api.decide(store.company, d.id, 'approve', who);
                              setErr('');
                            } catch (e) {
                              setErr((e as Error).message);
                            }
                            store.refresh();
                          }}
                        >
                          <Check /> Approve
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={async () => {
                            try {
                              await api.decide(store.company, d.id, 'reject', who);
                            } catch (e) {
                              setErr((e as Error).message);
                            }
                            store.refresh();
                          }}
                        >
                          <X />
                        </Button>
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {!shown.length && <p className="py-8 text-center text-sm text-muted-foreground">No drafts.</p>}
        </div>
      </CardContent>
    </Card>
  );
}
