import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Eye, KeyRound, Loader2, Lock, RefreshCw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { MarkdownMessage } from '@/components/shared/markdown-message';
import type { WorkspaceShareMessage, WorkspaceShareMeta } from '@/types/api';

// 公开分享页：/share/ws/:token。独立于主应用布局（无导航/无鉴权 store），
// 直接打 /share/api/ws/:token/* 端点（不走带 /api 前缀和 Bearer 头的 apiFetch
// —— cookie 才是这里的凭证，且该页面在未登录状态下渲染）。解锁 cookie 由
// unlock 端点 Set-Cookie；数据面用 TanStack Query 拉取 + 15s 轮询。

async function shareFetch<T>(path: string, init?: RequestInit): Promise<{ ok: boolean; status: number; data: T | null }> {
  const res = await fetch(path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
  });
  let data: T | null = null;
  try {
    data = (await res.json()) as T;
  } catch {
    data = null;
  }
  return { ok: res.ok, status: res.status, data };
}

interface WorkspaceSharePageProps {
  token: string;
}

export function WorkspaceSharePage({ token }: WorkspaceSharePageProps) {
  const { t } = useTranslation('workspaces');
  const [password, setPassword] = useState('');
  const [unlocking, setUnlocking] = useState(false);
  const [unlockError, setUnlockError] = useState(false);

  const base = `/share/api/ws/${encodeURIComponent(token)}`;

  const metaQuery = useQuery({
    queryKey: ['share-meta', token],
    queryFn: async () => {
      const res = await shareFetch<WorkspaceShareMeta>(`${base}/info`);
      if (res.status === 404) throw new Error('NOT_FOUND');
      if (!res.ok || !res.data) throw new Error('ERROR');
      return res.data;
    },
    retry: false,
  });
  const meta = metaQuery.data;
  const loadError = metaQuery.error instanceof Error ? metaQuery.error.message : null;

  // 已解锁后拉聊天内容：最新 300 条，15s 轮询（列表按消息 id reconcile，
  // 增量部分自然追加）。
  const messagesQuery = useQuery({
    queryKey: ['share-messages', token],
    queryFn: async () => {
      const res = await shareFetch<{ messages: WorkspaceShareMessage[]; hasMore: boolean }>(`${base}/messages?limit=300`);
      if (res.status === 404) throw new Error('NOT_FOUND');
      if (!res.ok || !res.data) throw new Error('ERROR');
      return res.data;
    },
    enabled: !!meta?.unlocked,
    refetchInterval: 15_000,
  });

  const handleUnlock = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!password || unlocking) return;
    setUnlocking(true);
    setUnlockError(false);
    const res = await shareFetch<WorkspaceShareMeta>(`${base}/unlock`, {
      method: 'POST',
      body: JSON.stringify({ password }),
    });
    setUnlocking(false);
    if (res.status === 404) {
      metaQuery.refetch();
      return;
    }
    if (res.ok && res.data?.unlocked) {
      setPassword('');
      await metaQuery.refetch();
    } else {
      setUnlockError(true);
    }
  };

  // 加载中
  if (metaQuery.isPending) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <Loader2 className="size-6 animate-spin text-warm-text-muted" aria-hidden="true" />
      </div>
    );
  }

  // 不可访问（不存在/已撤销/已过期——统一文案，不区分原因）
  if (loadError === 'NOT_FOUND') {
    return (
      <ShareShell>
        <div className="flex flex-col items-center gap-3 py-8 text-center">
          <Lock className="size-8 text-warm-text-muted" aria-hidden="true" />
          <p className="text-sm text-warm-text">{t('share.notFound')}</p>
        </div>
      </ShareShell>
    );
  }
  if (loadError === 'ERROR') {
    return (
      <ShareShell>
        <div className="flex flex-col items-center gap-3 py-8 text-center">
          <p className="text-sm text-warm-text-muted">{t('share.loadError')}</p>
          <Button variant="outline" size="sm" onClick={() => void metaQuery.refetch()}>
            <RefreshCw className="size-3.5" aria-hidden="true" />
            {t('common:actions.retry')}
          </Button>
        </div>
      </ShareShell>
    );
  }

  // 密码门
  if (meta && !meta.unlocked) {
    return (
      <ShareShell>
        <form onSubmit={handleUnlock} className="flex flex-col gap-4 py-6">
          <div className="flex flex-col items-center gap-2 text-center">
            <KeyRound className="size-8 text-warm-text-muted" aria-hidden="true" />
            <p className="text-sm font-medium text-warm-text">{t('share.passwordPromptTitle')}</p>
            <p className="text-xs text-warm-text-muted">{t('share.passwordPromptHint')}</p>
          </div>
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={t('share.passwordPlaceholder')}
            autoFocus
          />
          {unlockError && <p className="text-xs text-destructive">{t('share.wrongPassword')}</p>}
          <Button type="submit" disabled={!password || unlocking}>
            {unlocking && <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />}
            {t('share.unlock')}
          </Button>
          {meta.expiresAt && (
            <p className="text-center text-xs text-warm-text-muted">
              {t('share.expiresAt', { time: new Date(meta.expiresAt).toLocaleString() })}
            </p>
          )}
        </form>
      </ShareShell>
    );
  }

  // 已解锁：只读会话视图
  const messages = messagesQuery.data?.messages ?? [];
  return (
    <ShareShell>
      <div className="border-b border-warm-border px-4 py-3 flex items-center justify-between gap-3 shrink-0">
        <div className="min-w-0">
          <div className="truncate text-sm font-medium text-warm-text">{meta?.name}</div>
          {meta?.issueTitle && <div className="truncate text-xs text-warm-text-muted">{meta.issueTitle}</div>}
        </div>
        <span className="flex items-center gap-1 shrink-0 text-xs text-warm-text-muted">
          <Eye className="size-3.5" aria-hidden="true" />
          {t('share.readOnly')}
        </span>
      </div>
      <div className="flex-1 overflow-y-auto px-4 py-3 space-y-3 min-w-0">
        {messages.length === 0 && messagesQuery.isPending && (
          <div className="flex justify-center py-8">
            <Loader2 className="size-5 animate-spin text-warm-text-muted" aria-hidden="true" />
          </div>
        )}
        {messages.length === 0 && !messagesQuery.isPending && (
          <p className="py-8 text-center text-xs text-warm-text-muted">{t('share.empty')}</p>
        )}
        {messages.map((m) =>
          m.role === 'user' ? (
            <div key={m.id} className="whitespace-pre-wrap break-words rounded-md border-l-2 border-info/40 bg-info/10 px-3 py-2 text-sm text-foreground">
              {m.content}
            </div>
          ) : (
            <MarkdownMessage key={m.id} content={m.content} role="assistant" />
          ),
        )}
      </div>
    </ShareShell>
  );
}

// ShareShell 是页面级容器：居中卡片（未解锁态）或全高列（会话态由内容决定
// 高度——统一用全高列 + 内容区自适应，卡片感交给内部元素）。
function ShareShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col bg-background text-foreground">
      <div className="mx-auto flex w-full max-w-3xl flex-1 flex-col min-h-0 px-4 py-6 sm:py-10">
        {children}
      </div>
    </div>
  );
}
