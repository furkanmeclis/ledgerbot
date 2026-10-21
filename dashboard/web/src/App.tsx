import { useState } from 'react';
import { Activity, BarChart3, ClipboardCheck, LayoutDashboard, Settings2, Wifi, WifiOff } from 'lucide-react';
import { cn } from '@/lib/utils';
import { useConsole } from '@/store';
import { CHANNELS, ChannelIcon, type ChannelKey } from '@/components/ChannelIcon';
import { Badge } from '@/components/ui/badge';
import { Overview } from '@/views/Overview';
import { ChannelView } from '@/views/ChannelView';
import { Approvals } from '@/views/Approvals';
import { ActivityView } from '@/views/Activity';
import { SettingsView } from '@/views/SettingsView';
import { UsageView } from '@/views/Usage';

const NAV = [
  { key: 'overview', label: 'Overview', icon: LayoutDashboard },
  ...CHANNELS.map((c) => ({ key: c.key, label: c.label, icon: null as any })),
  { key: 'approvals', label: 'Approvals', icon: ClipboardCheck },
  { key: 'activity', label: 'Activity', icon: Activity },
  { key: 'usage', label: 'AI usage', icon: BarChart3 },
  { key: 'settings', label: 'Settings', icon: Settings2 },
];

export default function App() {
  const store = useConsole();
  const [view, setView] = useState('overview');
  const pending = (store.state?.drafts ?? []).filter((d) => d.status === 'pending').length;
  const title = NAV.find((n) => n.key === view)?.label;
  const companyName = store.state?.settings.settings.company_name || store.company;

  return (
    <div className="flex min-h-screen">
      <div className="demo-watermark" aria-hidden />
      <aside className="sticky top-0 hidden h-screen w-60 shrink-0 flex-col bg-sidebar text-sidebar-foreground lg:flex">
        <div className="flex items-center gap-2 px-5 py-5">
          <span className="flex h-8 w-8 items-center justify-center rounded-md bg-primary text-sm font-bold text-primary-foreground">lb</span>
          <div>
            <p className="text-sm font-semibold leading-tight">ledgerbot</p>
            <p className="text-[11px] text-sidebar-muted">Assistant layer · Demo console</p>
          </div>
        </div>
        <nav className="flex-1 space-y-0.5 px-3">
          {NAV.map((n, i) => (
            <div key={n.key}>
              {i === 1 && <p className="px-2 pb-1 pt-4 text-[10px] font-semibold uppercase tracking-wider text-sidebar-muted">Channels</p>}
              {i === CHANNELS.length + 1 && <p className="px-2 pb-1 pt-4 text-[10px] font-semibold uppercase tracking-wider text-sidebar-muted">Operations</p>}
              <button
                onClick={() => setView(n.key)}
                className={cn(
                  'flex w-full items-center gap-2.5 rounded-md px-2 py-2 text-sm transition-colors hover:bg-sidebar-active',
                  view === n.key && 'bg-sidebar-active font-medium',
                )}
              >
                {n.icon ? <n.icon className="h-4 w-4 opacity-80" /> : <ChannelIcon channel={n.key} className="h-5 w-5 [&_svg]:h-3 [&_svg]:w-3" />}
                <span className="flex-1 text-left">{n.label}</span>
                {n.key === 'approvals' && pending > 0 && <span className="rounded-full bg-warning px-1.5 text-[11px] font-semibold text-black">{pending}</span>}
              </button>
            </div>
          ))}
        </nav>
        <div className="m-3 rounded-md border border-white/10 p-3 text-[11px] leading-relaxed text-sidebar-muted">
          Demo build. Runs on the fake data provider, the event simulator and offline AI stubs. Not connected to a live accounting system.
        </div>
      </aside>

      <main className="min-w-0 flex-1">
        <header className="sticky top-0 z-40 flex items-center justify-between gap-3 border-b bg-background/85 px-6 py-3 backdrop-blur">
          <div className="flex items-center gap-3">
            <h1 className="text-lg font-semibold">{title}</h1>
            <Badge variant="warning">DEMO</Badge>
          </div>
          <div className="flex items-center gap-3">
            <select
              className="h-9 rounded-md border bg-card px-2 text-sm lg:hidden"
              value={view}
              onChange={(e) => setView(e.target.value)}
            >
              {NAV.map((n) => (
                <option key={n.key} value={n.key}>
                  {n.label}
                </option>
              ))}
            </select>
            <select className="h-9 rounded-md border bg-card px-2 text-sm" value={store.company} onChange={(e) => store.setCompany(e.target.value)}>
              {(store.companies.length ? store.companies : [store.company]).map((c) => (
                <option key={c} value={c}>
                  {c === store.company ? companyName : c}
                </option>
              ))}
            </select>
            <span className={cn('flex items-center gap-1 text-xs', store.connected ? 'text-primary' : 'text-destructive')}>
              {store.connected ? <Wifi className="h-4 w-4" /> : <WifiOff className="h-4 w-4" />}
              {store.connected ? 'live' : 'offline'}
            </span>
          </div>
        </header>
        <div className="bg-warning/15 px-6 py-1.5 text-center text-xs text-[oklch(0.4_0.1_70)]">
          This is a demo environment with generated data. Channels are simulated; no message leaves this machine.
        </div>
        <div className="p-6">
          {store.error && <p className="mb-4 rounded-md border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">ledgerbot is not reachable: {store.error}</p>}
          {!store.state && !store.error && <p className="text-sm text-muted-foreground">Loading…</p>}
          {store.state && (
            <>
              {view === 'overview' && <Overview store={store} go={setView} />}
              {CHANNELS.some((c) => c.key === view) && <ChannelView key={view} store={store} channel={view as ChannelKey} />}
              {view === 'approvals' && <Approvals store={store} />}
              {view === 'activity' && <ActivityView store={store} />}
              {view === 'usage' && <UsageView store={store} />}
              {view === 'settings' && <SettingsView store={store} />}
            </>
          )}
        </div>
      </main>
    </div>
  );
}
