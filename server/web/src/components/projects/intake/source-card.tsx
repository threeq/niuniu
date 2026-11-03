import type { ComponentType } from 'react'
import { useTranslation } from 'react-i18next'
import { Bot, Github, Mail, Settings2, Workflow, Webhook } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { IntakeMode, IntakeSetting, IntakeSourceKind } from '@/lib/intake-api'
import { cn } from '@/lib/utils'
import { INTAKE_MODES, dailyCapValue, type DraftSetting } from './intake-utils'

const SOURCE_ICONS: Record<IntakeSourceKind, ComponentType<{ className?: string }>> = {
  im_bot: Bot,
  github: Github,
  ci: Workflow,
  email: Mail,
  webhook: Webhook,
}

interface Props {
  projectId: number
  source: IntakeSetting
  draft: DraftSetting
  connected: boolean
  canConnect: boolean
  pending: number
  ruleCount: number
  isMobile: boolean
  onModeChange: (mode: IntakeMode) => void
  onOpenConfig: () => void
  onConnect: () => void
}

export function SourceCard({
  projectId,
  source,
  draft,
  connected,
  canConnect,
  pending,
  ruleCount,
  isMobile,
  onModeChange,
  onOpenConfig,
  onConnect,
}: Props) {
  const { t } = useTranslation('intake')
  const Icon = SOURCE_ICONS[source.source_kind] ?? Webhook
  const cap = dailyCapValue(draft.daily_cap) ?? source.daily_cap
  const mode = draft.mode

  return (
    <div className="space-y-3 rounded-lg border bg-card p-4">
      <div className="flex items-center gap-2">
        <Icon className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        <span className="text-sm font-medium">{t(`intake.source.${source.source_kind}`)}</span>
        <Badge
          variant="outline"
          className={cn(
            'ml-auto inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs',
            connected
              ? 'border-success/30 bg-success/15 text-success'
              : 'border-warm-border bg-warm-muted text-warm-text-muted',
          )}
        >
          {connected ? t('intake.connected') : t('intake.notConnected')}
        </Badge>
      </div>

      {connected ? (
        <>
          {isMobile ? (
            <div className="space-y-1.5">
              <Select value={mode} onValueChange={(v) => onModeChange(v as IntakeMode)}>
                <SelectTrigger aria-label={t('intake.mode.label')} className="h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {INTAKE_MODES.map((m) => (
                    <SelectItem key={m} value={m}>
                      {t(`intake.mode.${m}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          ) : (
            <div
              className="inline-flex overflow-hidden rounded-md border border-warm-border"
              role="group"
              aria-label={t('intake.mode.label')}
            >
              {INTAKE_MODES.map((m) => (
                <Button
                  key={m}
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-pressed={mode === m}
                  title={t(`intake.mode.${m}Description`)}
                  onClick={() => onModeChange(m)}
                  className={cn(
                    'rounded-none border-r border-warm-border px-3 last:border-r-0',
                    mode === m
                      ? 'bg-brand-soft text-brand hover:bg-brand-soft'
                      : 'bg-warm-muted text-warm-text-muted',
                  )}
                >
                  {t(`intake.mode.${m}`)}
                </Button>
              ))}
            </div>
          )}
          <p className="text-xs text-muted-foreground">{t(`intake.mode.${mode}Description`)}</p>

          <p className="text-xs text-muted-foreground tabular-nums">
            {t('intake.summary', { cap, rules: ruleCount, members: draft.allow_list.length })}
          </p>

          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" variant="outline" size="sm" onClick={onOpenConfig}>
              <Settings2 className="mr-1 h-4 w-4" aria-hidden="true" />
              {t('intake.configure')}
            </Button>
            <Button type="button" variant="ghost" size="sm" asChild>
              <a href={`/intake?project=${projectId}&source=${source.source_kind}`}>
                {t('intake.viewPending', { count: pending })}
              </a>
            </Button>
          </div>
        </>
      ) : (
        <div className="flex flex-wrap items-center justify-end gap-2">
          {!canConnect ? (
            <span className="text-xs text-muted-foreground">{t('intake.connectComingSoon')}</span>
          ) : null}
          <Button type="button" variant="outline" size="sm" onClick={onConnect} disabled={!canConnect}>
            {t('intake.connect')}
          </Button>
        </div>
      )}
    </div>
  )
}
