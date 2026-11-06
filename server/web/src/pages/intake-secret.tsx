import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Check, Copy, KeyRound, Link2 } from 'lucide-react';
import { apiFetch, ApiError } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';

interface Props {
  token: string;
}

interface IntakeSecret {
  secret: string;
  source_id: number;
  source_kind: string;
  name: string;
  inbound_path: string;
  expires_at: string;
}

type Status = 'loading' | 'ready' | 'expired' | 'error';

/**
 * One-time intake secret page (design §10.1). Create/Rotate hand the caller a
 * link to this page instead of the plaintext secret, so the secret never enters
 * a conversation or a client's history. The token is consumed by the first
 * fetch: a refresh, a second tab or a forwarded link all read as expired.
 *
 * Public route (no session): the token in the URL is the only authorization.
 */
export function IntakeSecretPage({ token }: Props) {
  const { t } = useTranslation('intake');

  const [status, setStatus] = useState<Status>('loading');
  const [data, setData] = useState<IntakeSecret | null>(null);
  const [copied, setCopied] = useState<'secret' | 'url' | null>(null);

  useEffect(() => {
    let cancelled = false;
    void apiFetch<IntakeSecret>(`/intake/sources/secret/${token}`, {
      method: 'GET',
      // 410 is an expected state on this page, not a background failure.
      suppressError: true,
    })
      .then((result) => {
        if (cancelled) return;
        setData(result);
        setStatus('ready');
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setStatus(err instanceof ApiError && err.status === 410 ? 'expired' : 'error');
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  const copy = (kind: 'secret' | 'url', value: string) => {
    void navigator.clipboard?.writeText(value).then(() => {
      setCopied(kind);
      window.setTimeout(() => setCopied(null), 2000);
    });
  };

  const inboundUrl = data ? `${window.location.origin}${data.inbound_path}` : '';

  return (
    <div className="min-h-screen flex items-center justify-center bg-warm-canvas p-4">
      <Card className="w-full max-w-lg">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <KeyRound className="h-5 w-5" />
            {t('secretPage.title')}
          </CardTitle>
          <CardDescription>{t('secretPage.description')}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          {status === 'loading' && (
            <p className="text-sm text-warm-text-muted">{t('secretPage.loading')}</p>
          )}

          {status === 'expired' && (
            <p className="text-sm text-destructive">{t('secretPage.expired')}</p>
          )}

          {status === 'error' && (
            <p className="text-sm text-destructive">{t('secretPage.loadError')}</p>
          )}

          {status === 'ready' && data && (
            <>
              <div className="space-y-1">
                <Label htmlFor="intake-secret-value">{t('secretPage.secretLabel')}</Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="intake-secret-value"
                    readOnly
                    value={data.secret}
                    className="font-mono text-xs"
                    onFocus={(e) => e.currentTarget.select()}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    aria-label={t('secretPage.copySecret')}
                    onClick={() => copy('secret', data.secret)}
                  >
                    {copied === 'secret' ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
                  </Button>
                </div>
              </div>

              <div className="space-y-1">
                <Label htmlFor="intake-secret-url">{t('secretPage.inboundUrlLabel')}</Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="intake-secret-url"
                    readOnly
                    value={inboundUrl}
                    className="font-mono text-xs"
                    onFocus={(e) => e.currentTarget.select()}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    aria-label={t('secretPage.copyUrl')}
                    onClick={() => copy('url', inboundUrl)}
                  >
                    {copied === 'url' ? <Check className="h-4 w-4" /> : <Link2 className="h-4 w-4" />}
                  </Button>
                </div>
              </div>

              <p className="text-xs text-warm-text-muted">
                {t('secretPage.hint', { name: data.name })}
              </p>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
