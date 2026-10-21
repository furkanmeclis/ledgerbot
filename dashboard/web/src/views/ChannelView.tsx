import { useEffect, useMemo, useRef, useState } from 'react';
import { Check, FileSpreadsheet, FileText, Image as ImageIcon, Loader2, Mic, Paperclip, Receipt, SendHorizonal, X } from 'lucide-react';
import { api, type Draft, type Reply, type User } from '@/lib/api';
import { cn, fileToBase64, textToBase64, timeAgo } from '@/lib/utils';
import type { ConsoleStore } from '@/store';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { CHANNELS, ChannelIcon, type ChannelKey } from '@/components/ChannelIcon';

const QUICK = [
  "Ahmet Yılmaz'dan 5.000 TL tahsilat",
  'evet',
  "Ahmet Yılmaz'ın bakiyesi",
  'stok toner',
  'kritik stok',
  'vadesi geçen alacaklar',
  'haftalık rapor',
  'KDV tahmini',
  'nakit akışı',
  "Ahmet Yılmaz için 10 paket A4 kağıdı sipariş oluştur",
  'her zamanki gibi 750 TL',
  'not al: depo kirası her ayın 5inde ödenir',
  'notlarda depo kirası ara',
  "Ahmet Yılmaz'a ekstre at ve vadesi geçenleri göster",
  'received 300 usd from Ahmet Yılmaz',
  'yardım',
];

type Media = { mime: string; filename?: string; data: string };

function sampleReceipt(): Media {
  const no = 'A-' + Math.floor(100000 + Math.random() * 899999);
  const json = {
    vendor: 'Örnek Kırtasiye Ltd. Şti.', vendor_tax_id: '1234567890', document_no: no, date: new Date().toISOString().slice(0, 10), currency: 'TRY',
    lines: [
      { name: 'A4 Fotokopi Kağıdı', quantity: 2, unit: 'paket', unit_price: '185,00', vat_rate: 20 },
      { name: 'Toner 85A', quantity: 1, unit: 'adet', unit_price: '1.450,00', vat_rate: 20 },
    ],
    total: '2.184,00', confidence: 0.93,
  };
  return { mime: 'image/jpeg', filename: `fis-${no}.jpg`, data: textToBase64(JSON.stringify(json)) };
}

function sampleStatement(): Media {
  const d = new Date();
  const day = (n: number) => new Date(d.getTime() - n * 86400000).toLocaleDateString('tr-TR');
  const amt = (1000 + Math.floor(Math.random() * 9000)).toLocaleString('tr-TR') + ',00';
  const csv = `Hesap Hareketleri\nİşlem Tarihi;Açıklama;Tutar;Bakiye;Dekont No\n${day(2)};FAST GELEN AHMET YILMAZ ${Date.now() % 100000};${amt};;D${Date.now() % 1000}\n${day(1)};KIRA ODEMESI DEPO;-18.500,00;;D2\n${day(0)};POS KOMISYON;-42,90;;D3\n`;
  return { mime: 'text/csv', filename: 'ekstre.csv', data: textToBase64(csv) };
}

async function sampleInvoice(): Promise<Media> {
  const text = await (await fetch('/samples/e-fatura.xml')).text();
  return { mime: 'application/xml', filename: 'e-fatura.xml', data: textToBase64(text) };
}

export function ChannelView({ store, channel }: { store: ConsoleStore; channel: ChannelKey }) {
  const meta = CHANNELS.find((c) => c.key === channel)!;
  const users = store.state?.settings.permissions.users ?? [];
  const usable = users.filter((u) => u.addresses.dev);
  const [sender, setSender] = useState<User | null>(null);
  const [text, setText] = useState('');
  const [subject, setSubject] = useState('');
  const [busy, setBusy] = useState(false);
  const [last, setLast] = useState<Reply | null>(null);
  const [raw, setRaw] = useState<string>('');
  const [err, setErr] = useState('');
  const fileRef = useRef<HTMLInputElement>(null);
  const endRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!sender && usable.length) setSender(usable.find((u) => u.role === 'accountant') ?? usable[0]);
  }, [usable, sender]);
  useEffect(() => {
    setSender(null);
    setLast(null);
  }, [store.company]);
  useEffect(() => {
    if (sender?.addresses.dev) store.primeOutbox(sender.addresses.dev);
  }, [sender, store]);

  const devAddr = sender?.addresses.dev ?? '';
  const msgs = useMemo(() => store.chats[`${store.company}|${devAddr}`] ?? [], [store.chats, store.company, devAddr]);
  useEffect(() => {
    // Newer browsers return a Promise from scrollIntoView; never return it from an effect.
    endRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [msgs.length]);

  const conn = (store.state?.channels.channels ?? []).find((c: any) =>
    channel === 'whatsapp' ? c.kind === 'wuzapi' || c.kind === 'waba' : c.kind === channel,
  );

  async function send(body: { text?: string; media?: Media }) {
    if (!sender) return;
    setBusy(true);
    setErr('');
    try {
      const t = channel === 'email' && subject && body.text ? `${body.text}` : body.text;
      const reply = await api.send(store.company, { sender: devAddr, channel, text: t, media: body.media });
      setLast(reply);
      setRaw(JSON.stringify(reply, null, 2));
      setText('');
      store.refresh();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function decide(d: Draft, action: 'approve' | 'reject') {
    if (!sender) return;
    try {
      await api.decide(store.company, d.id, action, sender.id);
      setLast((r) => (r ? { ...r, drafts: r.drafts?.map((x) => (x.id === d.id ? { ...x, status: action === 'approve' ? 'committed' : 'rejected' } : x)) } : r));
      store.refresh();
    } catch (e) {
      setErr((e as Error).message);
    }
  }

  async function attach(f: File) {
    const data = await fileToBase64(f);
    await send({ text: subject || undefined, media: { mime: f.type || 'application/octet-stream', filename: f.name, data } });
  }

  const address = sender ? sender.addresses[meta.addressKind] ?? sender.addresses.dev : '';
  const bubbleIn = channel === 'whatsapp' ? 'bg-[#D9FDD3]' : channel === 'telegram' ? 'bg-[#EFFDDE]' : 'bg-primary text-primary-foreground';

  return (
    <div className="grid gap-4 xl:grid-cols-[240px_1fr_320px]">
      {/* people */}
      <Card className="h-fit">
        <CardHeader>
          <div className="flex items-center gap-2">
            <ChannelIcon channel={channel} />
            <div>
              <CardTitle>{meta.label}</CardTitle>
              <CardDescription>{meta.hint}</CardDescription>
            </div>
          </div>
          <div className="mt-2 flex flex-wrap gap-1">
            {conn ? <Badge variant="success">{conn.status}</Badge> : <Badge variant="warning">Simulated in demo</Badge>}
            <Badge variant="outline">dev channel</Badge>
          </div>
        </CardHeader>
        <CardContent className="space-y-1">
          <p className="mb-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Send as</p>
          {usable.map((u) => (
            <button
              key={u.id}
              onClick={() => setSender(u)}
              className={cn('flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-sm hover:bg-muted', sender?.id === u.id && 'bg-accent')}
            >
              <span className="flex h-8 w-8 items-center justify-center rounded-full bg-primary/15 text-xs font-semibold text-primary">
                {(u.name ?? u.id).slice(0, 2).toUpperCase()}
              </span>
              <span className="min-w-0">
                <span className="block truncate font-medium">{u.name ?? u.id}</span>
                <span className="block truncate text-xs text-muted-foreground">
                  {u.role} · {u.addresses[meta.addressKind] ?? `dev:${u.addresses.dev}`}
                </span>
              </span>
            </button>
          ))}
        </CardContent>
      </Card>

      {/* conversation */}
      <Card className="flex h-[calc(100vh-150px)] min-h-[560px] flex-col overflow-hidden">
        <div className="flex items-center justify-between border-b px-4 py-3">
          <div className="text-sm">
            <span className="font-semibold">{sender?.name ?? '—'}</span>
            <span className="ml-2 text-muted-foreground">{address}</span>
          </div>
          <Badge variant="secondary">{sender?.role}</Badge>
        </div>

        {channel === 'api' ? (
          <div className="grid flex-1 gap-0 md:grid-cols-2">
            <pre className="overflow-auto whitespace-pre-wrap break-all border-r bg-slate-950 p-4 text-[12px] leading-relaxed text-emerald-200">
{`curl -X POST $LEDGERBOT/v1/messages \\
  -H "Authorization: Bearer lb_${store.company}_••••" \\
  -H "Idempotency-Key: $(uuidgen)" \\
  -d '{"actor_id":"${sender?.id ?? ''}","text":${JSON.stringify(text || '…')}}'`}
            </pre>
            <pre className="overflow-auto whitespace-pre-wrap break-all bg-slate-900 p-4 text-[12px] leading-relaxed text-slate-200">{raw || '// response appears here'}</pre>
          </div>
        ) : (
          <div className={cn('flex-1 space-y-2 overflow-y-auto p-4', channel === 'whatsapp' && 'chat-wa', channel === 'telegram' && 'chat-tg', channel === 'email' && 'bg-muted/40')}>
            {msgs.length === 0 && <p className="pt-20 text-center text-sm text-muted-foreground">Try a quick command below, or attach a receipt, an e-invoice or a bank statement.</p>}
            {msgs.map((m) =>
              channel === 'email' ? (
                <div key={m.key} className={cn('rounded-md border bg-card p-3 text-sm shadow-sm', m.direction === 'in' ? 'ml-10' : 'mr-10')}>
                  <div className="mb-1 flex justify-between text-xs text-muted-foreground">
                    <span>{m.direction === 'in' ? `From: ${address}` : 'From: ledgerbot@demo.local'}</span>
                    <span>{timeAgo(m.at)}</span>
                  </div>
                  <p className="whitespace-pre-wrap">{m.text}</p>
                </div>
              ) : (
                <div key={m.key} className={cn('flex', m.direction === 'in' ? 'justify-end' : 'justify-start')}>
                  <div className={cn('max-w-[78%] whitespace-pre-wrap rounded-lg px-3 py-2 text-sm shadow-sm', m.direction === 'in' ? bubbleIn : 'bg-card')}>
                    {m.text}
                    <div className="mt-1 text-right text-[10px] opacity-60">{new Date(m.at).toLocaleTimeString('tr-TR', { hour: '2-digit', minute: '2-digit' })}</div>
                  </div>
                </div>
              ),
            )}
            <div ref={endRef} />
          </div>
        )}

        <div className="border-t bg-card p-3">
          <div className="mb-2 flex gap-1 overflow-x-auto pb-1">
            {QUICK.map((q) => (
              <button key={q} onClick={() => send({ text: q })} disabled={busy} className="shrink-0 rounded-full border px-2.5 py-1 text-xs hover:bg-accent disabled:opacity-50">
                {q}
              </button>
            ))}
          </div>
          <div className="mb-2 flex flex-wrap gap-1">
            <Button size="sm" variant="outline" disabled={busy} onClick={() => send({ media: sampleReceipt() })}>
              <Receipt /> Sample receipt
            </Button>
            <Button size="sm" variant="outline" disabled={busy} onClick={async () => send({ media: await sampleInvoice() })}>
              <FileText /> Sample e-invoice
            </Button>
            <Button size="sm" variant="outline" disabled={busy} onClick={() => send({ media: sampleStatement() })}>
              <FileSpreadsheet /> Sample bank statement
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={busy || !text}
              title="Sends the text as a voice note (the demo uses an offline speech-to-text stub)"
              onClick={() => send({ media: { mime: 'audio/ogg', filename: 'voice.ogg', data: textToBase64(text) } })}
            >
              <Mic /> As voice note
            </Button>
          </div>
          {channel === 'email' && <Input className="mb-2" placeholder="Subject" value={subject} onChange={(e) => setSubject(e.target.value)} />}
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (text.trim()) send({ text: text.trim() });
            }}
          >
            <input ref={fileRef} type="file" className="hidden" accept="image/*,.pdf,.xml,.csv,.xlsx" onChange={(e) => e.target.files?.[0] && attach(e.target.files[0])} />
            <Button type="button" size="icon" variant="ghost" onClick={() => fileRef.current?.click()} title="Attach a photo, PDF, e-invoice XML, CSV or Excel">
              <Paperclip />
            </Button>
            <Input placeholder={channel === 'email' ? 'Write the e-mail body…' : 'Type a message…'} value={text} onChange={(e) => setText(e.target.value)} />
            <Button type="submit" disabled={busy || !text.trim()}>
              {busy ? <Loader2 className="animate-spin" /> : <SendHorizonal />} Send
            </Button>
          </form>
          {err && <p className="mt-2 text-xs text-destructive">{err}</p>}
        </div>
      </Card>

      {/* understanding */}
      <div className="space-y-4">
        <Card>
          <CardHeader>
            <CardTitle>What ledgerbot understood</CardTitle>
            <CardDescription>Rules first; the LLM only when no rule matches. Numbers are computed in code.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-2">
            {!last?.intents?.length && <p className="text-sm text-muted-foreground">No message yet.</p>}
            {last?.intents?.map((i, n) => (
              <div key={n} className="rounded-md border p-2 text-sm">
                <div className="flex items-center justify-between">
                  <span className="font-mono text-xs font-semibold">{i.capability}</span>
                  <Badge variant={i.source === 'rules' ? 'success' : 'secondary'}>{i.source}</Badge>
                </div>
                {Object.entries(i.args ?? {}).map(([k, v]) => (
                  <div key={k} className="mt-1 flex justify-between text-xs">
                    <span className="text-muted-foreground">{k}</span>
                    <span className="font-medium">{v}</span>
                  </div>
                ))}
              </div>
            ))}
            {last?.language && <p className="text-xs text-muted-foreground">Reply language: {last.language}</p>}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>Drafts from this message</CardTitle>
            <CardDescription>Save mode: {store.state?.capabilities.save_mode} (roles can only be stricter)</CardDescription>
          </CardHeader>
          <CardContent className="space-y-2">
            {!last?.drafts?.length && <p className="text-sm text-muted-foreground">Nothing to record.</p>}
            {last?.drafts?.map((d) => (
              <div key={d.id} className="rounded-md border p-2 text-sm">
                <div className="mb-1 flex items-center justify-between">
                  <span className="font-mono text-xs">{d.capability}</span>
                  <StatusBadge status={d.status} />
                </div>
                <p className="whitespace-pre-wrap text-xs">{d.summary}</p>
                {d.status === 'pending' && (
                  <div className="mt-2 flex gap-2">
                    <Button size="sm" onClick={() => decide(d, 'approve')}>
                      <Check /> Approve
                    </Button>
                    <Button size="sm" variant="outline" onClick={() => decide(d, 'reject')}>
                      <X /> Reject
                    </Button>
                  </div>
                )}
              </div>
            ))}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-1">
              <ImageIcon className="h-4 w-4" /> Accepted inputs
            </CardTitle>
          </CardHeader>
          <CardContent className="text-xs text-muted-foreground">
            Text and chained commands, receipt photos and PDFs (vision model), UBL-TR e-invoice XML (parsed without AI), bank statements (CSV, Excel, PDF), voice notes (speech-to-text).
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

export function StatusBadge({ status }: { status: string }) {
  const v = status === 'committed' ? 'success' : status === 'pending' ? 'warning' : status === 'rejected' || status === 'failed' ? 'destructive' : 'secondary';
  return <Badge variant={v as any}>{status}</Badge>;
}
