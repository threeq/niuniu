// HealthLight — a small status dot for an integration source's continuous
// health (Epic #726 / #744), shared by the provider list and the data-source
// panel. The dot's colour follows the design system's discrete semantic
// tokens; the label is always localized and available as a tooltip, so the
// light is never the only signal.
import { useTranslation } from 'react-i18next';

/** The health verdict stored on a source row. */
export type HealthStatus = 'unknown' | 'healthy' | 'unhealthy' | 'disabled';

const DOT_CLASS: Record<HealthStatus, string> = {
  healthy: 'bg-success',
  unhealthy: 'bg-destructive',
  disabled: 'bg-warm-text-muted',
  unknown: 'bg-warning',
};

function normalize(status?: string): HealthStatus {
  switch (status) {
    case 'healthy':
    case 'unhealthy':
    case 'disabled':
      return status;
    default:
      return 'unknown';
  }
}

export function healthLabelKey(status: HealthStatus): string {
  return `integrations.health.status.${status}`;
}

interface HealthLightProps {
  status?: string;
  /** Extra context appended to the tooltip (e.g. the last probe error). */
  detail?: string;
  className?: string;
}

export function HealthLight({ status, detail, className }: HealthLightProps) {
  const { t } = useTranslation('settings');
  const s = normalize(status);
  const label = t(healthLabelKey(s));
  return (
    <span
      className={
        'inline-flex items-center gap-1 text-[10px] text-warm-text-muted ' +
        (className ?? '')
      }
      title={detail ? `${label} — ${detail}` : label}
      aria-label={label}
      role="status"
    >
      <span
        aria-hidden
        className={`size-2 rounded-full shrink-0 ${DOT_CLASS[s]}`}
      />
      {label}
    </span>
  );
}
