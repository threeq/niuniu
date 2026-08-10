import { useState, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Shield, ShieldCheck, Check, Settings } from 'lucide-react';
import { api } from '@/lib/api';
import { AutohostSettingsDialog } from '@/components/dialogs/autohost-settings-dialog';

const PERMISSION_MODES = [
  { value: 'autohost', i18nKey: 'autohost' },
  { value: 'default', i18nKey: 'default' },
  { value: 'acceptEdits', i18nKey: 'acceptEdits' },
  { value: 'auto', i18nKey: 'auto' },
  { value: 'plan', i18nKey: 'plan' },
  { value: 'bypassPermissions', i18nKey: 'bypassPermissions' },
] as const;

// Brief feedback so a click on a mode row visibly registers, since the
// underlying state (workspace_env table + CLI flag at next spawn) is invisible.
const SAVED_FEEDBACK_MS = 1500;

interface PermissionSelectorProps {
  workspaceId: string;
}

export function PermissionSelector({ workspaceId }: PermissionSelectorProps) {
  const { t } = useTranslation('workspaces');
  const [mode, setMode] = useState('default');
  const [open, setOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [showSaved, setShowSaved] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const savedTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    api
      .get<{ env: Record<string, string> }>(`/workspaces/${workspaceId}/env`)
      .then((res) => {
        const m = res.env['NIUNIU_PERMISSION_MODE'];
        if (m) setMode(m);
      })
      .catch(() => {});
  }, [workspaceId]);

  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, []);

  useEffect(() => {
    return () => {
      if (savedTimerRef.current) clearTimeout(savedTimerRef.current);
    };
  }, []);

  const handleSelect = async (value: string) => {
    setMode(value);
    setOpen(false);
    const existing = await api
      .get<{ env: Record<string, string> }>(`/workspaces/${workspaceId}/env`)
      .then((r) => r.env)
      .catch(() => ({}) as Record<string, string>);
    existing['NIUNIU_PERMISSION_MODE'] = value;
    await api.put(`/workspaces/${workspaceId}/env`, { env: existing }).catch(() => {});

    setShowSaved(true);
    if (savedTimerRef.current) clearTimeout(savedTimerRef.current);
    savedTimerRef.current = setTimeout(() => setShowSaved(false), SAVED_FEEDBACK_MS);
  };

  const current = PERMISSION_MODES.find((m) => m.value === mode) ?? PERMISSION_MODES[0];
  const isAutohost = mode === 'autohost';

  // Toolbar button visually diverges by mode: autohost looks "on" (brand
  // background + filled icon + status dot) so the user sees at a glance the
  // agent will run autonomously. Other modes stay neutral.
  const buttonClass = isAutohost
    ? 'flex items-center gap-1.5 rounded bg-brand-soft px-2 py-1 text-xs font-medium text-brand hover:bg-brand-soft/80 transition-colors'
    : 'flex items-center gap-1 rounded px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground transition-colors';

  return (
    <>
      <div ref={ref} className="relative">
        <button
          onClick={() => setOpen(!open)}
          className={buttonClass}
          title={isAutohost ? t('panels.permissions.autohost.activeTitle') : t('panels.permissions.title')}
        >
          {isAutohost ? <ShieldCheck className="h-3.5 w-3.5" /> : <Shield className="h-3.5 w-3.5" />}
          <span>{t(`panels.permissions.modes.${current.i18nKey}.label`)}</span>
          {isAutohost && (
            <span className="ml-0.5 inline-block h-1.5 w-1.5 rounded-full bg-brand" aria-hidden />
          )}
        </button>

        {open && (
          <div className="absolute bottom-full left-0 mb-1 w-64 rounded-md border border-border bg-card shadow-lg z-50">
            <div className="px-3 py-2 border-b border-border flex items-center justify-between gap-2">
              <div className="min-w-0">
                <p className="text-xs font-medium text-foreground">{t('panels.permissions.title')}</p>
                <p className="text-[10px] text-muted-foreground">{t('panels.permissions.description')}</p>
              </div>
              {showSaved && (
                <span className="flex items-center gap-1 text-[10px] text-success">
                  <Check className="h-3 w-3" />
                  {t('panels.permissions.saved')}
                </span>
              )}
            </div>
            <div className="py-1">
              {PERMISSION_MODES.map((item) => {
                const selected = mode === item.value;
                return (
                  <div
                    key={item.value}
                    className={`group flex items-stretch ${selected ? 'bg-info/10' : ''}`}
                  >
                    <button
                      onClick={() => handleSelect(item.value)}
                      className={`flex-1 min-w-0 text-left px-3 py-2 text-xs hover:bg-accent transition-colors ${
                        selected ? 'text-info' : 'text-foreground'
                      }`}
                    >
                      <div className="font-medium truncate">
                        {t(`panels.permissions.modes.${item.i18nKey}.label`)}
                      </div>
                      <div className="text-[10px] text-muted-foreground mt-0.5 truncate">
                        {t(`panels.permissions.modes.${item.i18nKey}.desc`)}
                      </div>
                    </button>
                    {item.value === 'autohost' && selected && (
                      <button
                        type="button"
                        onClick={(e) => {
                          e.stopPropagation();
                          setOpen(false);
                          setSettingsOpen(true);
                        }}
                        className="flex items-center justify-center px-2 hover:bg-accent transition-colors text-muted-foreground hover:text-foreground"
                        title={t('panels.permissions.autohost.openSettings')}
                        aria-label={t('panels.permissions.autohost.openSettings')}
                      >
                        <Settings className="h-3.5 w-3.5" />
                      </button>
                    )}
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </div>

      <AutohostSettingsDialog
        open={settingsOpen}
        onOpenChange={setSettingsOpen}
        workspaceId={workspaceId}
      />
    </>
  );
}
