import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Eye, KeyRound, Loader2, Lock, RefreshCw, SendHorizonal } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { MarkdownMessage } from '@/components/shared/markdown-message';
import type { WorkspaceShareMessage, WorkspaceShareMeta } from '@/types/api';

// 公开分享页：/share/ws/:token。独立于主应用布局（无导航/无鉴权 store），
// 直接打 /share/api/ws/:token/* 端点（不走带 /api 前缀和 Bearer 头的 apiFetch
// —— cookie 才是这里的凭证，且该页面在未登录状态下渲染）。解锁 cookie 由
// unlock 端点 Set-Cookie；数据面用 TanStack Query 拉取，实时性靠 /events 的
// SSE 信号（收到信号就重拉列表，再保留一个低频轮询兜底）。

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

// 分享页消息分页：最新一页用 latest，向上翻页用 before + cursor（服务端返回
// 的本页最老原始行 id——可见消息全是 text，翻页游标必须按原始行算才不会卡住）。
interface ShareMessagesPage {
  messages: WorkspaceShareMessage[];
  hasMore: boolean;
  cursor?: string;
}

// 首屏只取最新一页：打开页面看的是「最新消息」，更早的内容等用户滚到顶部再加载。
const INITIAL_PAGE_SIZE = 30;
const OLDER_PAGE_SIZE = 50;

interface WorkspaceSharePageProps {
  token: string;
}

export function WorkspaceSharePage({ token }: WorkspaceSharePageProps) {
  const { t } = useTranslation('workspaces');
  const qc = useQueryClient();
  const [password, setPassword] = useState('');
  const [unlocking, setUnlocking] = useState(false);
  const [unlockError, setUnlockError] = useState(false);
  const [chatDraft, setChatDraft] = useState('');
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState(false);
  const [liveConnected, setLiveConnected] = useState(false);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const contentRef = useRef<HTMLDivElement | null>(null);
  const stickToBottomRef = useRef(true);
  // 向上翻页：olderMessages 是首屏窗口之外的更老消息，olderPage 是翻页游标和
  // 「还有没有更老的」。首屏窗口会随新消息漂移，一旦开始翻页就固定用翻页游标，
  // 所以 olderPage 为 null 时读首屏那一页的 cursor/hasMore。
  const [olderMessages, setOlderMessages] = useState<WorkspaceShareMessage[]>([]);
  const [olderPage, setOlderPage] = useState<{ cursor: string; hasMore: boolean } | null>(null);
  const [loadingOlder, setLoadingOlder] = useState(false);
  // 滚动事件会连续触发，state 更新是异步的，必须用 ref 同步防重入，
  // 否则一次上滑会把同一页请求多次。
  const loadingOlderRef = useRef(false);
  const pendingAnchorRef = useRef<{ height: number; top: number } | null>(null);
  const autoFillRef = useRef(0);

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

  // 已解锁后拉聊天内容：首屏最新一页。SSE 连着时靠 /events 的信号即时刷新，
  // 只是留一个 2 分钟的低频轮询兜底（长连接被代理掐断时的最后防线）。
  const messagesQuery = useQuery({
    queryKey: ['share-messages', token],
    queryFn: async () => {
      const res = await shareFetch<ShareMessagesPage>(`${base}/messages?limit=${INITIAL_PAGE_SIZE}`);
      if (res.status === 404) throw new Error('NOT_FOUND');
      // 解锁 cookie 过期（1 小时）——刷新 meta 把页面送回密码门。
      if (res.status === 401) {
        void qc.invalidateQueries({ queryKey: ['share-meta', token] });
        throw new Error('LOCKED');
      }
      if (!res.ok || !res.data) throw new Error('ERROR');
      return res.data;
    },
    enabled: !!meta?.unlocked,
    refetchInterval: liveConnected ? 120_000 : 15_000,
  });

  // 实时更新通道：EventSource 自带 cookie，unlock cookie 即凭证。错误时按指数
  // 退避重连（解锁 cookie 过期、分享被撤销、服务重启都走这条路），重连期间由
  // messagesQuery 的轮询兜底。
  useEffect(() => {
    // 回到未解锁态时由上一次的 cleanup 关掉连接并把状态复位，这里直接退出。
    if (!meta?.unlocked) return;
    let es: EventSource | null = null;
    let retryTimer: number | undefined;
    let debounceTimer: number | undefined;
    let attempt = 0;
    let disposed = false;

    // 服务端已按 500ms 合并信号，这里再压一层：一次流式输出大约每秒重拉一次，
    // 既看得到逐段增长的内容，也不会把整张消息表刷爆。
    const scheduleRefresh = () => {
      if (debounceTimer) return;
      debounceTimer = window.setTimeout(() => {
        debounceTimer = undefined;
        void qc.invalidateQueries({ queryKey: ['share-messages', token] });
      }, 1000);
    };

    const connect = () => {
      if (disposed) return;
      es = new EventSource(`${base}/events`);
      es.onopen = () => {
        attempt = 0;
        setLiveConnected(true);
      };
      es.onmessage = () => scheduleRefresh();
      es.onerror = () => {
        setLiveConnected(false);
        es?.close();
        es = null;
        if (disposed) return;
        attempt = Math.min(attempt + 1, 5);
        retryTimer = window.setTimeout(connect, Math.min(30_000, 1000 * 2 ** attempt));
      };
    };
    connect();

    return () => {
      disposed = true;
      if (retryTimer) window.clearTimeout(retryTimer);
      if (debounceTimer) window.clearTimeout(debounceTimer);
      es?.close();
      setLiveConnected(false);
    };
  }, [base, meta?.unlocked, qc, token]);

  // 首屏窗口 + 已翻出来的更老消息合并渲染。重叠时以首屏窗口里的版本为准
  // （流式更新会改写内容），顺序仍是时间正序。
  const messages = useMemo(() => {
    const tail = messagesQuery.data?.messages ?? [];
    if (olderMessages.length === 0) return tail;
    const tailIds = new Set(tail.map((m) => m.id));
    const head = olderMessages.filter((m) => !tailIds.has(m.id));
    return head.length === 0 ? tail : [...head, ...tail];
  }, [olderMessages, messagesQuery.data]);

  const olderCursor = olderPage ? olderPage.cursor : (messagesQuery.data?.cursor ?? '');
  const hasMoreOlder = olderPage ? olderPage.hasMore : !!messagesQuery.data?.hasMore;

  // 向上翻页：用 before + cursor 取更老的一页，插到列表前面并保持视觉锚点
  // （记录插入前后的高度差补回 scrollTop），否则内容会在用户眼前跳走。
  const loadOlder = useCallback(async () => {
    if (loadingOlderRef.current || !hasMoreOlder || !olderCursor) return;
    loadingOlderRef.current = true;
    setLoadingOlder(true);
    const el = scrollRef.current;
    const anchor = el ? { height: el.scrollHeight, top: el.scrollTop } : null;
    let collected: WorkspaceShareMessage[] = [];
    let cursor = olderCursor;
    let more = false;
    let failed = false;
    try {
      // 有的页可能全是 tool/thinking（公开视图里不可见），连翻几页直到有内容。
      for (let page = 0; page < 5 && cursor; page += 1) {
        const res = await shareFetch<ShareMessagesPage>(
          `${base}/messages?limit=${OLDER_PAGE_SIZE}&before=${encodeURIComponent(cursor)}`,
        );
        if (!res.ok || !res.data) {
          failed = true;
          break;
        }
        collected = [...res.data.messages, ...collected];
        more = res.data.hasMore;
        cursor = res.data.cursor ?? '';
        if (collected.length > 0) break;
      }
    } catch {
      failed = true;
    }
    if (!failed) {
      pendingAnchorRef.current = anchor;
      setOlderMessages((prev) => {
        const known = new Set(prev.map((m) => m.id));
        return [...collected.filter((m) => !known.has(m.id)), ...prev];
      });
      setOlderPage({ cursor, hasMore: more && cursor !== '' });
    }
    loadingOlderRef.current = false;
    setLoadingOlder(false);
  }, [base, hasMoreOlder, olderCursor]);

  // 插入更老的消息后把滚动位置补回去，保持用户当前看到的内容不动。
  useLayoutEffect(() => {
    const el = scrollRef.current;
    const anchor = pendingAnchorRef.current;
    if (!el || !anchor) return;
    pendingAnchorRef.current = null;
    el.scrollTop = anchor.top + (el.scrollHeight - anchor.height);
  }, [olderMessages]);

  // 首屏只有最新一页；如果内容撑不满一屏就滚不动、也就触发不了「滚到顶部加载」。
  // 这时主动补几页（工具调用多的会话首屏可见消息可能很少），最多补 5 轮。
  useEffect(() => {
    if (!messagesQuery.isSuccess) return;
    const el = scrollRef.current;
    if (!el) return;
    if (el.scrollHeight > el.clientHeight + 8) {
      autoFillRef.current = 0;
      return;
    }
    if (autoFillRef.current >= 5 || loadingOlderRef.current) return;
    if (!hasMoreOlder || !olderCursor) return;
    autoFillRef.current += 1;
    // 放到下一拍再拉，避免在 effect 里同步触发渲染链。
    const timer = window.setTimeout(() => void loadOlder(), 0);
    return () => window.clearTimeout(timer);
  }, [messages, messagesQuery.isSuccess, hasMoreOlder, olderCursor, loadOlder]);

  // 新内容到达时贴底；用户主动往上翻过就不再抢滚动位置。
  useEffect(() => {
    const el = scrollRef.current;
    if (el && stickToBottomRef.current) el.scrollTop = el.scrollHeight;
  }, [messagesQuery.data]);

  // 贴底状态下持续跟随内容高度：首屏 markdown/代码块异步撑高、流式输出变长时
  // 都能保持在底部（进入页面即默认停在最新一条）。
  useEffect(() => {
    if (!meta?.unlocked) return;
    const el = scrollRef.current;
    const content = contentRef.current;
    if (!el || !content || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(() => {
      if (stickToBottomRef.current) el.scrollTop = el.scrollHeight;
    });
    ro.observe(content);
    return () => ro.disconnect();
  }, [meta?.unlocked]);

  // 视口变化（旋转、移动端键盘/地址栏）后消息区高度变了，贴底状态下重新贴底。
  useEffect(() => {
    const onResize = () => {
      const el = scrollRef.current;
      if (el && stickToBottomRef.current) el.scrollTop = el.scrollHeight;
    };
    window.addEventListener('resize', onResize);
    window.visualViewport?.addEventListener('resize', onResize);
    return () => {
      window.removeEventListener('resize', onResize);
      window.visualViewport?.removeEventListener('resize', onResize);
    };
  }, []);

  const handleScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    stickToBottomRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 64;
    if (el.scrollTop < 80) void loadOlder();
  };

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
      <div className="flex min-h-dvh items-center justify-center bg-background">
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
        <form onSubmit={handleUnlock} className="flex w-full flex-col gap-4 py-6">
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

  const handleSendChat = async (e?: React.FormEvent) => {
    e?.preventDefault();
    const content = chatDraft.trim();
    if (!content || sending) return;
    setSending(true);
    setSendError(false);
    const res = await shareFetch<{ status: string }>(`${base}/chat`, {
      method: 'POST',
      body: JSON.stringify({ content }),
    });
    setSending(false);
    if (res.ok) {
      setChatDraft('');
      stickToBottomRef.current = true;
      // 立即拉一次会话（自己的消息先出现），回复靠 /events 信号。
      void qc.invalidateQueries({ queryKey: ['share-messages', token] });
    } else if (res.status === 403 || res.status === 404) {
      // 权限被收回或分享被撤销 —— 刷新 meta 让 UI 回到正确状态。
      void qc.invalidateQueries({ queryKey: ['share-meta', token] });
    } else {
      setSendError(true);
    }
  };

  // 已解锁：会话视图。外层固定为一屏高，只有消息区滚动——输入框始终钉在
  // 窗口底部，移动端浏览器地址栏收起/展开也不会把输入框顶出可视区。
  return (
    <ShareShell variant="chat">
      <div className="flex shrink-0 items-center justify-between gap-3 border-b border-warm-border px-4 py-3">
        <div className="min-w-0">
          <div className="truncate text-sm font-medium text-warm-text">{meta?.name}</div>
          {meta?.issueTitle && <div className="truncate text-xs text-warm-text-muted">{meta.issueTitle}</div>}
        </div>
        <span className="flex shrink-0 items-center gap-1 text-xs text-warm-text-muted">
          <Eye className="size-3.5" aria-hidden="true" />
          {meta?.allowChat ? t('share.chatEnabled') : t('share.readOnly')}
        </span>
      </div>
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 py-3"
      >
        <div ref={contentRef} className="space-y-3">
          {loadingOlder && (
            <div className="flex justify-center py-2">
              <Loader2 className="size-4 animate-spin text-warm-text-muted" aria-hidden="true" />
            </div>
          )}
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
      </div>
      {meta?.allowChat && (
        <form
          onSubmit={handleSendChat}
          className="flex shrink-0 items-center gap-2 border-t border-warm-border px-4 py-3"
        >
          <Input
            value={chatDraft}
            onChange={(e) => setChatDraft(e.target.value)}
            placeholder={t('share.chatPlaceholder')}
            maxLength={4000}
          />
          <Button type="submit" size="icon" disabled={!chatDraft.trim() || sending} aria-label={t('share.chatSend')}>
            {sending ? <Loader2 className="size-4 animate-spin" aria-hidden="true" /> : <SendHorizonal className="size-4" aria-hidden="true" />}
          </Button>
          {sendError && <span className="shrink-0 text-xs text-destructive">{t('share.chatSendFailed')}</span>}
        </form>
      )}
    </ShareShell>
  );
}

// ShareShell 是页面级容器：
// - chat：h-dvh 全高一屏，只有消息区滚动（输入框钉底，移动端地址栏变化安全）；
// - 其它（加载/密码门/错误）：居中的窄栏，内容不足一屏时垂直居中。
function ShareShell({ children, variant = 'card' }: { children: React.ReactNode; variant?: 'card' | 'chat' }) {
  if (variant === 'chat') {
    return (
      <div className="flex h-dvh flex-col overflow-hidden bg-background text-foreground">
        <div className="mx-auto flex min-h-0 w-full max-w-3xl flex-1 flex-col">{children}</div>
      </div>
    );
  }
  return (
    <div className="flex min-h-dvh flex-col bg-background text-foreground">
      <div className="mx-auto flex w-full max-w-3xl flex-1 flex-col items-center justify-center px-4 py-6 sm:py-10">
        {children}
      </div>
    </div>
  );
}
