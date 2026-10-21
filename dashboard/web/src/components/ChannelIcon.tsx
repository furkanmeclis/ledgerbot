import { Braces, Mail, MessageCircle, Send, Terminal } from 'lucide-react';
import { cn } from '@/lib/utils';

export type ChannelKey = 'whatsapp' | 'telegram' | 'email' | 'api';

export const CHANNELS: { key: ChannelKey; label: string; addressKind: string; hint: string; color: string }[] = [
  { key: 'whatsapp', label: 'WhatsApp', addressKind: 'wuzapi', hint: 'WuzAPI per company · central number · Meta Cloud WABA', color: 'bg-[#25D366]' },
  { key: 'telegram', label: 'Telegram', addressKind: 'telegram', hint: 'Bot API · webhook or long polling', color: 'bg-[#2AABEE]' },
  { key: 'email', label: 'E-mail', addressKind: 'email', hint: 'IMAP polling or inbound webhook · SMTP replies', color: 'bg-[#EA4335]' },
  { key: 'api', label: 'REST API', addressKind: 'dev', hint: 'POST /v1/messages for host developers', color: 'bg-[#334155]' },
];

export function ChannelIcon({ channel, className }: { channel: ChannelKey | string; className?: string }) {
  const c = CHANNELS.find((x) => x.key === channel);
  const Icon = channel === 'whatsapp' ? MessageCircle : channel === 'telegram' ? Send : channel === 'email' ? Mail : channel === 'api' ? Braces : Terminal;
  return (
    <span className={cn('inline-flex h-7 w-7 items-center justify-center rounded-md text-white', c?.color ?? 'bg-slate-500', className)}>
      <Icon className="h-4 w-4" />
    </span>
  );
}
