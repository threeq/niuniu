import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { MINIMUM_TOOLS } from '@/lib/system-deps-gate'
import { openClaudeLoginTerminal, openCodexLoginTerminal, openExternalSmart } from '@/lib/shell'
import type { SystemDepsInfo, ToolStatus } from '@/types/api'
import { GitIdentityPanel } from '@/components/system-deps/GitIdentityPanel'
import { CLILoginTerminalDialog } from '@/components/system-deps/CLILoginTerminalDialog'
import { useAuthStore } from '@/stores/auth-store'

// downloadUrl for tesseract points at the in-house OCR install guide (#284);
// keep it in sync with ocrGuideURL in internal/service/system_deps.go and
// cmd/niuniu-mcp/image_tools.go.
const toolLabels: Record<string, { label: string; downloadUrl: string }> = {
  node:      { label: 'Node.js',  downloadUrl: 'https://nodejs.org/' },
  python3:   { label: 'Python 3', downloadUrl: 'https://www.python.org/downloads/' },
  git:       { label: 'Git',      downloadUrl: 'https://git-scm.com/downloads' },
  claude:    { label: 'Claude Code CLI', downloadUrl: 'https://docs.claude.com/en/docs/claude-code/setup' },
  codex:     { label: 'Codex CLI', downloadUrl: 'https://github.com/openai/codex' },
  qwen:      { label: 'Qwen Code CLI', downloadUrl: 'https://qwen.code/' },
  omp:       { label: 'Oh My Pi (omp) CLI', downloadUrl: 'https://github.com/can1357/oh-my-pi' },
  goose:     { label: 'Goose CLI', downloadUrl: 'https://github.com/block/goose' },
  tesseract: { label: 'Tesseract OCR', downloadUrl: 'https://www.niu6ai.com/docs/install/ocr-tesseract' },
  // cairosvg (issue #472): optional pip package powering PNG export for the
  // fireworks diagram scene; SVG always works without it. Installs via pip on
  // every platform (NOT the OS PM), so commandFor() short-circuits below.
  cairosvg:  { label: 'CairoSVG', downloadUrl: 'https://cairosvg.org/documentation/' },
}

function commandFor(tool: string, info: SystemDepsInfo): string {
  if (tool === 'claude') return 'npm install -g @anthropic-ai/claude-code'
  if (tool === 'codex') return 'npm install -g @openai/codex'
  if (tool === 'qwen') return 'npm install -g @qwen-code/qwen-code'
  if (tool === 'omp') return 'npm install -g oh-my-pi'
  if (tool === 'goose') return 'npm install -g @block/goose'
  // cairosvg installs via pip on every platform — independent of the OS PM.
  // Mirrors commandFor() in internal/service/system_deps.go (--user, no admin).
  if (tool === 'cairosvg') return 'python -m pip install --user cairosvg'
  switch (info.package_manager) {
    case 'winget': {
      const ids: Record<string, string> = {
        node: 'OpenJS.NodeJS.LTS',
        python3: 'Python.Python.3.13',
        git: 'Git.Git',
        tesseract: 'UB-Mannheim.TesseractOCR',
      }
      return `winget install -e --id ${ids[tool] ?? ''}`
    }
    case 'brew': {
      const pkgs: Record<string, string> = { node: 'node', python3: 'python@3.13', git: 'git', tesseract: 'tesseract' }
      return `brew install ${pkgs[tool] ?? ''}`
    }
    case 'apt-get': {
      const pkgs: Record<string, string> = { node: 'nodejs', python3: 'python3', git: 'git', tesseract: 'tesseract-ocr' }
      return `sudo apt-get install -y ${pkgs[tool] ?? ''}`
    }
    default:
      return ''
  }
}

export function SystemDepsSettings() {
  const { t } = useTranslation('settings')
  const probe = useQuery({
    queryKey: ['system-deps'],
    queryFn: () => api.getSystemDeps(),
    refetchOnWindowFocus: false,
  })
  const [installing, setInstalling] = useState<string | null>(null) // tool name
  const [loginPending, setLoginPending] = useState<boolean>(false)
  // Which CLI's browser login terminal is open (team edition path, #677).
  const [browserLoginTool, setBrowserLoginTool] = useState<'claude' | 'codex' | null>(null)
  // Team edition gates the browser login on admin, because the server has a
  // single shared $HOME: a member re-running login would swap the credentials
  // every other member's agents run under. Personal mode has no role claim, and
  // its lone local user is implicitly admin.
  const authRole = useAuthStore((s) => s.user?.role)
  const loginTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [logs, setLogs] = useState<string[]>([])
  const [logTitle, setLogTitle] = useState<string>('')
  const esRef = useRef<EventSource | null>(null)
  const finishedRef = useRef<boolean>(false)
  const logBoxRef = useRef<HTMLDivElement | null>(null)

  // Auto-scroll log panel.
  useEffect(() => {
    if (logBoxRef.current) {
      logBoxRef.current.scrollTop = logBoxRef.current.scrollHeight
    }
  }, [logs])

  useEffect(() => {
    return () => { esRef.current?.close() }
  }, [])

  // Clear any pending debounce timer on unmount.
  useEffect(() => {
    return () => {
      if (loginTimerRef.current) {
        clearTimeout(loginTimerRef.current)
        loginTimerRef.current = null
      }
    }
  }, [])

  async function handleInstall(tool: string) {
    if (installing) {
      toast.warning(t('systemDeps.anotherInstalling'))
      return
    }
    try {
      const resp = await api.startSystemDepsInstall(tool)
      if (!resp.job_id && resp.fallback_url) {
        // In the desktop webview window.open is swallowed; route through the
        // backend shell so the OS default browser opens. See openExternalSmart.
        void openExternalSmart(resp.fallback_url, probe.data?.personal_mode ?? false)
        return
      }
      setInstalling(tool)
      setLogs([])
      setLogTitle(toolLabels[tool]?.label ?? tool)
      finishedRef.current = false
      const url = api.systemDepsInstallStreamUrl(resp.job_id)
      const es = new EventSource(url)
      esRef.current = es
      es.onmessage = (ev) => {
        try {
          const data = JSON.parse(ev.data) as { line?: string; done?: boolean; exit_code?: number }
          if (data.done) {
            finishedRef.current = true
            setLogs((prev) => [...prev, t('systemDeps.doneLine', { code: data.exit_code ?? 0 })])
            es.close()
            esRef.current = null
            setInstalling(null)
            void probe.refetch()
            return
          }
          if (typeof data.line === 'string') {
            setLogs((prev) => [...prev, data.line!])
          }
        } catch {
          /* ignore malformed event */
        }
      }
      es.onerror = () => {
        // Browsers fire onerror on the natural close after `done` — ignore it.
        if (finishedRef.current) return
        setLogs((prev) => [...prev, t('systemDeps.connectionLost')])
        es.close()
        esRef.current = null
        setInstalling(null)
      }
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e)
      if (msg.includes('409')) toast.warning(t('systemDeps.anotherInstalling'))
      else if (msg.includes('403')) toast.error(t('systemDeps.installNotSupported'))
      else toast.error(t('systemDeps.installFailed', { message: msg }))
    }
  }

  // Manual recheck: refetch and surface a toast so the click has visible
  // feedback even when the probe result is byte-identical to the cached one
  // (which is the common case unless the user just installed/uninstalled a
  // tool out-of-band). Without the toast, refetch updates dataUpdatedAt but
  // re-renders the same DOM, and the user thinks the button is broken.
  async function handleRefresh() {
    try {
      await probe.refetch()
      toast.success(t('systemDeps.recheckDone'))
    } catch {
      // refetch already surfaces query errors via probe.isError; nothing to do.
    }
  }

  // Shared login-terminal launch path: claude and codex differ only in which
  // backend endpoint we hit and which i18n strings surface on failure.
  // Centralized so the 3-second debounce, refetch-on-403, and toast wiring
  // stay identical between the two buttons.
  async function runLogin(
    open: () => Promise<void>,
    notSupportedKey: string,
    failedKey: string,
  ) {
    if (loginPending) return
    setLoginPending(true)
    if (loginTimerRef.current) {
      clearTimeout(loginTimerRef.current)
      loginTimerRef.current = null
    }
    loginTimerRef.current = setTimeout(() => {
      loginTimerRef.current = null
      setLoginPending(false)
    }, 3000)
    try {
      await open()
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e)
      if (msg.includes('403')) {
        toast.error(t(notSupportedKey))
        void probe.refetch()
      } else {
        toast.error(t(failedKey, { message: msg }))
      }
      if (loginTimerRef.current) {
        clearTimeout(loginTimerRef.current)
        loginTimerRef.current = null
      }
      setLoginPending(false)
    }
  }

  function handleClaudeLogin() {
    // Team edition / headless host: run the login inside a browser terminal,
    // since there is no desktop on the server to pop a native window on (#677).
    if (probe.data?.browser_cli_login) {
      setBrowserLoginTool('claude')
      return
    }
    void runLogin(
      openClaudeLoginTerminal,
      'systemDeps.tool.claudeLoginNotSupported',
      'systemDeps.tool.claudeLoginFailed',
    )
  }

  function handleCodexLogin() {
    if (probe.data?.browser_cli_login) {
      setBrowserLoginTool('codex')
      return
    }
    void runLogin(
      openCodexLoginTerminal,
      'systemDeps.tool.codexLoginNotSupported',
      'systemDeps.tool.codexLoginFailed',
    )
  }

  if (probe.isLoading) return <div className="p-6 text-muted-foreground">{t('systemDeps.detecting')}</div>
  if (probe.isError || !probe.data) {
    return <div className="p-6 text-destructive">{t('systemDeps.detectFailed', { message: (probe.error as Error)?.message ?? 'unknown' })}</div>
  }
  const info = probe.data

  // Required tools (node+git+claude) that aren't installed yet. Surfaced as a
  // banner so the user — who may have been auto-routed here by the landing
  // gate — sees which dependencies are blocking, distinct from the optional
  // python3 / codex rows. Driven by live probe data, so it's correct however
  // the user arrived.
  const missingRequired = MINIMUM_TOOLS.filter(
    (name) => !info.tools.find((tool) => tool.name === name)?.found,
  )
  return (
    <div className="p-6 max-w-3xl space-y-4">
      <div>
        <h2 className="text-lg font-semibold mb-1">{t('systemDeps.title')}</h2>
        <p className="text-sm text-muted-foreground">
          {t('systemDeps.description')}
        </p>
        {missingRequired.length > 0 && (
          <div
            role="alert"
            className="mt-3 rounded-md border border-amber-500/50 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400"
          >
            {t('systemDeps.requiredMissing', {
              tools: missingRequired.map((name) => toolLabels[name]?.label ?? name).join(', '),
            })}
          </div>
        )}
        {!info.can_install && (
          <p className="mt-2 text-sm text-amber-600 dark:text-amber-500">
            {info.personal_mode
              ? t('systemDeps.noPackageManager')
              : t('systemDeps.noInstallSupport')}
          </p>
        )}
      </div>

      <div className="space-y-3">
        {info.tools.map((tool) => (
          <ToolCard
            key={tool.name}
            tool={tool}
            info={info}
            installing={installing}
            loginPending={loginPending}
            isRefreshing={probe.isFetching && !probe.isLoading}
            onInstall={() => handleInstall(tool.name)}
            onRefresh={handleRefresh}
            onClaudeLogin={handleClaudeLogin}
            onCodexLogin={handleCodexLogin}
            canBrowserLogin={authRole ? authRole === 'admin' || authRole === 'owner' : true}
            nodeFound={info.tools.find((t) => t.name === 'node')?.found ?? false}
          />
        ))}
      </div>

      <CLILoginTerminalDialog
        tool={browserLoginTool}
        onClose={() => setBrowserLoginTool(null)}
        // Re-probe on dismiss so the 已登录/未登录 badge reflects the result
        // without the user having to hit 重新检测.
        onFinished={() => { void probe.refetch() }}
      />

      {logs.length > 0 && (
        <div className="rounded border bg-muted/40">
          <div className="px-3 py-2 border-b text-sm font-medium flex items-center gap-2">
            <span>{t('systemDeps.logsTitle', { title: logTitle })}</span>
            {installing && <span className="text-xs text-muted-foreground">{t('systemDeps.running')}</span>}
          </div>
          <div
            ref={logBoxRef}
            role="log"
            aria-live="polite"
            className="p-3 font-mono text-xs whitespace-pre-wrap max-h-80 overflow-auto"
          >
            {logs.map((l, i) => (
              <div key={i}>{l}</div>
            ))}
          </div>
        </div>
      )}

    </div>
  )
}

export function ToolCard(props: {
  tool: ToolStatus
  info: SystemDepsInfo
  installing: string | null
  loginPending: boolean
  isRefreshing?: boolean
  onInstall: () => void
  onRefresh: () => void
  onClaudeLogin: () => void
  onCodexLogin?: () => void
  /** Whether the current user may run the server-side browser login (admin).
   *  Ignored in personal mode, where the native-window launch is used. */
  canBrowserLogin?: boolean
  nodeFound: boolean
}) {
  const { t } = useTranslation('settings')
  const { tool, info, installing, loginPending, isRefreshing, onInstall, onRefresh, onClaudeLogin, onCodexLogin, canBrowserLogin = true, nodeFound } = props
  const meta = toolLabels[tool.name]
  const cmd = commandFor(tool.name, info)
  const isInstalling = installing === tool.name
  const claudeBlocked = tool.name === 'claude' && !nodeFound
  const codexBlocked = tool.name === 'codex' && !nodeFound
  // Login buttons appear on the matching CLI row once the CLI is detected.
  // Two transports: personal mode pops a native terminal on the user's own
  // desktop; every other deployment (notably the team-edition container, which
  // has no display) runs the CLI in a server-side PTY bridged to a browser
  // terminal. Before #677 this was gated on personal_mode alone, so team users
  // saw no login affordance at all and the endpoint 403'd.
  const isLoginTool = tool.name === 'claude' || tool.name === 'codex'
  const canLogin = tool.found && isLoginTool && (info.personal_mode || info.browser_cli_login)
  // In the browser-login transport the action is admin-only (shared server $HOME).
  const loginAllowed = info.browser_cli_login ? canBrowserLogin : true
  const showClaudeLogin = canLogin && tool.name === 'claude'
  const showCodexLogin = canLogin && tool.name === 'codex'
  // logged_in is only sent for CLIs that have a login flow; undefined elsewhere.
  const loginState = tool.found && isLoginTool ? tool.logged_in : undefined
  const loginLabel = loginState
    ? t('systemDeps.tool.relogin')
    : info.browser_cli_login
      ? t('systemDeps.tool.loginInBrowser')
      : tool.name === 'claude'
        ? t('systemDeps.tool.claudeLogin')
        : t('systemDeps.tool.codexLogin')

  return (
    <div className="rounded border p-3">
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-2 min-w-0">
          <span
            aria-hidden
            className={`inline-block h-2 w-2 rounded-full shrink-0 ${
              tool.found ? 'bg-green-500' : 'bg-red-500'
            }`}
          />
          <span className="font-medium">{meta?.label ?? tool.name}</span>
          <span className="text-sm text-muted-foreground truncate">
            {tool.found ? tool.version || t('systemDeps.tool.installed') : t('systemDeps.tool.notInstalled')}
          </span>
          {/* Login state for the agent CLIs. Without this the team-edition card
              gave no hint that the CLI was unauthenticated — agents just failed
              at run time (#677). */}
          {loginState !== undefined && (
            <span
              className={`text-xs px-1.5 py-0.5 rounded shrink-0 ${
                loginState
                  ? 'bg-green-500/10 text-green-700 dark:text-green-400'
                  : 'bg-amber-500/10 text-amber-700 dark:text-amber-400'
              }`}
            >
              {loginState ? t('systemDeps.tool.loggedIn') : t('systemDeps.tool.notLoggedIn')}
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {tool.found ? (
            <>
              {showClaudeLogin && (
                <Button
                  size="sm"
                  variant={loginState ? 'outline' : 'default'}
                  disabled={loginPending || !loginAllowed}
                  title={!loginAllowed ? t('systemDeps.tool.loginAdminOnly') : undefined}
                  onClick={onClaudeLogin}
                >
                  {loginLabel}
                </Button>
              )}
              {showCodexLogin && onCodexLogin && (
                <Button
                  size="sm"
                  variant={loginState ? 'outline' : 'default'}
                  disabled={loginPending || !loginAllowed}
                  title={!loginAllowed ? t('systemDeps.tool.loginAdminOnly') : undefined}
                  onClick={onCodexLogin}
                >
                  {loginLabel}
                </Button>
              )}
              <Button size="sm" variant="ghost" disabled={isRefreshing} onClick={onRefresh}>
                {isRefreshing ? t('systemDeps.detecting') : t('systemDeps.tool.recheck')}
              </Button>
            </>
          ) : (
            <>
              {tool.installable && (
                <Button
                  size="sm"
                  disabled={!!installing || claudeBlocked || codexBlocked}
                  title={(claudeBlocked || codexBlocked) ? t('systemDeps.tool.needNodeFirst') : undefined}
                  onClick={onInstall}
                >
                  {isInstalling ? t('systemDeps.tool.installing') : t('systemDeps.tool.oneClickInstall')}
                </Button>
              )}
              {/* Surface the recheck affordance in the not-installed branch too,
                  so users who installed the tool out-of-band (manual download,
                  another package manager, PATH fix) can re-trigger detection
                  without leaving the page. Disabled while a one-click install
                  is in flight on this row to avoid stomping the streamed log. */}
              <Button size="sm" variant="ghost" disabled={isInstalling || isRefreshing} onClick={onRefresh}>
                {isRefreshing ? t('systemDeps.detecting') : t('systemDeps.tool.recheck')}
              </Button>
              {meta && (
                <a
                  href={meta.downloadUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  onClick={(e) => {
                    // The desktop webview swallows target="_blank", so the link
                    // looks dead. In personal mode hand the URL to the backend
                    // shell to open the OS default browser; in hosted/browser
                    // mode let the normal new-tab navigation proceed.
                    if (info.personal_mode) {
                      e.preventDefault()
                      void openExternalSmart(meta.downloadUrl, true)
                    }
                  }}
                  className="text-sm text-blue-600 hover:underline"
                >
                  {t('systemDeps.tool.manualDownload')}
                </a>
              )}
            </>
          )}
        </div>
      </div>
      {tool.found && tool.path && (
        <div className="mt-1 ml-4 text-xs text-muted-foreground font-mono truncate">{tool.path}</div>
      )}
      {!tool.found && tool.installable && cmd && (
        <div className="mt-1 ml-4 text-xs text-muted-foreground font-mono">{t('systemDeps.tool.willRun', { cmd })}</div>
      )}
      {tool.name === 'git' && tool.found && (
        <GitIdentityPanel
          initial={tool.extras?.git_identity}
          onSaved={() => props.onRefresh()}
        />
      )}
    </div>
  )
}
