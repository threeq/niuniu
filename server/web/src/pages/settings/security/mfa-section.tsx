import { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { ShieldCheck, ShieldOff, RefreshCw, Copy, Check } from 'lucide-react';
import { toast } from 'sonner';
import { api, ApiError } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Badge } from '@/components/ui/badge';
import { Alert, AlertDescription } from '@/components/ui/alert';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';

interface MFAStatus {
  enabled: boolean;
  backup_codes_remain: number;
  trusted_device_count: number;
}

export function MfaSection() {
  const { t } = useTranslation('settings');

  // Status
  const [status, setStatus] = useState<MFAStatus | null>(null);
  const [statusLoading, setStatusLoading] = useState(true);

  // Setup flow
  const [setupDialogOpen, setSetupDialogOpen] = useState(false);
  const [setupData, setSetupData] = useState<{
    provisioning_uri: string;
    qr_data_uri: string;
    secret: string;
  } | null>(null);
  const [setupCode, setSetupCode] = useState('');
  const [setupError, setSetupError] = useState('');
  const [setupLoading, setSetupLoading] = useState(false);

  // Backup codes display (after enable/regenerate)
  const [backupCodesDialogOpen, setBackupCodesDialogOpen] = useState(false);
  const [backupCodes, setBackupCodes] = useState<string[]>([]);
  const [backupCodesCopied, setBackupCodesCopied] = useState(false);

  // Disable flow
  const [disableDialogOpen, setDisableDialogOpen] = useState(false);
  const [disableCode, setDisableCode] = useState('');
  const [disableError, setDisableError] = useState('');
  const [disableLoading, setDisableLoading] = useState(false);

  // Regenerate flow
  const [regenerateDialogOpen, setRegenerateDialogOpen] = useState(false);
  const [regenerateCode, setRegenerateCode] = useState('');
  const [regenerateError, setRegenerateError] = useState('');
  const [regenerateLoading, setRegenerateLoading] = useState(false);

  const fetchStatus = useCallback(async () => {
    try {
      const s = await api.getMFAStatus();
      setStatus(s);
    } catch {
      // Silently handle — user may not be authed
    } finally {
      setStatusLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchStatus();
  }, [fetchStatus]);

  // ── Setup flow ──────────────────────────────────────────────

  async function handleStartSetup() {
    setSetupDialogOpen(true);
    setSetupData(null);
    setSetupCode('');
    setSetupError('');
    setSetupLoading(true);

    try {
      const data = await api.setupMFA();
      setSetupData(data);
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : String(err);
      setSetupError(msg);
    } finally {
      setSetupLoading(false);
    }
  }

  async function handleEnableMFA() {
    if (!setupCode.trim() || setupCode.trim().length !== 6) {
      setSetupError(t('security.mfa.incorrectCode'));
      return;
    }
    setSetupError('');
    setSetupLoading(true);

    try {
      const result = await api.enableMFA(setupCode.trim());
      setSetupDialogOpen(false);
      setBackupCodes(result.backup_codes);
      setBackupCodesCopied(false);
      setBackupCodesDialogOpen(true);
      toast.success(t('security.mfa.setupSuccess'));
      fetchStatus();
    } catch (err) {
      if (err instanceof ApiError) {
        setSetupError(err.message || t('security.mfa.incorrectCode'));
      } else {
        setSetupError(t('security.mfa.incorrectCode'));
      }
    } finally {
      setSetupLoading(false);
    }
  }

  // ── Disable flow ────────────────────────────────────────────

  function handleDisableOpen() {
    setDisableCode('');
    setDisableError('');
    setDisableDialogOpen(true);
  }

  async function handleDisableMFA() {
    if (!disableCode.trim()) return;
    setDisableError('');
    setDisableLoading(true);

    try {
      await api.disableMFA(disableCode.trim());
      setDisableDialogOpen(false);
      toast.success(t('security.mfa.disableSuccess'));
      fetchStatus();
    } catch (err) {
      if (err instanceof ApiError) {
        setDisableError(err.message || t('security.mfa.incorrectCode'));
      } else {
        setDisableError(t('security.mfa.incorrectCode'));
      }
    } finally {
      setDisableLoading(false);
    }
  }

  // ── Regenerate backup codes ─────────────────────────────────

  function handleRegenerateOpen() {
    setRegenerateCode('');
    setRegenerateError('');
    setRegenerateDialogOpen(true);
  }

  async function handleRegenerate() {
    if (!regenerateCode.trim()) return;
    setRegenerateError('');
    setRegenerateLoading(true);

    try {
      const result = await api.regenerateBackupCodes(regenerateCode.trim());
      setRegenerateDialogOpen(false);
      setBackupCodes(result.backup_codes);
      setBackupCodesCopied(false);
      setBackupCodesDialogOpen(true);
      toast.success(t('security.mfa.regenerateSuccess'));
      fetchStatus();
    } catch (err) {
      if (err instanceof ApiError) {
        setRegenerateError(err.message || t('security.mfa.incorrectCode'));
      } else {
        setRegenerateError(t('security.mfa.incorrectCode'));
      }
    } finally {
      setRegenerateLoading(false);
    }
  }

  // ── Copy backup codes ───────────────────────────────────────

  function handleCopyBackupCodes() {
    void navigator.clipboard.writeText(backupCodes.join('\n'));
    setBackupCodesCopied(true);
  }

  // ── Render ──────────────────────────────────────────────────

  if (statusLoading) {
    return (
      <section>
        <h2 className="text-lg font-semibold text-warm-text mb-4">
          {t('security.mfa.setup')}
        </h2>
        <p className="text-sm text-muted-foreground animate-pulse">
          ...
        </p>
      </section>
    );
  }

  return (
    <section>
      <h2 className="text-lg font-semibold text-warm-text mb-4">
        {t('security.mfa.setup')}
      </h2>

      {status?.enabled ? (
        /* ── MFA enabled ── */
        <div className="space-y-4">
          <div className="flex items-center gap-2">
            <ShieldCheck className="h-5 w-5 text-success" />
            <span className="text-sm font-medium text-foreground">
              {t('security.mfa.enabled')}
            </span>
            <Badge variant="outline" className="border-success/50 text-success text-xs">
              {t('security.mfa.enabled')}
            </Badge>
          </div>

          <p className="text-sm text-muted-foreground">
            {t('security.mfa.backupCodesRemain', { count: status.backup_codes_remain })}
          </p>

          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={handleDisableOpen}
            >
              <ShieldOff className="h-4 w-4 mr-1" />
              {t('security.mfa.disable')}
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={handleRegenerateOpen}
            >
              <RefreshCw className="h-4 w-4 mr-1" />
              {t('security.mfa.regenerateBackupCodes')}
            </Button>
          </div>
        </div>
      ) : (
        /* ── MFA not enabled ── */
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            {t('security.mfa.description')}
          </p>
          <Button
            variant="outline"
            size="sm"
            onClick={handleStartSetup}
          >
            <ShieldCheck className="h-4 w-4 mr-1" />
            {t('security.mfa.setup')}
          </Button>
        </div>
      )}

      {/* ── Setup dialog ─────────────────────────────────────── */}
      <Dialog open={setupDialogOpen} onOpenChange={setSetupDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('security.mfa.setup')}</DialogTitle>
            <DialogDescription>
              {t('security.mfa.description')}
            </DialogDescription>
          </DialogHeader>

          {setupLoading && (
            <p className="text-sm text-muted-foreground text-center py-4">
              ...
            </p>
          )}

          {setupError && (
            <Alert variant="destructive">
              <AlertDescription>{setupError}</AlertDescription>
            </Alert>
          )}

          {setupData && !setupError && (
            <div className="space-y-4">
              {/* QR Code */}
              <div className="flex justify-center">
                <img
                  src={`https://api.qrserver.com/v1/create-qr-code/?size=200x200&data=${encodeURIComponent(setupData.provisioning_uri)}`}
                  alt={t('security.mfa.qrAlt')}
                  className="rounded-md border"
                  width={200}
                  height={200}
                />
              </div>

              {/* Manual secret */}
              <p className="text-xs text-muted-foreground text-center break-all font-mono">
                {setupData.secret}
              </p>

              {/* Verification code input */}
              <div className="space-y-2">
                <label className="text-sm font-medium text-foreground">
                  {t('security.mfa.verifyCode')}
                </label>
                <Input
                  type="text"
                  inputMode="numeric"
                  maxLength={6}
                  value={setupCode}
                  onChange={(e) => {
                    const val = e.target.value.replace(/\D/g, '');
                    setSetupCode(val.slice(0, 6));
                  }}
                  placeholder={t('security.mfa.verifyCodePlaceholder')}
                />
              </div>

              <DialogFooter>
                <Button
                  onClick={handleEnableMFA}
                  disabled={setupLoading || setupCode.length !== 6}
                >
                  {t('security.mfa.enable')}
                </Button>
              </DialogFooter>
            </div>
          )}
        </DialogContent>
      </Dialog>

      {/* ── Backup codes dialog ──────────────────────────────── */}
      <Dialog open={backupCodesDialogOpen} onOpenChange={setBackupCodesDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('security.mfa.backupCodesTitle')}</DialogTitle>
            <DialogDescription>
              {t('security.mfa.backupCodesWarning')}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-2">
              {backupCodes.map((code) => (
                <code
                  key={code}
                  className="text-sm font-mono bg-muted px-3 py-1.5 rounded text-center select-all"
                >
                  {code}
                </code>
              ))}
            </div>

            <Button
              variant="outline"
              size="sm"
              className="w-full"
              onClick={handleCopyBackupCodes}
            >
              {backupCodesCopied ? (
                <>
                  <Check className="h-4 w-4 mr-1" />
                  {t('security.mfa.backupCodesSaved')}
                </>
              ) : (
                <>
                  <Copy className="h-4 w-4 mr-1" />
                  {t('security.mfa.backupCodesSaved')}
                </>
              )}
            </Button>
          </div>

          <DialogFooter>
            <Button onClick={() => setBackupCodesDialogOpen(false)}>
              {t('security.mfa.backupCodesSaved')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ── Disable dialog ────────────────────────────────────── */}
      <Dialog open={disableDialogOpen} onOpenChange={setDisableDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('security.mfa.disableConfirm')}</DialogTitle>
            <DialogDescription>
              {t('security.mfa.disableDescription')}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-3">
            <Input
              type="text"
              inputMode="numeric"
              maxLength={6}
              value={disableCode}
              onChange={(e) => {
                const val = e.target.value.replace(/\D/g, '');
                setDisableCode(val.slice(0, 6));
              }}
              placeholder={t('security.mfa.verifyCodePlaceholder')}
            />

            {disableError && (
              <Alert variant="destructive">
                <AlertDescription>{disableError}</AlertDescription>
              </Alert>
            )}
          </div>

          <DialogFooter>
            <Button
              variant="destructive"
              onClick={handleDisableMFA}
              disabled={disableLoading || disableCode.length !== 6}
            >
              {t('security.mfa.disable')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ── Regenerate backup codes dialog ────────────────────── */}
      <Dialog open={regenerateDialogOpen} onOpenChange={setRegenerateDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('security.mfa.regenerateBackupCodes')}</DialogTitle>
            <DialogDescription>
              {t('security.mfa.regenerateDescription')}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-3">
            <Input
              type="text"
              inputMode="numeric"
              maxLength={6}
              value={regenerateCode}
              onChange={(e) => {
                const val = e.target.value.replace(/\D/g, '');
                setRegenerateCode(val.slice(0, 6));
              }}
              placeholder={t('security.mfa.verifyCodePlaceholder')}
            />

            {regenerateError && (
              <Alert variant="destructive">
                <AlertDescription>{regenerateError}</AlertDescription>
              </Alert>
            )}
          </div>

          <DialogFooter>
            <Button
              onClick={handleRegenerate}
              disabled={regenerateLoading || regenerateCode.length !== 6}
            >
              {t('security.mfa.regenerateBackupCodes')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
