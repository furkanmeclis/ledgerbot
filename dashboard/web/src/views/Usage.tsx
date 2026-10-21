import type { ConsoleStore } from '@/store';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';

export function UsageView({ store }: { store: ConsoleStore }) {
  const u = store.state?.usage;
  if (!u) return null;
  const pct = u.quota.daily_limit ? Math.min(100, Math.round((u.quota.used_today / u.quota.daily_limit) * 100)) : 0;
  return (
    <div className="grid gap-4 xl:grid-cols-3">
      <Card>
        <CardHeader>
          <CardTitle>AI quota today</CardTitle>
          <CardDescription>Rule-based commands and e-invoice XML never use AI.</CardDescription>
        </CardHeader>
        <CardContent>
          <p className="text-3xl font-semibold">
            {u.quota.used_today}
            <span className="text-base font-normal text-muted-foreground"> / {u.quota.daily_limit || '∞'} calls</span>
          </p>
          <div className="mt-3 h-2 rounded-full bg-muted">
            <div className="h-2 rounded-full bg-primary" style={{ width: `${pct}%` }} />
          </div>
        </CardContent>
      </Card>
      <Card className="xl:col-span-2">
        <CardHeader>
          <CardTitle>Cost by purpose and provider</CardTitle>
          <CardDescription>
            {u.from} → {u.to} · {u.total_calls} calls · ${u.total_cost_usd.toFixed(4)}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-left text-xs text-muted-foreground">
                {['Purpose', 'Provider', 'Model', 'Calls', 'Tokens in', 'Tokens out', 'Audio s', 'Cost $'].map((h) => (
                  <th key={h} className="py-2 pr-3 font-medium">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {(u.rows ?? []).map((r: any, i: number) => (
                <tr key={i} className="border-b last:border-0">
                  <td className="py-2 pr-3">{r.purpose}</td>
                  <td className="py-2 pr-3">{r.provider}</td>
                  <td className="py-2 pr-3 font-mono text-xs">{r.model}</td>
                  <td className="py-2 pr-3">{r.calls}</td>
                  <td className="py-2 pr-3">{r.input_tokens}</td>
                  <td className="py-2 pr-3">{r.output_tokens}</td>
                  <td className="py-2 pr-3">{r.audio_seconds?.toFixed?.(1)}</td>
                  <td className="py-2 pr-3">{r.cost_usd.toFixed(4)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {!(u.rows ?? []).length && <p className="py-6 text-center text-sm text-muted-foreground">No AI calls yet. Send a receipt photo or a voice note.</p>}
        </CardContent>
      </Card>
    </div>
  );
}
