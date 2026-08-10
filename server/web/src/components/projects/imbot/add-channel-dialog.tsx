import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from '@tanstack/react-router';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Button } from '@/components/ui/button';
import { imbotApi } from '@/lib/imbot-api';
import type { ImBotChannelType, ImBotConnectionMode } from '@/types/imbot';

interface Props {
  projectId: number;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

// Adapters: Lark (WS), Telegram (long-poll), DingTalk (Stream), WeCom (AI-Bot
// WebSocket long connection OR self-built-app webhook) and WeChat (QR-scan
// ClawBot). Labels come from i18n (labelKey).
const TYPES: { key: ImBotChannelType; labelKey: string }[] = [
  { key: 'lark', labelKey: 'imbot.typeLark' },
  { key: 'telegram', labelKey: 'imbot.typeTelegram' },
  { key: 'dingtalk', labelKey: 'imbot.typeDingtalk' },
  { key: 'wework', labelKey: 'imbot.typeWework' },
  { key: 'wechat', labelKey: 'imbot.typeWechat' },
];

// Per-channel credential fields. `secret` masks the input; `optional` skips it in
// validation; `webhookOnly`/`streamOnly` fields are only shown/required in that
// connection mode. WeCom carries two products: the AI-Bot long connection
// (streamOnly bot_id) and the self-built app webhook (webhookOnly corp_id/agent_id
// /token/aes_key); `secret` is shared by both.
type CredField = {
  key: string;
  labelKey: string;
  secret?: boolean;
  optional?: boolean;
  webhookOnly?: boolean;
  streamOnly?: boolean;
};

const CRED_FIELDS: Record<ImBotChannelType, CredField[]> = {
  lark: [
    { key: 'app_id', labelKey: 'imbot.appId' },
    { key: 'app_secret', labelKey: 'imbot.appSecret', secret: true },
  ],
  telegram: [{ key: 'bot_token', labelKey: 'imbot.botToken', secret: true }],
  dingtalk: [
    { key: 'client_id', labelKey: 'imbot.clientId' },
    { key: 'client_secret', labelKey: 'imbot.clientSecret', secret: true },
    { key: 'robot_code', labelKey: 'imbot.robotCode', optional: true },
  ],
  wework: [
    { key: 'bot_id', labelKey: 'imbot.botId', streamOnly: true },
    { key: 'corp_id', labelKey: 'imbot.corpId', webhookOnly: true },
    { key: 'agent_id', labelKey: 'imbot.agentId', webhookOnly: true },
    { key: 'secret', labelKey: 'imbot.secret', secret: true },
    { key: 'token', labelKey: 'imbot.token', webhookOnly: true },
    { key: 'aes_key', labelKey: 'imbot.aesKey', secret: true, webhookOnly: true },
  ],
  // WeChat needs no pasted secret — the bot_token is minted by QR scan, so it has
  // no credential fields; selecting it switches this dialog to the scan flow.
  wechat: [],
};

export function AddChannelDialog({ projectId, open, onOpenChange }: Props) {
  const { t } = useTranslation('projects');
  const qc = useQueryClient();
  const navigate = useNavigate();

  const [channelType, setChannelType] = useState<ImBotChannelType>('lark');
  const [mode, setMode] = useState<ImBotConnectionMode>('stream');
  const [name, setName] = useState('');
  const [creds, setCreds] = useState<Record<string, string>>({});
  const [webhookSecret, setWebhookSecret] = useState('');
  const [error, setError] = useState<string | null>(null);

  // WeChat connects by QR scan (no secret to paste): it hides the mode toggle +
  // credential form and swaps the footer for a "scan to connect" action.
  const isWechat = channelType === 'wechat';

  const reset = () => {
    setChannelType('lark');
    setMode('stream');
    setName('');
    setCreds({});
    setWebhookSecret('');
    setError(null);
  };

  const pickType = (key: ImBotChannelType) => {
    setChannelType(key);
    setCreds({});
    // All channels default to the LAN-friendly long-connection mode, including
    // WeCom now that its AI-Bot supports a WebSocket long connection.
    if (key === 'wechat') setMode('stream');
    else setMode('stream');
  };

  // WeChat: mint a one-time QR-onboarding link for this project and jump to the
  // scan page (same secure token flow the assistant uses, minus the chat step).
  const wechatConnect = useMutation({
    mutationFn: () => imbotApi.issueWechatLink(projectId, name.trim() || undefined),
    onSuccess: (data) => {
      reset();
      onOpenChange(false);
      const token = data.url.split('/').pop() ?? '';
      void navigate({ to: '/imbot/onboarding/$token', params: { token } });
    },
    onError: (e) => setError(e instanceof Error ? e.message : String(e)),
  });

  const fieldVisible = (f: CredField) =>
    (!f.webhookOnly || mode === 'webhook') && (!f.streamOnly || mode === 'stream');
  const fieldRequired = (f: CredField) => !f.optional && fieldVisible(f);
  const fields = CRED_FIELDS[channelType];
  // WeCom verifies via token/aes_key (credential fields), not the shared secret.
  const showWebhookSecret = mode === 'webhook' && channelType !== 'wework';

  const create = useMutation({
    mutationFn: () => {
      const credential: Record<string, unknown> = {};
      for (const f of fields) {
        if (!fieldVisible(f)) continue;
        const v = (creds[f.key] ?? '').trim();
        if (v) credential[f.key] = v;
      }
      return imbotApi.createChannel(projectId, {
        channel_type: channelType,
        name: name.trim(),
        connection_mode: mode,
        webhook_secret: showWebhookSecret ? webhookSecret.trim() : undefined,
        credential,
      });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['imbot-channels', projectId] });
      reset();
      onOpenChange(false);
    },
    onError: (e) => setError(e instanceof Error ? e.message : String(e)),
  });

  const credsValid = fields
    .filter(fieldRequired)
    .every((f) => (creds[f.key] ?? '').trim().length > 0);
  const canSubmit = name.trim().length > 0 && credsValid && !create.isPending;

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) reset(); onOpenChange(o); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('imbot.addChannel')}</DialogTitle>
          <DialogDescription>{t('imbot.addChannelDesc')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {/* Step 1: channel type */}
          <div className="grid gap-2">
            <Label className="text-sm">{t('imbot.channelType')}</Label>
            <div className="grid grid-cols-2 gap-2">
              {TYPES.map((tp) => (
                <button
                  key={tp.key}
                  type="button"
                  onClick={() => pickType(tp.key)}
                  className={
                    'rounded-md border px-3 py-2 text-sm text-left transition-colors ' +
                    (channelType === tp.key
                      ? 'border-brand bg-brand/5 text-warm-text'
                      : 'border-warm-border text-warm-text-muted hover:bg-warm-muted')
                  }
                >
                  {t(tp.labelKey)}
                </button>
              ))}
            </div>
          </div>

          {/* Step 2: connection mode (hidden for WeChat — always stream). */}
          {!isWechat && (
          <div className="grid gap-2">
            <Label className="text-sm">{t('imbot.connectionMode')}</Label>
            <div className="grid grid-cols-2 gap-2">
              <button
                type="button"
                onClick={() => setMode('stream')}
                className={
                  'rounded-md border px-3 py-2 text-left transition-colors ' +
                  (mode === 'stream'
                    ? 'border-brand bg-brand/5 text-warm-text'
                    : 'border-warm-border text-warm-text-muted hover:bg-warm-muted')
                }
              >
                <span className="text-sm">{t('imbot.modeStream')}</span>
                <span className="block text-xs text-warm-text-muted">{t('imbot.modeStreamHint')}</span>
              </button>
              <button
                type="button"
                onClick={() => setMode('webhook')}
                className={
                  'rounded-md border px-3 py-2 text-left transition-colors ' +
                  (mode === 'webhook'
                    ? 'border-brand bg-brand/5 text-warm-text'
                    : 'border-warm-border text-warm-text-muted hover:bg-warm-muted')
                }
              >
                <span className="text-sm">{t('imbot.modeWebhook')}</span>
                <span className="block text-xs text-warm-text-muted">{t('imbot.modeWebhookHint')}</span>
              </button>
            </div>
          </div>
          )}

          {/* Step 3: name + credentials */}
          <div className="grid gap-2">
            <Label htmlFor="imbot-name" className="text-sm">{t('imbot.name')}</Label>
            <Input id="imbot-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={t('imbot.namePlaceholder')} />
          </div>

          {isWechat && (
            <p className="text-xs text-warm-text-muted">{t('imbot.wechatScanHint')}</p>
          )}

          {fields.filter(fieldVisible).map((f) => (
            <div key={f.key} className="grid gap-2">
              <Label htmlFor={`imbot-${f.key}`} className="text-sm">
                {t(f.labelKey)}
                {f.optional && <span className="text-warm-text-muted"> ({t('imbot.optional')})</span>}
              </Label>
              <Input
                id={`imbot-${f.key}`}
                value={creds[f.key] ?? ''}
                onChange={(e) => setCreds((c) => ({ ...c, [f.key]: e.target.value }))}
                type={f.secret ? 'password' : 'text'}
                autoComplete="off"
              />
            </div>
          ))}

          {showWebhookSecret && (
            <div className="grid gap-2">
              <Label htmlFor="imbot-webhook-secret" className="text-sm">{t('imbot.webhookSecret')}</Label>
              <Input id="imbot-webhook-secret" value={webhookSecret} onChange={(e) => setWebhookSecret(e.target.value)} autoComplete="off" />
              <p className="text-xs text-warm-text-muted">{t('imbot.webhookUrlHint')}</p>
            </div>
          )}

          {error && <p className="text-sm text-destructive">{error}</p>}
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={create.isPending || wechatConnect.isPending}>
            {t('imbot.cancel')}
          </Button>
          {isWechat ? (
            <Button
              type="button"
              onClick={() => { setError(null); wechatConnect.mutate(); }}
              disabled={wechatConnect.isPending}
            >
              {wechatConnect.isPending ? t('imbot.saving') : t('imbot.wechatScanConnect')}
            </Button>
          ) : (
            <Button type="button" onClick={() => { setError(null); create.mutate(); }} disabled={!canSubmit}>
              {create.isPending ? t('imbot.saving') : t('imbot.save')}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
