import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Check, Copy, Link2, Loader2, RefreshCw, Share2, Trash2 } from 'lucide-react';
import { toast } from 'sonner';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Checkbox } from '@/components/ui/checkbox';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { api } from '@/lib/api';
import { copyTextToClipboard } from '@/lib/copy-to-clipboard';
import type { WorkspaceShare } from '@/types/api';

interface WorkspaceShareDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  workspaceId: string;
}

// 预设时效（小时）。分享必须有时效——不允许"永久"。
const EXPIRY_OPTIONS: { hours: number; labelKey: string }[] = [
  { hours: 1, labelKey: 'share.expiry.1h' },
  { hours: 24, labelKey: 'share.expiry.24h' },
  { hours: 72, labelKey: 'share.expiry.72h' },
  { hours: 24 * 7, labelKey: 'share.expiry.7d' },
  { hours: 24 * 30, labelKey: 'share.expiry.30d' },
];

function generatePassword(): string {
  const alphabet = 'abcdefghjkmnpqrstuvwxyzACDEFGHJKLMNPQRSTUVWXYZ23456789';
  const buf = new Uint32Array(10);
  crypto.getRandomValues(buf);
  return Array.from(buf, (n) => alphabet[n % alphabet.length]).join('');
}

function formatDateTime(iso: string): string {
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}

export function WorkspaceShareDialog({ open, onOpenChange, workspaceId }: WorkspaceShareDialogProps) {
  const { t } = useTranslation('workspaces');
  const qc = useQueryClient();

  const [password, setPassword] = useState(() => generatePassword());
  const [expiryHours, setExpiryHours] = useState(24 * 7);
  const [allowChat, setAllowChat] = useState(false);
  // 创建成功的一次性展示态：链接 + 密码（token 不再可取回）。
  const [created, setCreated] = useState<{ url: string; password: string } | null>(null);
  const [copiedField, setCopiedField] = useState<'url' | 'password' | null>(null);

  const sharesQuery = useQuery({
    queryKey: ['workspace-shares', workspaceId],
    queryFn: () => api.listWorkspaceShares(workspaceId),
    enabled: open,
  });

  const createMutation = useMutation({
    mutationFn: async () => {
      const expiresAt = new Date(Date.now() + expiryHours * 3600_000).toISOString();
      return api.createWorkspaceShare(workspaceId, password, expiresAt, allowChat);
    },
    onSuccess: (res) => {
      const url = res.url
        || `${window.location.origin}${res.path}`;
      setCreated({ url, password });
      void qc.invalidateQueries({ queryKey: ['workspace-shares', workspaceId] });
    },
    onError: () => {
      toast.error(t('share.createFailed'));
    },
  });

  const allowChatMutation = useMutation({
    mutationFn: ({ shareId, value }: { shareId: number; value: boolean }) =>
      api.patchWorkspaceShare(workspaceId, shareId, value),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['workspace-shares', workspaceId] });
    },
    onError: () => {
      toast.error(t('share.allowChatUpdateFailed'));
    },
  });

  const revokeMutation = useMutation({
    mutationFn: (shareId: number) => api.revokeWorkspaceShare(workspaceId, shareId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['workspace-shares', workspaceId] });
    },
    onError: () => {
      toast.error(t('share.revokeFailed'));
    },
  });

  const activeShares = useMemo(
    () => (sharesQuery.data?.shares ?? []).filter((s) => !s.revokedAt),
    [sharesQuery.data],
  );

  const handleClose = (next: boolean) => {
    if (!next) {
      // 关闭时清掉一次性展示态，重置表单供下次打开。
      setCreated(null);
      setCopiedField(null);
      setPassword(generatePassword());
      setExpiryHours(24 * 7);
    }
    onOpenChange(next);
  };

  const copyField = async (field: 'url' | 'password', value: string) => {
    const ok = await copyTextToClipboard(value);
    if (ok) {
      setCopiedField(field);
      setTimeout(() => setCopiedField(null), 2000);
    } else {
      toast.error(t('share.copyFailed'));
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent className="sm:max-w-[520px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Share2 className="size-4" aria-hidden="true" />
            {t('share.title')}
          </DialogTitle>
          <DialogDescription>{t('share.description')}</DialogDescription>
        </DialogHeader>

        {created ? (
          <div className="py-2 space-y-3">
            <div className="space-y-1.5">
              <div className="text-xs font-medium text-warm-text-muted">{t('share.linkLabel')}</div>
              <div className="flex items-center gap-2">
                <Input readOnly value={created.url} className="font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
                <Button
                  variant="outline"
                  size="icon"
                  aria-label={t('share.copyLink')}
                  title={t('share.copyLink')}
                  onClick={() => void copyField('url', created.url)}
                >
                  {copiedField === 'url' ? <Check className="size-3.5 text-success" aria-hidden="true" /> : <Copy className="size-3.5" aria-hidden="true" />}
                </Button>
              </div>
            </div>
            <div className="space-y-1.5">
              <div className="text-xs font-medium text-warm-text-muted">{t('share.passwordLabel')}</div>
              <div className="flex items-center gap-2">
                <Input readOnly value={created.password} className="font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
                <Button
                  variant="outline"
                  size="icon"
                  aria-label={t('share.copyPassword')}
                  title={t('share.copyPassword')}
                  onClick={() => void copyField('password', created.password)}
                >
                  {copiedField === 'password' ? <Check className="size-3.5 text-success" aria-hidden="true" /> : <Copy className="size-3.5" aria-hidden="true" />}
                </Button>
              </div>
            </div>
            <p className="text-xs text-warm-text-muted">{t('share.createdOnce')}</p>
            <Button variant="outline" className="w-full" onClick={() => handleClose(false)}>
              {t('common:actions.close')}
            </Button>
          </div>
        ) : (
          <div className="py-2 space-y-4">
            <div className="space-y-1.5">
              <div className="text-xs font-medium text-warm-text-muted">{t('share.passwordLabel')}</div>
              <div className="flex items-center gap-2">
                <Input
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="font-mono text-xs"
                  autoComplete="off"
                />
                <Button
                  variant="outline"
                  size="icon"
                  aria-label={t('share.generatePassword')}
                  title={t('share.generatePassword')}
                  onClick={() => setPassword(generatePassword())}
                >
                  <RefreshCw className="size-3.5" aria-hidden="true" />
                </Button>
              </div>
            </div>
            <div className="space-y-1.5">
              <div className="text-xs font-medium text-warm-text-muted">{t('share.expiryLabel')}</div>
              <Select value={String(expiryHours)} onValueChange={(v) => setExpiryHours(Number(v))}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {EXPIRY_OPTIONS.map((opt) => (
                    <SelectItem key={opt.hours} value={String(opt.hours)}>
                      {t(opt.labelKey)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-xs text-warm-text-muted">{t('share.expiryHint')}</p>
            </div>

            <label className="flex items-start gap-2 cursor-pointer">
              <Checkbox
                checked={allowChat}
                onCheckedChange={(v) => setAllowChat(v === true)}
                className="mt-0.5"
                aria-label={t('share.allowChatLabel')}
              />
              <span className="text-xs">
                <span className="font-medium text-warm-text">{t('share.allowChatLabel')}</span>
                <span className="block text-warm-text-muted">{t('share.allowChatHint')}</span>
              </span>
            </label>

            <Button
              className="w-full"
              disabled={createMutation.isPending || password.trim().length < 4}
              onClick={() => createMutation.mutate()}
            >
              {createMutation.isPending && <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />}
              {t('share.create')}
            </Button>
          </div>
        )}

        {activeShares.length > 0 && (
          <div className="border-t border-warm-border pt-3 mt-1">
            <div className="text-xs font-medium text-warm-text-muted mb-2">{t('share.activeList')}</div>
            <ul className="space-y-1.5 max-h-40 overflow-y-auto">
              {activeShares.map((s: WorkspaceShare) => (
                <li key={s.id} className="flex items-center justify-between gap-2 text-xs">
                  <span className="flex items-center gap-1.5 text-warm-text min-w-0">
                    <Link2 className="size-3 text-warm-text-muted shrink-0" aria-hidden="true" />
                    <span className="truncate">{t('share.expiresAt', { time: formatDateTime(s.expiresAt) })}</span>
                    <label
                      className="flex items-center gap-1 shrink-0 cursor-pointer text-warm-text-muted"
                      title={t('share.allowChatHint')}
                    >
                      <Checkbox
                        checked={s.allowChat}
                        disabled={allowChatMutation.isPending}
                        onCheckedChange={(v) => allowChatMutation.mutate({ shareId: s.id, value: v === true })}
                        className="size-3"
                        aria-label={t('share.allowChatLabel')}
                      />
                      {t('share.allowChatShort')}
                    </label>
                  </span>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 px-1.5 text-destructive hover:text-destructive shrink-0"
                    disabled={revokeMutation.isPending}
                    aria-label={t('share.revoke')}
                    title={t('share.revoke')}
                    onClick={() => revokeMutation.mutate(s.id)}
                  >
                    <Trash2 className="size-3.5" aria-hidden="true" />
                  </Button>
                </li>
              ))}
            </ul>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
