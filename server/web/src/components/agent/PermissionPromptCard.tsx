import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Lock } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { HIGH_RISK_TOOLS } from '@/types/permission';
import type { DecideBody, Matcher, PermissionRequest } from '@/types/permission';
import { PermissionInputRenderer } from './PermissionInputRenderer';
import { PermissionMatcherMenu } from './PermissionMatcherMenu';

interface Props {
  request: PermissionRequest;
  onDecide: (body: DecideBody) => void;
}

export function PermissionPromptCard({ request, onDecide }: Props) {
  const { t } = useTranslation();
  const [denyOpen, setDenyOpen] = useState(false);
  const [denyMsg, setDenyMsg] = useState('');
  const [remaining, setRemaining] = useState(() => msUntil(request.expires_at));

  useEffect(() => {
    const id = setInterval(() => setRemaining(msUntil(request.expires_at)), 1000);
    return () => clearInterval(id);
  }, [request.expires_at]);

  const isPending = request.status === 'pending';
  const isHighRisk = HIGH_RISK_TOOLS.has(request.tool_name);

  const handleAlways = (m: Matcher) =>
    onDecide({ decision: 'allow', scope: 'always', matcher: m });

  if (!isPending) {
    return (
      <Card className="my-2 border-dashed" data-permission-card={request.status}>
        <CardContent className="py-2 text-xs text-muted-foreground">
          {labelForStatus(request.status, t)} · {request.tool_name}
        </CardContent>
      </Card>
    );
  }

  return (
    <Card className="my-2 border-amber-300" data-permission-card="pending">
      <CardHeader className="py-2">
        <CardTitle className="flex items-center gap-1 text-sm">
          <Lock className="h-3.5 w-3.5" />
          <span>
            {t('workspaces:permission.card.wantsToCall', { tool: request.tool_name })}
          </span>
          <span className="ml-auto text-xs text-muted-foreground">
            {formatRemaining(remaining, t)}
          </span>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-2 py-2">
        <PermissionInputRenderer toolName={request.tool_name} input={request.tool_input} />
        {!denyOpen ? (
          <div className="flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              onClick={() => onDecide({ decision: 'allow', scope: 'once' })}
            >
              {t('workspaces:permission.card.allowOnce')}
            </Button>
            {isHighRisk ? (
              <PermissionMatcherMenu
                toolName={request.tool_name}
                toolInput={request.tool_input}
                onPick={handleAlways}
                trigger={
                  <Button size="sm" variant="secondary">
                    {t('workspaces:permission.card.alwaysAllow')}
                  </Button>
                }
              />
            ) : (
              <Button
                size="sm"
                variant="secondary"
                onClick={() => handleAlways({ kind: 'any', value: '' })}
              >
                {t('workspaces:permission.card.alwaysAllow')}
              </Button>
            )}
            <Button size="sm" variant="ghost" onClick={() => setDenyOpen(true)}>
              {t('workspaces:permission.card.deny')}
            </Button>
          </div>
        ) : (
          <div className="space-y-1">
            <textarea
              className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
              rows={3}
              placeholder={t('workspaces:permission.card.denyPlaceholder')}
              value={denyMsg}
              onChange={(e) => setDenyMsg(e.target.value)}
            />
            <div className="flex gap-2">
              <Button
                size="sm"
                variant="destructive"
                onClick={() =>
                  onDecide({
                    decision: 'deny',
                    scope: 'once',
                    deny_message: denyMsg,
                  })
                }
              >
                {t('workspaces:permission.card.confirmDeny')}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setDenyOpen(false);
                  setDenyMsg('');
                }}
              >
                {t('common:actions.cancel')}
              </Button>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function msUntil(unixMs: number): number {
  return unixMs - Date.now();
}

function formatRemaining(ms: number, t: TFunction): string {
  if (ms <= 0) return t('workspaces:permission.card.expired');
  const totalSec = Math.floor(ms / 1000);
  const m = Math.floor(totalSec / 60);
  const s = totalSec % 60;
  return t('workspaces:permission.card.remaining', { m, s });
}

function labelForStatus(
  status: PermissionRequest['status'],
  t: TFunction,
): string {
  switch (status) {
    case 'allowed':
      return t('workspaces:permission.card.allowed');
    case 'denied':
      return t('workspaces:permission.card.denied');
    case 'timeout':
      return t('workspaces:permission.card.timeout');
    case 'cancelled':
      return t('workspaces:permission.card.cancelled');
    default:
      return status;
  }
}
