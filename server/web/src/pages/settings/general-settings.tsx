import { useState, useEffect, useCallback, useRef } from 'react'
import { Bell, BellOff, Volume2, VolumeX, Play, Monitor, Globe, Languages, Power, Minimize2, BarChart3, Sparkles, Keyboard } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { cn } from '@/lib/utils'
import { api } from '@/lib/api'
import { invalidateEditorConfigCache } from '@/lib/vscode'
import { openExternalSmart } from '@/lib/shell'
import { TELEMETRY_PRIVACY_URL } from '@/lib/links'
import { playNotificationSound } from '@/lib/notification-sound'
import {
  useNotificationSettings,
  SOUND_OPTIONS,
  type SoundName,
} from '@/stores/notification-settings-store'
import { useConfigStore } from '@/stores/config-store'
import { useAuthStore } from '@/stores/auth-store'
import { useLanguageStore } from '@/stores/language-store'
import { SUPPORTED_LANGUAGES, LANGUAGE_LABELS, type Language } from '@/i18n'
import { isDesktopShell } from '@/lib/desktop-runner-context'
import {
  DESKTOP_HOTKEY_EVENT,
  queryDesktopHotkey,
  setDesktopHotkey,
  defaultAccelerator,
  readBootstrapHotkey,
  writeBootstrapHotkey,
  type DesktopHotkeyConfig,
  type DesktopHotkeyTarget,
} from '@/lib/desktop-hotkey'
import { KeybindingInput } from '@/components/ui/keybinding-input'

/** Reactive state for one configurable desktop global hotkey (window | ai). */
interface DesktopHotkeyState {
  enabled: boolean
  accel: string
  label: string
  setEnabled: (v: boolean) => void
  setAccel: (v: string) => void
}

/**
 * Drives one desktop global hotkey. The configured combo is read SYNCHRONOUSLY
 * from the desktop-injected `window.__NIUNIU_HOTKEYS__` global (falling back to the
 * platform default), so the field always shows a value — fixing the "默认值/已设置
 * 的快捷键不回显" bug. The old async query→ExecJS echo is unreliable for the
 * URL-loaded local SPA (dozens of queries, zero reply), so it is only used as a
 * best-effort refinement for the "实际生效" fallback label. On change we update the
 * global too, so a same-session reopen still echoes the new combo.
 */
function useDesktopHotkey(
  target: DesktopHotkeyTarget,
  isDesktop: boolean,
  onSaveError: (error: string) => void,
): DesktopHotkeyState {
  const boot = readBootstrapHotkey(target)
  const [enabled, setEnabledState] = useState(boot ? boot.enabled : true)
  const [accel, setAccelState] = useState(boot ? boot.accelerator : defaultAccelerator(target))
  const [label, setLabel] = useState('')
  // True once we've applied the real (injected) config, so later polls/echoes
  // don't fight an in-session user edit.
  const settledRef = useRef(!!boot)

  useEffect(() => {
    if (!isDesktop) return
    const onConfig = (e: Event) => {
      const d = (e as CustomEvent<DesktopHotkeyConfig>).detail
      if (!d || (d.target ?? 'ai') !== target) return
      settledRef.current = true
      setEnabledState(d.enabled)
      if (d.accelerator) setAccelState(d.accelerator)
      setLabel(d.label)
      if (!d.ok) onSaveError(d.error || '')
    }
    window.addEventListener(DESKTOP_HOTKEY_EVENT, onConfig)
    queryDesktopHotkey(target)
    // The desktop shell injects window.__NIUNIU_HOTKEYS__ on navigation-completed,
    // which can land just AFTER this mount. Poll briefly until it appears so a
    // restart shows the persisted combo instead of the platform default. Stop once
    // settled (read once, or a user edit came in) to avoid clobbering input.
    let tries = 0
    const timer = window.setInterval(() => {
      tries += 1
      if (settledRef.current || tries >= 20) {
        window.clearInterval(timer)
        return
      }
      const b = readBootstrapHotkey(target)
      if (b) {
        settledRef.current = true
        setEnabledState(b.enabled)
        if (b.accelerator) setAccelState(b.accelerator)
        window.clearInterval(timer)
      }
    }, 200)
    return () => {
      window.removeEventListener(DESKTOP_HOTKEY_EVENT, onConfig)
      window.clearInterval(timer)
    }
  }, [isDesktop, target, onSaveError])

  const setEnabled = useCallback(
    (v: boolean) => {
      settledRef.current = true
      setEnabledState(v)
      writeBootstrapHotkey(target, v, accel)
      setDesktopHotkey(v, accel, target)
    },
    [accel, target]
  )
  const setAccel = useCallback(
    (v: string) => {
      settledRef.current = true
      setAccelState(v)
      writeBootstrapHotkey(target, enabled, v)
      setDesktopHotkey(enabled, v, target)
    },
    [enabled, target]
  )

  return { enabled, accel, label, setEnabled, setAccel }
}

/**
 * One global-hotkey settings entry: an enable toggle plus (when enabled) a
 * KeybindingInput that echoes the configured combo and captures a new one, and a
 * "实际生效" hint when the OS bound a different fallback combo.
 */
function HotkeyRow({
  titleKey,
  descKey,
  state,
}: {
  titleKey: string
  descKey: string
  state: DesktopHotkeyState
}) {
  const { t } = useTranslation('settings')
  return (
    <div className="flex items-center justify-between gap-4 py-2.5 px-4 rounded-lg border border-border bg-card">
      <div className="flex items-center gap-3 min-w-0">
        <Keyboard className={cn('h-4 w-4 shrink-0', state.enabled ? 'text-info' : 'text-muted-foreground')} />
        <div className="min-w-0">
          <p className="text-sm font-medium text-foreground">{t(titleKey)}</p>
          <p className="text-xs text-muted-foreground">{t(descKey)}</p>
          {state.enabled && state.label && state.label !== state.accel && (
            <p className="text-xs text-muted-foreground mt-0.5">
              {t('general.globalShortcuts.boundAs', { combo: state.label })}
            </p>
          )}
        </div>
      </div>
      <div className="flex items-center gap-3 shrink-0">
        {state.enabled && <KeybindingInput value={state.accel} onChange={state.setAccel} />}
        <Toggle checked={state.enabled} onChange={state.setEnabled} />
      </div>
    </div>
  )
}

function Toggle({
  checked,
  onChange,
  disabled = false,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        'relative inline-flex h-5 w-9 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors',
        checked ? 'bg-info' : 'bg-muted',
        disabled && 'cursor-not-allowed opacity-50'
      )}
    >
      <span
        className={cn(
          'pointer-events-none inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
          checked ? 'translate-x-4' : 'translate-x-0'
        )}
      />
    </button>
  )
}

interface EditorConfig {
  vscode_mode: string
  vscode_remote_url: string
}

interface AppConfig {
  editor: EditorConfig
  /** Anonymous usage-telemetry opt-out flag (default true). Personal edition
   *  only; team/hosted instances never run the reporter. See #366 / #367. */
  telemetry_enabled?: boolean
}

interface AutostartStatus {
  supported: boolean
  enabled: boolean
  minimized: boolean
}

export function GeneralSettings() {
  const {
    toastEnabled,
    soundEnabled,
    soundName,
    volume,
    setToastEnabled,
    setSoundEnabled,
    setSoundName,
    setVolume,
    alertScope,
    setAlertScope,
  } = useNotificationSettings()

  const { t } = useTranslation('settings')
  const language = useLanguageStore((s) => s.language)
  const setLanguage = useLanguageStore((s) => s.setLanguage)
  // Telemetry only runs on personal/self-hosted (Auth.Enabled=false) instances;
  // team/hosted never reports, so we hide the toggle there to avoid implying a
  // collection that isn't happening (mirrors the ConsentGate section gate).
  // Gate on configLoaded too: personalMode defaults to true until /api/health
  // resolves, so without it team edition would briefly flash the section.
  const personalMode = useConfigStore((s) => s.personalMode)
  const configLoaded = useConfigStore((s) => s.loaded)
  // 牛牛助手 capability is a team-edition, admin-only instance toggle. Personal
  // edition always shows the entry, so the section is hidden there.
  const isAdmin = useAuthStore((s) => s.user)?.role === 'admin'
  const showAssistantToggle = configLoaded && !personalMode && isAdmin
  const [switching, setSwitching] = useState(false)

  const queryClient = useQueryClient()
  const { data: appConfig } = useQuery({
    queryKey: ['app-config'],
    queryFn: () => api.get<AppConfig>('/config'),
  })

  // Launch-at-login (personal/desktop edition only). The server reports
  // supported=false in team/hosted mode, so the section self-hides.
  const { data: autostart } = useQuery({
    queryKey: ['autostart'],
    queryFn: () => api.get<AutostartStatus>('/autostart'),
  })
  const autostartMutation = useMutation({
    mutationFn: (vars: { enabled: boolean; minimized: boolean }) =>
      api.put<AutostartStatus>('/autostart', vars),
    onSuccess: (data) => {
      queryClient.setQueryData(['autostart'], data)
    },
    onError: () => {
      // Write may fail (e.g. registry/plist permission); resync the toggle to
      // the server's actual state rather than leaving it on the clicked value.
      queryClient.invalidateQueries({ queryKey: ['autostart'] })
    },
  })

  // The two configurable global hotkeys (desktop edition only): the local main
  // window and the AI-aggregation window. State is driven by the desktop shell
  // over the raw bridge; the section self-hides in a plain browser.
  const isDesktop = isDesktopShell()
  const onHotkeySaveError = useCallback(
    (error: string) => toast.error(t('general.globalShortcuts.saveFailed', { error })),
    [t]
  )
  const windowHotkey = useDesktopHotkey('window', isDesktop, onHotkeySaveError)
  const aiHotkey = useDesktopHotkey('ai', isDesktop, onHotkeySaveError)

  const [vscodeMode, setVscodeMode] = useState<string>('local')
  const [remoteUrl, setRemoteUrl] = useState('')

  // Compare-during-render: sync editor config from API response when it first arrives
  const [prevAppConfig, setPrevAppConfig] = useState(appConfig)
  if (appConfig !== prevAppConfig) {
    setPrevAppConfig(appConfig)
    if (appConfig?.editor) {
      setVscodeMode(appConfig.editor.vscode_mode || 'local')
      setRemoteUrl(appConfig.editor.vscode_remote_url || '')
    }
  }

  const saveEditorMutation = useMutation({
    mutationFn: (editor: { vscode_mode: string; vscode_remote_url: string }) =>
      api.put('/config', { editor }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['app-config'] })
      invalidateEditorConfigCache()
    },
  })

  const handleSaveEditor = () => {
    saveEditorMutation.mutate({ vscode_mode: vscodeMode, vscode_remote_url: remoteUrl })
  }

  // Anonymous-telemetry opt-out. Defaults to true (opt-out) until the config
  // loads or on an older server that doesn't report the flag. The PUT echoes
  // the full snapshot, so we seed the cache from the response — the reporter
  // re-reads the flag on its next tick, no restart needed.
  const telemetryEnabled = appConfig?.telemetry_enabled ?? true
  const saveTelemetryMutation = useMutation({
    mutationFn: (enabled: boolean) =>
      api.put<AppConfig>('/config', { telemetry_enabled: enabled }),
    onSuccess: (data) => {
      queryClient.setQueryData(['app-config'], data)
      toast.success(t('general.telemetry.saved'))
    },
    onError: (err) => {
      // Resync to the server's actual state rather than leaving the switch on
      // the clicked value, then surface why.
      queryClient.invalidateQueries({ queryKey: ['app-config'] })
      toast.error(t('general.telemetry.saveFailed', { error: (err as Error).message }))
    },
  })

  // 牛牛助手 nav-entry capability (team edition). Stored as the admin-settable
  // "features.assistant_enabled" server setting ("1"/"0"); the nav reads the
  // effective value from /config. Only queried for admins in team edition.
  const ASSISTANT_KEY = 'features.assistant_enabled'
  const { data: assistantSetting } = useQuery({
    queryKey: ['admin-setting', ASSISTANT_KEY],
    queryFn: () => api.get<{ key: string; value: string }>(`/admin/settings/${ASSISTANT_KEY}`),
    enabled: showAssistantToggle,
  })
  const assistantEnabled = assistantSetting?.value === '1'
  const saveAssistantMutation = useMutation({
    mutationFn: (enabled: boolean) =>
      api.put(`/admin/settings/${ASSISTANT_KEY}`, { value: enabled ? '1' : '0' }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['admin-setting', ASSISTANT_KEY] })
      // The global nav reads assistant_enabled from /config — refresh it so the
      // entry appears/disappears without a reload.
      queryClient.invalidateQueries({ queryKey: ['app-config'] })
      toast.success(t('general.assistant.saved'))
    },
    onError: (err) => {
      queryClient.invalidateQueries({ queryKey: ['admin-setting', ASSISTANT_KEY] })
      toast.error(t('general.assistant.saveFailed', { error: (err as Error).message }))
    },
  })

  const handleTestSound = () => {
    playNotificationSound(soundName, volume)
  }

  return (
    <div className="py-6 space-y-8">
      {/* Interface language */}
      <div className="space-y-4">
        <div>
          <h2 className="text-lg font-medium text-foreground">{t('general.language.title')}</h2>
          <p className="text-sm text-muted-foreground mt-1">{t('general.language.description')}</p>
        </div>
        <div className="flex items-center gap-3">
          <Languages className="h-4 w-4 text-muted-foreground" />
          <select
            value={language}
            onChange={async (e) => {
              setSwitching(true)
              await setLanguage(e.target.value as Language)
              setSwitching(false)
            }}
            disabled={switching}
            className="h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:border-transparent disabled:opacity-50"
          >
            {SUPPORTED_LANGUAGES.map((lang) => (
              <option key={lang} value={lang}>{LANGUAGE_LABELS[lang]}</option>
            ))}
          </select>
        </div>
      </div>

      {/* Privacy — anonymous usage telemetry (personal/self-hosted only).
          This is the primary opt-out entry point for existing users. */}
      {configLoaded && personalMode && (
        <div className="space-y-4">
          <div>
            <h2 className="text-lg font-medium text-foreground">{t('general.privacy.title')}</h2>
            <p className="text-sm text-muted-foreground mt-1">{t('general.telemetry.description')}</p>
          </div>
          <div className="flex items-center justify-between py-2 px-4 rounded-lg border border-border bg-card">
            <div className="flex items-center gap-3">
              <BarChart3
                className={cn('h-4 w-4', telemetryEnabled ? 'text-info' : 'text-muted-foreground')}
              />
              <div>
                <p className="text-sm font-medium text-foreground">{t('general.telemetry.toggleLabel')}</p>
                <Button
                  variant="link"
                  className="h-auto p-0 text-xs font-normal text-muted-foreground"
                  onClick={() => {
                    void openExternalSmart(TELEMETRY_PRIVACY_URL, personalMode)
                  }}
                >
                  {t('general.telemetry.learnMore')}
                </Button>
              </div>
            </div>
            <Switch
              checked={telemetryEnabled}
              onCheckedChange={(v) => saveTelemetryMutation.mutate(v)}
              disabled={saveTelemetryMutation.isPending}
              aria-label={t('general.telemetry.toggleLabel')}
            />
          </div>
        </div>
      )}

      {/* 牛牛助手 capability (team edition, admin only). Hidden by default;
          enabling it surfaces the assistant entry in the top nav for everyone. */}
      {showAssistantToggle && (
        <div className="space-y-4">
          <div>
            <h2 className="text-lg font-medium text-foreground">{t('general.assistant.title')}</h2>
            <p className="text-sm text-muted-foreground mt-1">{t('general.assistant.description')}</p>
          </div>
          <div className="flex items-center justify-between py-2 px-4 rounded-lg border border-border bg-card">
            <div className="flex items-center gap-3">
              <Sparkles className={cn('h-4 w-4', assistantEnabled ? 'text-info' : 'text-muted-foreground')} />
              <p className="text-sm font-medium text-foreground">{t('general.assistant.toggleLabel')}</p>
            </div>
            <Switch
              checked={assistantEnabled}
              onCheckedChange={(v) => saveAssistantMutation.mutate(v)}
              disabled={saveAssistantMutation.isPending}
              aria-label={t('general.assistant.toggleLabel')}
            />
          </div>
        </div>
      )}

      {/* Launch at login (personal/desktop edition only) */}
      {autostart?.supported && (
        <div className="space-y-4">
          <div>
            <h2 className="text-lg font-medium text-foreground">{t('general.autostart.title')}</h2>
            <p className="text-sm text-muted-foreground mt-1">{t('general.autostart.description')}</p>
          </div>
          {/* Enable/disable the OS login item. Default launch shows the window. */}
          <div className="flex items-center justify-between py-2 px-4 rounded-lg border border-border bg-card">
            <div className="flex items-center gap-3">
              <Power className={cn('h-4 w-4', autostart.enabled ? 'text-info' : 'text-muted-foreground')} />
              <p className="text-sm font-medium text-foreground">{t('general.autostart.title')}</p>
            </div>
            <Toggle
              checked={autostart.enabled}
              onChange={(v) => autostartMutation.mutate({ enabled: v, minimized: autostart.minimized })}
              disabled={autostartMutation.isPending}
            />
          </div>
          {/* Optional: start minimized to the tray (only when autostart is on). */}
          {autostart.enabled && (
            <div className="flex items-center justify-between py-2 px-4 rounded-lg border border-border bg-card">
              <div className="flex items-center gap-3">
                <Minimize2 className={cn('h-4 w-4', autostart.minimized ? 'text-info' : 'text-muted-foreground')} />
                <div>
                  <p className="text-sm font-medium text-foreground">{t('general.autostart.minimized.title')}</p>
                  <p className="text-xs text-muted-foreground">{t('general.autostart.minimized.description')}</p>
                </div>
              </div>
              <Toggle
                checked={autostart.minimized}
                onChange={(v) => autostartMutation.mutate({ enabled: true, minimized: v })}
                disabled={autostartMutation.isPending}
              />
            </div>
          )}
        </div>
      )}

      {/* Global shortcuts for the local main window + AI window (desktop only) */}
      {isDesktop && (
        <div className="space-y-4">
          <div>
            <h2 className="text-lg font-medium text-foreground">{t('general.globalShortcuts.title')}</h2>
            <p className="text-sm text-muted-foreground mt-1">{t('general.globalShortcuts.description')}</p>
          </div>
          <HotkeyRow
            titleKey="general.globalShortcuts.toggleWindow.title"
            descKey="general.globalShortcuts.toggleWindow.description"
            state={windowHotkey}
          />
          <HotkeyRow
            titleKey="general.globalShortcuts.toggleAI.title"
            descKey="general.globalShortcuts.toggleAI.description"
            state={aiHotkey}
          />
        </div>
      )}

      {/* Section header */}
      <div>
        <h2 className="text-lg font-medium text-foreground">{t('general.notifications.title')}</h2>
        <p className="text-sm text-muted-foreground mt-1">
          {t('general.notifications.description')}
        </p>
      </div>

      {/* Alert scope */}
      <div className="space-y-3">
        <h3 className="text-sm font-medium text-foreground">
          {t('general.notifications.alertScope.title')}
        </h3>
        <div className="space-y-2">
          {(['mine', 'all', 'none'] as const).map((scope) => (
            <label
              key={scope}
              className="flex items-start gap-3 py-2 px-4 rounded-lg border border-border bg-card cursor-pointer hover:bg-accent/50"
            >
              <input
                type="radio"
                name="alertScope"
                value={scope}
                checked={alertScope === scope}
                onChange={() => setAlertScope(scope)}
                className="mt-0.5"
              />
              <div>
                <p className="text-sm font-medium text-foreground">
                  {t(`general.notifications.alertScope.${scope}.title`)}
                </p>
                <p className="text-xs text-muted-foreground">
                  {t(`general.notifications.alertScope.${scope}.description`)}
                </p>
              </div>
            </label>
          ))}
        </div>
        <p className="text-xs text-muted-foreground/70 px-1">
          {t('general.notifications.alertScope.permissionNote')}
        </p>
      </div>

      {/* Notification method toggles */}
      <div className="space-y-4">
        <h3 className="text-sm font-medium text-foreground">{t('general.notifications.method')}</h3>

        <div className="space-y-3">
          {/* Toast notification toggle */}
          <div className="flex items-center justify-between py-2 px-4 rounded-lg border border-border bg-card">
            <div className="flex items-center gap-3">
              {toastEnabled ? (
                <Bell className="h-4 w-4 text-info" />
              ) : (
                <BellOff className="h-4 w-4 text-muted-foreground" />
              )}
              <div>
                <p className="text-sm font-medium text-foreground">{t('general.notifications.toast.title')}</p>
                <p className="text-xs text-muted-foreground">
                  {t('general.notifications.toast.description')}
                </p>
              </div>
            </div>
            <Toggle checked={toastEnabled} onChange={setToastEnabled} />
          </div>

          {/* Sound notification toggle */}
          <div className="flex items-center justify-between py-2 px-4 rounded-lg border border-border bg-card">
            <div className="flex items-center gap-3">
              {soundEnabled ? (
                <Volume2 className="h-4 w-4 text-info" />
              ) : (
                <VolumeX className="h-4 w-4 text-muted-foreground" />
              )}
              <div>
                <p className="text-sm font-medium text-foreground">{t('general.notifications.sound.title')}</p>
                <p className="text-xs text-muted-foreground">
                  {t('general.notifications.sound.description')}
                </p>
              </div>
            </div>
            <Toggle checked={soundEnabled} onChange={setSoundEnabled} />
          </div>
        </div>
      </div>

      {/* Sound configuration */}
      <div
        className={cn(
          'space-y-4 transition-opacity',
          !soundEnabled && 'opacity-50 pointer-events-none'
        )}
      >
        <h3 className="text-sm font-medium text-foreground">{t('general.soundConfig.title')}</h3>

        {/* Sound selection */}
        <div className="space-y-2">
          <label className="text-sm text-muted-foreground">{t('general.soundConfig.selectLabel')}</label>
          <div className="flex items-center gap-2">
            <select
              value={soundName}
              onChange={(e) => setSoundName(e.target.value as SoundName)}
              className="flex-1 h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:border-transparent"
            >
              {SOUND_OPTIONS.map((opt) => (
                <option key={opt.value} value={opt.value}>
                  {t(`common:soundOptions.${opt.value}`)}
                </option>
              ))}
            </select>
            <Button
              size="sm"
              variant="outline"
              onClick={handleTestSound}
              className="gap-1.5"
            >
              <Play className="h-3.5 w-3.5" />
              {t('general.soundConfig.test')}
            </Button>
          </div>
        </div>

        {/* Volume slider */}
        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <label className="text-sm text-muted-foreground">{t('general.soundConfig.volume')}</label>
            <span className="text-sm text-muted-foreground tabular-nums">
              {volume}%
            </span>
          </div>
          <input
            type="range"
            min={0}
            max={100}
            step={1}
            value={volume}
            onChange={(e) => setVolume(Number(e.target.value))}
            className="w-full h-1.5 bg-muted rounded-full appearance-none cursor-pointer accent-info [&::-webkit-slider-thumb]:appearance-none [&::-webkit-slider-thumb]:h-4 [&::-webkit-slider-thumb]:w-4 [&::-webkit-slider-thumb]:rounded-full [&::-webkit-slider-thumb]:bg-info [&::-webkit-slider-thumb]:cursor-pointer"
          />
          <div className="flex justify-between text-xs text-muted-foreground/70">
            <span>{t('general.soundConfig.muted')}</span>
            <span>{t('general.soundConfig.max')}</span>
          </div>
        </div>
      </div>

      {/* VS Code settings */}
      <div className="space-y-4">
        <div>
          <h2 className="text-lg font-medium text-foreground">{t('general.vscode.title')}</h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t('general.vscode.description')}
          </p>
        </div>

        <div className="space-y-3">
          {/* Mode selection */}
          <div
            onClick={() => setVscodeMode('local')}
            className={cn(
              'flex items-center justify-between py-2 px-4 rounded-lg border cursor-pointer transition-colors',
              vscodeMode === 'local'
                ? 'border-info/40 bg-info/5'
                : 'border-border bg-card hover:border-border/80'
            )}
          >
            <div className="flex items-center gap-3">
              <Monitor className={cn('h-4 w-4', vscodeMode === 'local' ? 'text-info' : 'text-muted-foreground')} />
              <div>
                <p className="text-sm font-medium text-foreground">{t('general.vscode.local.title')}</p>
                <p className="text-xs text-muted-foreground">
                  {t('general.vscode.local.description')}
                </p>
              </div>
            </div>
            <input
              type="radio"
              checked={vscodeMode === 'local'}
              onChange={() => setVscodeMode('local')}
              className="accent-blue-500"
            />
          </div>

          <div
            onClick={() => setVscodeMode('remote')}
            className={cn(
              'flex items-center justify-between py-2 px-4 rounded-lg border cursor-pointer transition-colors',
              vscodeMode === 'remote'
                ? 'border-info/40 bg-info/5'
                : 'border-border bg-card hover:border-border/80'
            )}
          >
            <div className="flex items-center gap-3">
              <Globe className={cn('h-4 w-4', vscodeMode === 'remote' ? 'text-info' : 'text-muted-foreground')} />
              <div>
                <p className="text-sm font-medium text-foreground">{t('general.vscode.remote.title')}</p>
                <p className="text-xs text-muted-foreground">
                  {t('general.vscode.remote.description')}
                </p>
              </div>
            </div>
            <input
              type="radio"
              checked={vscodeMode === 'remote'}
              onChange={() => setVscodeMode('remote')}
              className="accent-blue-500"
            />
          </div>
        </div>

        {/* Remote URL input */}
        <div className={cn(
          'space-y-2 transition-opacity',
          vscodeMode !== 'remote' && 'opacity-50 pointer-events-none'
        )}>
          <label className="text-sm text-muted-foreground">{t('general.vscode.remoteUrlLabel')}</label>
          <input
            type="text"
            value={remoteUrl}
            onChange={(e) => setRemoteUrl(e.target.value)}
            placeholder="http://localhost:8080"
            className="w-full h-9 rounded-md border border-border bg-background px-3 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:border-transparent"
          />
          <p className="text-xs text-muted-foreground/70">
            {t('general.vscode.remoteUrlHint')}
          </p>
        </div>

        <Button
          size="sm"
          onClick={handleSaveEditor}
          disabled={saveEditorMutation.isPending}
        >
          {saveEditorMutation.isPending ? t('common:actions.saving') : t('common:actions.save')}
        </Button>
      </div>
    </div>
  )
}
