import { useState, useEffect, useRef } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { ShieldCheck } from 'lucide-react';
import { useAuthStore } from '@/stores/auth-store';
import { useOrgStore } from '@/stores/org-store';
import { api } from '@/lib/api';
import { ApiError } from '@/lib/api';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { Alert, AlertDescription } from '@/components/ui/alert';

type LockKind = 'account' | 'ip' | null;

// localStorage key for the optional "remember username and password" feature.
// Credentials are stored in plaintext as an explicit user convenience on their
// own device (this is a self-hosted/local-first tool); the box is off by default.
const REMEMBER_KEY = 'niuniu.login.remember';

export function LoginPage() {
  const { t } = useTranslation('login');
  const { t: tAuth } = useTranslation('auth');

  // Password login state
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [lockKind, setLockKind] = useState<LockKind>(null);
  const [lockSecondsLeft, setLockSecondsLeft] = useState(0);
  const [remember, setRemember] = useState(false);

  // MFA state
  const [mfaRequired, setMfaRequired] = useState(false);
  const [mfaToken, setMfaToken] = useState('');
  const [mfaCode, setMfaCode] = useState('');
  const [useBackupCode, setUseBackupCode] = useState(false);
  const [trustDevice, setTrustDevice] = useState(true);
  const [mfaError, setMfaError] = useState('');

  const { setTokens, setUser } = useAuthStore();
  const navigate = useNavigate();

  // Countdown ticker
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    if (lockSecondsLeft <= 0) {
      if (intervalRef.current !== null) {
        clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
      if (lockKind !== null) {
        setLockKind(null);
      }
      return;
    }
    intervalRef.current = setInterval(() => {
      setLockSecondsLeft((prev) => {
        if (prev <= 1) {
          return 0;
        }
        return prev - 1;
      });
    }, 1000);
    return () => {
      if (intervalRef.current !== null) {
        clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
    };
  }, [lockSecondsLeft, lockKind]);

  // Prefill saved credentials when "remember" was enabled on a previous login.
  useEffect(() => {
    try {
      const raw = localStorage.getItem(REMEMBER_KEY);
      if (!raw) return;
      const saved = JSON.parse(raw) as { username?: string; password?: string };
      if (saved.username) setUsername(saved.username);
      if (saved.password) setPassword(saved.password);
      setRemember(true);
    } catch {
      // Ignore malformed storage — fall back to empty fields.
    }
  }, []);

  const lockMinutes = Math.floor(lockSecondsLeft / 60);
  const lockSecs = lockSecondsLeft % 60;
  const isLocked = lockKind !== null && lockSecondsLeft > 0;

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (isLocked) return;
    setError('');
    setLoading(true);

    try {
      // Login
      const loginRes = await fetch('/api/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      });

      if (!loginRes.ok) {
        const data = await loginRes.json().catch(() => ({})) as {
          error?: { code?: string; message?: string; retry_after?: number }
        };

        if (loginRes.status === 423 && data?.error?.code === 'ACCOUNT_LOCKED') {
          const secs = typeof data.error?.retry_after === 'number' ? data.error.retry_after : 1800;
          setLockKind('account');
          setLockSecondsLeft(secs);
          setError('');
          return;
        }

        if (loginRes.status === 429 && data?.error?.code === 'IP_LOCKED') {
          const secs = typeof data.error?.retry_after === 'number' ? data.error.retry_after : 3600;
          setLockKind('ip');
          setLockSecondsLeft(secs);
          setError('');
          return;
        }

        // 401 INVALID_CREDENTIALS or other error
        setLockKind(null);
        setError(data?.error?.message || t('errors.invalidCredentials'));
        return;
      }

      const body = await loginRes.json() as {
        mfa_required?: boolean;
        mfa_token?: string;
        access_token: string;
        refresh_token: string;
      };

      // Credentials accepted by the server — persist or clear them per the
      // "remember username and password" choice (before any MFA branch).
      try {
        if (remember) {
          localStorage.setItem(REMEMBER_KEY, JSON.stringify({ username, password }));
        } else {
          localStorage.removeItem(REMEMBER_KEY);
        }
      } catch {
        // Ignore storage quota / privacy-mode errors — login still proceeds.
      }

      // Check if MFA is required
      if (body.mfa_required && body.mfa_token) {
        setMfaRequired(true);
        setMfaToken(body.mfa_token);
        setMfaCode('');
        setMfaError('');
        return;
      }

      // Standard login flow
      setTokens(body.access_token, body.refresh_token);

      // Fetch user info
      const meRes = await fetch('/api/auth/me', {
        headers: { 'Authorization': `Bearer ${body.access_token}` },
      });
      if (meRes.ok) {
        const user = await meRes.json();
        setUser(user);
      } else {
        setError(t('errors.fetchUserFailed'));
        return;
      }

      // Bootstrap org store so the "组织" tab + "创建组织" button (gated on
      // currentUser.role === 'admin') render right away. main.tsx only fires
      // this on a cold SPA load with a token already in storage; SPA-routed
      // login lands on /workspaces without ever touching that path.
      useOrgStore.getState().invalidate()
      void useOrgStore.getState().fetch()

      navigate({ to: '/workspaces' });
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 423 && err.code === 'ACCOUNT_LOCKED') {
          setLockKind('account');
          setLockSecondsLeft(err.retry_after ?? 1800);
          setError('');
          return;
        }
        if (err.status === 429 && err.code === 'IP_LOCKED') {
          setLockKind('ip');
          setLockSecondsLeft(err.retry_after ?? 3600);
          setError('');
          return;
        }
      }
      setError(t('errors.networkError'));
    } finally {
      setLoading(false);
    }
  }

  async function handleMfaVerify(e: React.FormEvent) {
    e.preventDefault();
    if (!mfaCode.trim()) return;
    setMfaError('');
    setLoading(true);

    try {
      const result = await api.verifyMFA(mfaToken, mfaCode.trim(), trustDevice);
      setTokens(result.access_token, result.refresh_token);

      const meRes = await fetch('/api/auth/me', {
        headers: { 'Authorization': `Bearer ${result.access_token}` },
      });
      if (meRes.ok) {
        const user = await meRes.json();
        setUser(user);
      } else {
        setMfaError(tAuth('mfa.invalidCode'));
        return;
      }

      useOrgStore.getState().invalidate()
      void useOrgStore.getState().fetch()

      navigate({ to: '/workspaces' });
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.code === 'MFA_TOKEN_EXPIRED') {
          setMfaError(tAuth('mfa.expired'));
          // Return to password login after a brief delay
          setTimeout(() => {
            setMfaRequired(false);
            setMfaToken('');
            setMfaCode('');
            setMfaError('');
          }, 2000);
          return;
        }
        if (err.code === 'MFA_INVALID') {
          setMfaError(tAuth('mfa.invalidCode'));
          return;
        }
      }
      setMfaError(tAuth('mfa.invalidCode'));
    } finally {
      setLoading(false);
    }
  }

  function handleBackToLogin() {
    setMfaRequired(false);
    setMfaToken('');
    setMfaCode('');
    setMfaError('');
    setUseBackupCode(false);
    setTrustDevice(true);
  }

  // MFA verification form
  if (mfaRequired) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-muted">
        <div className="w-full max-w-sm">
          <div className="bg-card rounded-lg shadow-sm border p-8">
            <div className="text-center mb-6">
              <div className="mx-auto mb-3 flex h-12 w-12 items-center justify-center rounded-full bg-primary/10">
                <ShieldCheck className="h-6 w-6 text-primary" />
              </div>
              <h1 className="text-xl font-semibold text-foreground">
                {tAuth('mfa.title')}
              </h1>
              <p className="text-sm text-muted-foreground mt-1">
                {useBackupCode
                  ? tAuth('mfa.backupCodePlaceholder')
                  : tAuth('mfa.codePlaceholder')}
              </p>
            </div>

            <form onSubmit={handleMfaVerify} className="space-y-4">
              <div>
                <Input
                  id="mfa-code"
                  type="text"
                  inputMode="numeric"
                  maxLength={useBackupCode ? 8 : 6}
                  autoComplete="one-time-code"
                  value={mfaCode}
                  onChange={(e) => {
                    const val = e.target.value.replace(/\D/g, '');
                    const maxLen = useBackupCode ? 8 : 6;
                    setMfaCode(val.slice(0, maxLen));
                  }}
                  placeholder={
                    useBackupCode
                      ? tAuth('mfa.backupCodePlaceholder')
                      : tAuth('mfa.codePlaceholder')
                  }
                  autoFocus
                  required
                />
              </div>

              {/* MFA error */}
              {mfaError && (
                <Alert variant="destructive">
                  <AlertDescription>{mfaError}</AlertDescription>
                </Alert>
              )}

              {/* Trust device checkbox */}
              {!useBackupCode && (
                <div className="flex items-center space-x-2">
                  <Checkbox
                    id="trust-device"
                    checked={trustDevice}
                    onCheckedChange={(checked) => setTrustDevice(checked === true)}
                  />
                  <label
                    htmlFor="trust-device"
                    className="text-sm font-medium leading-none text-foreground peer-disabled:cursor-not-allowed peer-disabled:opacity-70"
                  >
                    {tAuth('mfa.trustDevice')}
                  </label>
                </div>
              )}

              {/* Action buttons */}
              <Button
                type="submit"
                disabled={loading || !mfaCode.trim()}
                className="w-full"
              >
                {loading ? t('form.submitting') : tAuth('mfa.verify')}
              </Button>

              {/* Toggle backup code / authenticator */}
              <div className="text-center">
                <button
                  type="button"
                  onClick={() => {
                    setUseBackupCode(!useBackupCode);
                    setMfaCode('');
                    setMfaError('');
                  }}
                  className="text-sm text-muted-foreground hover:text-foreground transition-colors"
                >
                  {useBackupCode
                    ? tAuth('mfa.useAuthenticator')
                    : tAuth('mfa.useBackupCode')}
                </button>
              </div>

              {/* Back to password login */}
              <div className="text-center">
                <button
                  type="button"
                  onClick={handleBackToLogin}
                  className="text-sm text-muted-foreground hover:text-foreground transition-colors"
                >
                  {tAuth('mfa.backButton')}
                </button>
              </div>
            </form>
          </div>
        </div>
      </div>
    );
  }

  // Password login form
  return (
    <div className="min-h-screen flex items-center justify-center bg-muted">
      <div className="w-full max-w-sm">
        <div className="bg-card rounded-lg shadow-sm border p-8">
          <div className="text-center mb-6">
            <img
              src="/icon.svg"
              alt={t('form.logoAlt')}
              className="mx-auto mb-3 h-14 w-14"
            />
            <h1 className="text-xl font-semibold text-foreground">{t('form.title')}</h1>
            <p className="text-sm text-muted-foreground mt-1">{t('form.subtitle')}</p>
          </div>

          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label htmlFor="username" className="block text-sm font-medium text-foreground mb-1">
                {t('form.username')}
              </label>
              <input
                id="username"
                type="text"
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                className="w-full px-3 py-2 border rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-ring focus:border-transparent"
                placeholder={t('form.usernamePlaceholder')}
                autoFocus
                required
              />
            </div>

            <div>
              <label htmlFor="password" className="block text-sm font-medium text-foreground mb-1">
                {t('form.password')}
              </label>
              <input
                id="password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full px-3 py-2 border rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-ring focus:border-transparent"
                placeholder={t('form.passwordPlaceholder')}
                required
              />
            </div>

            {/* Remember username and password */}
            <div className="flex items-center space-x-2">
              <Checkbox
                id="remember-me"
                checked={remember}
                onCheckedChange={(checked) => setRemember(checked === true)}
              />
              <label
                htmlFor="remember-me"
                className="text-sm font-medium leading-none text-foreground"
              >
                {t('form.rememberMe')}
              </label>
            </div>

            {/* Lock banners */}
            {isLocked && lockKind === 'account' && (
              <div
                role="alert"
                aria-live="assertive"
                className="rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-sm text-warning"
              >
                {t('locked.account', { minutes: lockMinutes, seconds: String(lockSecs).padStart(2, '0') })}
              </div>
            )}
            {isLocked && lockKind === 'ip' && (
              <div
                role="alert"
                aria-live="assertive"
                className="rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-sm text-warning"
              >
                {t('locked.ip', { minutes: lockMinutes })}
              </div>
            )}

            {/* Generic error */}
            {!isLocked && error && (
              <div
                role="alert"
                aria-live="polite"
                className="text-sm text-destructive bg-destructive/10 rounded-md px-3 py-2"
              >
                {error}
              </div>
            )}

            <button
              type="submit"
              disabled={loading || isLocked}
              className="w-full py-2 px-4 bg-foreground text-background text-sm font-medium rounded-md hover:bg-foreground/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors duration-150"
            >
              {loading ? t('form.submitting') : t('form.submit')}
            </button>
          </form>
        </div>
      </div>
    </div>
  );
}
