import { timeAgo } from '@/lib/utils';
import type { ConsoleStore } from '@/store';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';

const tone: Record<string, 'success' | 'warning' | 'destructive' | 'secondary'> = {
  'draft.committed': 'success',
  'draft.created': 'warning',
  'draft.duplicate': 'destructive',
  'permission.denied': 'destructive',
  'draft.rejected': 'destructive',
  'sender.unknown': 'destructive',
};

export function ActivityView({ store }: { store: ConsoleStore }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Audit log (live)</CardTitle>
        <CardDescription>Append-only. Every message, parsed intent, permission decision and record is logged; deletion requests redact personal data.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-1">
        {store.audit.map((a) => (
          <div key={a.id} className="grid grid-cols-[110px_180px_1fr] items-start gap-3 border-b py-2 text-sm last:border-0">
            <span className="text-xs text-muted-foreground">{timeAgo(a.at)}</span>
            <span>
              <Badge variant={tone[a.action] ?? 'secondary'}>{a.action}</Badge>
            </span>
            <span className="min-w-0 text-xs">
              <span className="text-muted-foreground">
                {a.actor_id || '—'} · {a.channel || '—'}
                {a.draft_id ? ` · ${a.draft_id.slice(0, 12)}…` : ''}
              </span>
              {a.detail && <code className="mt-0.5 block truncate text-[11px]">{JSON.stringify(a.detail)}</code>}
            </span>
          </div>
        ))}
        {!store.audit.length && <p className="text-sm text-muted-foreground">No activity yet.</p>}
      </CardContent>
    </Card>
  );
}
