import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { CircleCheck, Eye, EyeOff, ShieldCheck } from 'lucide-react';
import { credentialOnboardingApi, type CredentialFieldSpec } from '@/lib/credential-onboarding-api';
import { ApiError } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';

interface Props {
  token: string;
}

type Status = 'loading' | 'idle' | 'submitting' | 'success' | 'expired' | 'error';

/**
 * Public, token-authenticated credential submission page (issue #727). It is
 * the browser half of the one-time credential link: an agent mints the link,
 * the user types the secret HERE (never in the conversation), and the server
 * writes it straight into the encrypted store. The page renders whatever field
 * list the server declares — it has no per-provider knowledge of its own, so
 * new integrations need no frontend change.
 *
 * Mounted outside the authenticated layout: the token in the URL is the only
 * authorization, and the server sends no-store / no-referrer / strict CSP with
 * this document.
 */
export function CredentialOnboardingPage({ token }: Props) {
  const { t } = useTranslation('projects');

  const [status, setStatus] = useState<Status>('loading');
  const [title, setTitle] = useState('');
  const [fields, setFields] = useState<CredentialFieldSpec[]>([]);
  const [values, setValues] = useState<Record<string, string>>({});
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});
  const [errorMessage, setErrorMessage] = useState('');

  useEffect(() => {
    void credentialOnboardingApi
      .getInfo(token)
      .then((info) => {
        setTitle(info.title);
        setFields(info.fields ?? []);
        setStatus('idle');
      })
      .catch(() => {
        // Every info-fetch failure reads as "link not usable" to the user; the
        // server deliberately returns the same 410 for unknown/expired/used.
        setStatus('expired');
      });
  }, [token]);

  const handleSubmit = useCallback(
    async (e: FormEvent) => {
      e.preventDefault();
      setStatus('submitting');
      try {
        await credentialOnboardingApi.submit(token, values);
        setStatus('success');
      } catch (err) {
        if (err instanceof ApiError && err.status === 410) {
          setStatus('expired');
          return;
        }
        setErrorMessage(err instanceof Error ? err.message : '');
        setStatus('error');
      }
    },
    [token, values],
  );

  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <ShieldCheck className="h-5 w-5" />
            {title || t('credentialOnboarding.pageTitle')}
          </CardTitle>
          <CardDescription>{t('credentialOnboarding.pageDescription')}</CardDescription>
        </CardHeader>
        <CardContent>
          {status === 'loading' && (
            <p className="text-sm text-muted-foreground">{t('credentialOnboarding.loading')}</p>
          )}

          {status === 'expired' && (
            <p className="text-sm text-destructive">{t('credentialOnboarding.expiredMessage')}</p>
          )}

          {status === 'error' && (
            <p className="text-sm text-destructive">
              {errorMessage || t('credentialOnboarding.errorMessage')}
            </p>
          )}

          {status === 'success' && (
            <div className="flex items-start gap-2 text-sm">
              <CircleCheck className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
              <span>{t('credentialOnboarding.successMessage')}</span>
            </div>
          )}

          {(status === 'idle' || status === 'submitting') && (
            <form className="space-y-4" onSubmit={(e) => void handleSubmit(e)}>
              {fields.map((field) => (
                <div key={field.name} className="space-y-1">
                  <Label htmlFor={`cred-${field.name}`}>
                    {field.label || field.name}
                    {field.required && <span className="text-destructive"> *</span>}
                  </Label>
                  <div className="flex items-center gap-2">
                    <Input
                      id={`cred-${field.name}`}
                      type={field.secret && !revealed[field.name] ? 'password' : 'text'}
                      value={values[field.name] ?? ''}
                      placeholder={field.placeholder}
                      required={field.required}
                      autoComplete="off"
                      autoCorrect="off"
                      spellCheck={false}
                      onChange={(e) =>
                        setValues((prev) => ({ ...prev, [field.name]: e.target.value }))
                      }
                    />
                    {field.secret && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        aria-label={
                          revealed[field.name]
                            ? t('credentialOnboarding.hideSecret')
                            : t('credentialOnboarding.showSecret')
                        }
                        onClick={() =>
                          setRevealed((prev) => ({ ...prev, [field.name]: !prev[field.name] }))
                        }
                      >
                        {revealed[field.name] ? (
                          <EyeOff className="h-4 w-4" />
                        ) : (
                          <Eye className="h-4 w-4" />
                        )}
                      </Button>
                    )}
                  </div>
                </div>
              ))}

              <Button type="submit" className="w-full" disabled={status === 'submitting'}>
                {status === 'submitting'
                  ? t('credentialOnboarding.submitting')
                  : t('credentialOnboarding.submit')}
              </Button>
            </form>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
