import { useCallback, useEffect, useRef, useState } from 'react';
import { api, live, type AuditEntry, type Outbound, type State } from '@/lib/api';

export type ChatMsg = { key: string; direction: 'in' | 'out'; text: string; at: string; channel?: string; media?: string };

/** One shared live state for the whole console. */
export function useConsole() {
  const [companies, setCompanies] = useState<string[]>([]);
  const [company, setCompany] = useState<string>('demo');
  const [state, setState] = useState<State | null>(null);
  const [error, setError] = useState<string>('');
  const [audit, setAudit] = useState<AuditEntry[]>([]);
  const [chats, setChats] = useState<Record<string, ChatMsg[]>>({}); // key: company|sender
  const seenOut = useRef<Record<string, number>>({}); // company|sender -> last outbox id shown
  const [connected, setConnected] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const [s, a] = await Promise.all([api.state(company), api.audit(company)]);
      setState(s);
      setAudit(a.entries ?? []);
      setError('');
    } catch (e) {
      setError(String((e as Error).message || e));
    }
  }, [company]);

  useEffect(() => {
    api.companies().then(setCompanies).catch(() => {});
  }, []);

  useEffect(() => {
    setState(null);
    refresh();
    const t = setInterval(refresh, 5000);
    return () => clearInterval(t);
  }, [refresh]);

  const appendOutbox = useCallback((c: string, to: string, messages: Outbound[]) => {
    const k = `${c}|${to}`;
    const last = seenOut.current[k] ?? 0;
    const fresh = messages.filter((m) => (m.id ?? 0) > last);
    if (!fresh.length) return;
    seenOut.current[k] = Math.max(...fresh.map((m) => m.id ?? 0));
    setChats((prev) => ({
      ...prev,
      [k]: [...(prev[k] ?? []), ...fresh.map((m) => ({ key: `o${m.id}`, direction: 'out' as const, text: m.text, at: m.at ?? new Date().toISOString() }))],
    }));
  }, []);

  useEffect(() => {
    const s = live();
    const onConnect = () => setConnected(true);
    const onDisconnect = () => setConnected(false);
    const onChat = (m: { company: string; sender: string; text: string; at: string; channel?: string }) => {
      const k = `${m.company}|${m.sender}`;
      setChats((prev) => ({ ...prev, [k]: [...(prev[k] ?? []), { key: `i${m.at}${Math.random()}`, direction: 'in', text: m.text, at: m.at, channel: m.channel }] }));
    };
    const onOutbox = (m: { company: string; to: string; messages: Outbound[] }) => appendOutbox(m.company, m.to, m.messages);
    const onAudit = (m: { company: string; entries: AuditEntry[] }) => {
      if (m.company !== company) return;
      setAudit((prev) => [...m.entries.slice().reverse(), ...prev].slice(0, 200));
      refresh();
    };
    s.on('connect', onConnect);
    s.on('disconnect', onDisconnect);
    s.on('chat', onChat);
    s.on('outbox', onOutbox);
    s.on('audit', onAudit);
    setConnected(s.connected);
    return () => {
      s.off('connect', onConnect);
      s.off('disconnect', onDisconnect);
      s.off('chat', onChat);
      s.off('outbox', onOutbox);
      s.off('audit', onAudit);
    };
  }, [appendOutbox, company, refresh]);

  /** Loads the existing outbox for a sender once, so history survives reloads. */
  const primeOutbox = useCallback(
    async (to: string) => {
      const k = `${company}|${to}`;
      if (seenOut.current[k] !== undefined) return;
      seenOut.current[k] = 0;
      const { messages } = await api.outbox(company, to);
      appendOutbox(company, to, messages ?? []);
    },
    [appendOutbox, company],
  );

  return { companies, company, setCompany, state, error, audit, chats, connected, refresh, primeOutbox };
}

export type ConsoleStore = ReturnType<typeof useConsole>;
