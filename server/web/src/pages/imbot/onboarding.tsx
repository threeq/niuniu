import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Bot, Copy } from 'lucide-react';
import { QRCodeSVG } from 'qrcode.react';
import { imbotApi } from '@/lib/imbot-api';
import { ApiError } from '@/lib/api';
import { copyTextToClipboard } from '@/lib/copy-to-clipboard';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';

interface Props {
  token: string;
}

type Status = 'loading' | 'idle' | 'submitting' | 'success' | 'expired' | 'error';

interface OnboardingInfo {
  platform: string;
  channel_name: string;
  connection_mode: string;
}

export function ImBotOnboardingPage({ token }: Props) {
  const { t } = useTranslation('projects');

  const [status, setStatus] = useState<Status>('loading');
  const [errorMessage, setErrorMessage] = useState('');
  const [info, setInfo] = useState<OnboardingInfo | null>(null);
  // Editable bot name — prefilled with the name chosen when the link was issued,
  // but the person entering the credential can override it (one platform can
  // hold many bots, so a distinguishing name helps).
  const [name, setName] = useState('');

  // Per-platform field state
  // lark
  const [appId, setAppId] = useState('');
  const [appSecret, setAppSecret] = useState('');
  // telegram
  const [botToken, setBotToken] = useState('');
  // dingtalk
  const [clientId, setClientId] = useState('');
  const [clientSecret, setClientSecret] = useState('');
  const [robotCode, setRobotCode] = useState('');
  const [webhookSecret, setWebhookSecret] = useState('');
  // wework
  const [corpId, setCorpId] = useState('');
  const [agentId, setAgentId] = useState('');
  const [secret, setSecret] = useState('');
  const [callbackToken, setCallbackToken] = useState('');
  const [aesKey, setAesKey] = useState('');
  // wechat (QR scan): no credential fields — the token is minted by scanning.
  const [qrImg, setQrImg] = useState<string | null>(null);
  const [wechatStatus, setWechatStatus] = useState('');
  const [verifyCode, setVerifyCode] = useState('');
  const verifyCodeRef = useRef('');
  useEffect(() => {
    verifyCodeRef.current = verifyCode;
  }, [verifyCode]);

  // After successful submit for webhook modes
  const [webhookCallbackUrl, setWebhookCallbackUrl] = useState<string | null>(null);
  const [copyStatus, setCopyStatus] = useState<'idle' | 'copied' | 'failed'>('idle');
  // Channel id returned on submit — shown so the user can relay it back to the
  // onboarding assistant (which needs it for the connectivity test).
  const [channelId, setChannelId] = useState<number | null>(null);
  const [idCopyStatus, setIdCopyStatus] = useState<'idle' | 'copied' | 'failed'>('idle');

  // WeChat: (re)start the QR handshake — fetch a fresh code to display.
  const startWechat = useCallback(async () => {
    setErrorMessage('');
    setQrImg(null);
    setVerifyCode('');
    setWechatStatus('starting');
    try {
      const { qrcode_img_content } = await imbotApi.wechatLoginStart(token);
      setQrImg(qrcode_img_content);
      setWechatStatus('wait');
    } catch (e) {
      if (e instanceof ApiError && e.status === 410) {
        setStatus('expired');
      } else {
        setStatus('error');
        setErrorMessage(e instanceof Error ? e.message : String(e));
      }
    }
  }, [token]);

  useEffect(() => {
    void imbotApi
      .getOnboardingInfo(token)
      .then((data) => {
        setInfo(data);
        setName(data.channel_name ?? '');
        setStatus('idle');
        // WeChat has no credential form — kick off the QR handshake immediately
        // (from this async callback, not synchronously in an effect body).
        if (data.platform === 'wechat') void startWechat();
      })
      .catch((e) => {
        if (e instanceof ApiError && e.status === 410) {
          setStatus('expired');
        } else {
          setStatus('expired'); // treat all info-fetch errors as invalid/expired
        }
      });
  }, [token, startWechat]);

  // Long-poll the QR login status while a code is shown. Each poll may hang
  // server-side (~40s), so it is sequential and rescheduled after each result;
  // it stops on confirmation (status → success) or when the QR is cleared.
  useEffect(() => {
    if (info?.platform !== 'wechat' || !qrImg || status === 'success') return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const loop = async () => {
      try {
        const res = await imbotApi.wechatLoginPoll(token, verifyCodeRef.current);
        if (cancelled) return;
        setWechatStatus(res.status);
        if (res.status === 'confirmed') {
          setChannelId(res.channel_id ?? null);
          setStatus('success');
          return;
        }
        if (res.status === 'expired' || res.status === 'binded_redirect') {
          setQrImg(null); // stops the loop; the refresh button restarts it
          return;
        }
        if (res.status !== 'need_verifycode' && verifyCodeRef.current) {
          setVerifyCode(''); // code accepted; clear the stale entry
        }
      } catch (e) {
        if (cancelled) return;
        if (e instanceof ApiError && e.status === 410) {
          setStatus('expired');
          return;
        }
      }
      timer = setTimeout(() => void loop(), 800);
    };
    timer = setTimeout(() => void loop(), 300);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [info, qrImg, status, token]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!info) return;
    setStatus('submitting');
    setErrorMessage('');

    const body: Record<string, string> = {};
    if (name.trim()) body.name = name.trim();

    if (info.platform === 'lark') {
      if (appId.trim()) body.app_id = appId.trim();
      if (appSecret.trim()) body.app_secret = appSecret.trim();
    } else if (info.platform === 'telegram') {
      if (botToken.trim()) body.bot_token = botToken.trim();
    } else if (info.platform === 'dingtalk') {
      if (clientId.trim()) body.client_id = clientId.trim();
      if (clientSecret.trim()) body.client_secret = clientSecret.trim();
      if (robotCode.trim()) body.robot_code = robotCode.trim();
      if (info.connection_mode === 'webhook' && webhookSecret.trim())
        body.webhook_secret = webhookSecret.trim();
    } else if (info.platform === 'wework') {
      if (corpId.trim()) body.corp_id = corpId.trim();
      if (agentId.trim()) body.agent_id = agentId.trim();
      if (secret.trim()) body.secret = secret.trim();
      if (callbackToken.trim()) body.token = callbackToken.trim();
      if (aesKey.trim()) body.aes_key = aesKey.trim();
    }

    try {
      const result = await imbotApi.submitOnboardingCredential(token, body);
      setChannelId(result.channel_id);
      if (info.connection_mode === 'webhook') {
        setWebhookCallbackUrl(
          `${window.location.origin}/api/imbot/webhook/${result.channel_id}`,
        );
      }
      setStatus('success');
    } catch (e) {
      if (e instanceof ApiError && e.status === 410) {
        setStatus('expired');
      } else {
        setStatus('error');
        setErrorMessage(e instanceof Error ? e.message : String(e));
      }
    }
  }

  async function handleCopy() {
    if (!webhookCallbackUrl) return;
    const ok = await copyTextToClipboard(webhookCallbackUrl);
    setCopyStatus(ok ? 'copied' : 'failed');
    setTimeout(() => setCopyStatus('idle'), 2000);
  }

  async function handleCopyChannelId() {
    if (channelId == null) return;
    const ok = await copyTextToClipboard(String(channelId));
    setIdCopyStatus(ok ? 'copied' : 'failed');
    setTimeout(() => setIdCopyStatus('idle'), 2000);
  }

  // Return to wherever the user opened this page from (the workspace chat that
  // showed the credential link). This is a standalone route outside the app
  // shell, so there is no in-app back affordance otherwise; fall back to the
  // home route when the page was opened without history (e.g. pasted URL).
  function goBack() {
    if (window.history.length > 1) {
      window.history.back();
    } else {
      window.location.assign('/');
    }
  }

  const renderFields = () => {
    if (!info) return null;

    if (info.platform === 'lark') {
      return (
        <>
          <div className="space-y-1">
            <Label htmlFor="app-id">{t('imbot.appId')}</Label>
            <Input
              id="app-id"
              value={appId}
              onChange={(e) => setAppId(e.target.value)}
              placeholder={t('imbot.onboarding.appIdPlaceholder')}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="app-secret">{t('imbot.appSecret')}</Label>
            <Input
              id="app-secret"
              type="password"
              value={appSecret}
              onChange={(e) => setAppSecret(e.target.value)}
              placeholder={t('imbot.onboarding.appSecretPlaceholder')}
            />
          </div>
        </>
      );
    }

    if (info.platform === 'telegram') {
      return (
        <div className="space-y-1">
          <Label htmlFor="bot-token">{t('imbot.botToken')}</Label>
          <Input
            id="bot-token"
            type="password"
            value={botToken}
            onChange={(e) => setBotToken(e.target.value)}
            placeholder={t('imbot.onboarding.botTokenPlaceholder')}
          />
        </div>
      );
    }

    if (info.platform === 'dingtalk') {
      return (
        <>
          <div className="space-y-1">
            <Label htmlFor="client-id">{t('imbot.clientId')}</Label>
            <Input
              id="client-id"
              value={clientId}
              onChange={(e) => setClientId(e.target.value)}
              placeholder={t('imbot.onboarding.clientIdPlaceholder')}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="client-secret">{t('imbot.clientSecret')}</Label>
            <Input
              id="client-secret"
              type="password"
              value={clientSecret}
              onChange={(e) => setClientSecret(e.target.value)}
              placeholder={t('imbot.onboarding.clientSecretPlaceholder')}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="robot-code">{t('imbot.robotCode')}</Label>
            <Input
              id="robot-code"
              value={robotCode}
              onChange={(e) => setRobotCode(e.target.value)}
              placeholder={t('imbot.onboarding.robotCodePlaceholder')}
            />
          </div>
          {info.connection_mode === 'webhook' && (
            <div className="space-y-1">
              <Label htmlFor="webhook-secret">
                {t('imbot.webhookSecret')}{' '}
                <span className="text-muted-foreground text-xs">({t('imbot.optional')})</span>
              </Label>
              <Input
                id="webhook-secret"
                type="password"
                value={webhookSecret}
                onChange={(e) => setWebhookSecret(e.target.value)}
                placeholder={t('imbot.onboarding.webhookSecretPlaceholder')}
              />
            </div>
          )}
        </>
      );
    }

    if (info.platform === 'wework') {
      return (
        <>
          <div className="rounded-md border border-blue-200 bg-blue-50 p-3 text-sm text-blue-800 dark:border-blue-800 dark:bg-blue-950 dark:text-blue-200">
            {t('imbot.weworkWebhookOnly')}
          </div>
          <div className="space-y-1">
            <Label htmlFor="corp-id">{t('imbot.corpId')}</Label>
            <Input
              id="corp-id"
              value={corpId}
              onChange={(e) => setCorpId(e.target.value)}
              placeholder={t('imbot.onboarding.corpIdPlaceholder')}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="agent-id">{t('imbot.agentId')}</Label>
            <Input
              id="agent-id"
              value={agentId}
              onChange={(e) => setAgentId(e.target.value)}
              placeholder={t('imbot.onboarding.agentIdPlaceholder')}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="secret">{t('imbot.secret')}</Label>
            <Input
              id="secret"
              type="password"
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
              placeholder={t('imbot.onboarding.secretPlaceholder')}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="callback-token">{t('imbot.token')}</Label>
            <Input
              id="callback-token"
              value={callbackToken}
              onChange={(e) => setCallbackToken(e.target.value)}
              placeholder={t('imbot.onboarding.tokenPlaceholder')}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="aes-key">{t('imbot.aesKey')}</Label>
            <Input
              id="aes-key"
              type="password"
              value={aesKey}
              onChange={(e) => setAesKey(e.target.value)}
              placeholder={t('imbot.onboarding.aesKeyPlaceholder')}
            />
          </div>
        </>
      );
    }

    return null;
  };

  const wechatStatusLabel = () => {
    switch (wechatStatus) {
      case 'scaned':
        return t('imbot.wechat.statusScaned');
      case 'need_verifycode':
        return t('imbot.wechat.statusNeedVerify');
      case 'verify_code_blocked':
        return t('imbot.wechat.statusBlocked');
      case 'binded_redirect':
        return t('imbot.wechat.statusBinded');
      case 'expired':
        return t('imbot.wechat.statusExpired');
      default:
        return t('imbot.wechat.statusWait');
    }
  };

  const renderWechat = () => (
    <div className="space-y-4">
      <div className="rounded-md border border-blue-200 bg-blue-50 p-3 text-sm text-blue-800 dark:border-blue-800 dark:bg-blue-950 dark:text-blue-200">
        {t('imbot.wechat.scanHint')}
      </div>
      {qrImg ? (
        <div className="flex flex-col items-center gap-3">
          {/* Rendered locally — the WeChat login handshake URL must never be sent
              to a third-party QR service (it would leak a scannable login token). */}
          <div
            className="rounded-md border border-border bg-white p-3"
            role="img"
            aria-label={t('imbot.wechat.qrAlt')}
          >
            <QRCodeSVG value={qrImg} size={200} />
          </div>
          <p className="text-sm text-muted-foreground">{wechatStatusLabel()}</p>
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">
          {wechatStatus === 'expired' || wechatStatus === 'binded_redirect'
            ? wechatStatusLabel()
            : t('imbot.wechat.starting')}
        </p>
      )}
      {wechatStatus === 'need_verifycode' && (
        <div className="space-y-1">
          <Label htmlFor="verify-code">{t('imbot.wechat.verifyCodeLabel')}</Label>
          <Input
            id="verify-code"
            value={verifyCode}
            onChange={(e) => setVerifyCode(e.target.value)}
            placeholder={t('imbot.wechat.verifyCodePlaceholder')}
          />
        </div>
      )}
      <Button type="button" variant="outline" className="w-full" onClick={() => void startWechat()}>
        {t('imbot.wechat.refreshQr')}
      </Button>
    </div>
  );

  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Bot className="h-5 w-5" />
            {t('imbot.onboarding.pageTitle')}
          </CardTitle>
          <CardDescription>{t('imbot.onboarding.pageDescription')}</CardDescription>
        </CardHeader>
        <CardContent>
          {status === 'loading' && (
            <p className="text-sm text-muted-foreground">{t('imbot.loading')}</p>
          )}

          {status === 'expired' && (
            <p className="text-sm text-destructive">{t('imbot.onboarding.expiredMessage')}</p>
          )}

          {status === 'error' && (
            <p className="text-sm text-destructive">{errorMessage || t('imbot.onboarding.errorMessage')}</p>
          )}

          {status === 'success' && (
            <div className="space-y-4">
              {channelId != null && (
                <div className="space-y-1">
                  <Label>{t('imbot.onboarding.channelIdLabel')}</Label>
                  <p className="text-xs text-muted-foreground">{t('imbot.onboarding.channelIdHint')}</p>
                  <div className="flex items-center gap-2">
                    <Input
                      readOnly
                      value={String(channelId)}
                      className="font-mono text-sm"
                    />
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => void handleCopyChannelId()}
                      title={t('imbot.copied')}
                    >
                      <Copy className="h-4 w-4" />
                    </Button>
                  </div>
                  {idCopyStatus === 'copied' && (
                    <p className="text-xs text-success">{t('imbot.copied')}</p>
                  )}
                  {idCopyStatus === 'failed' && (
                    <p className="text-xs text-destructive">{t('imbot.copyFailed')}</p>
                  )}
                </div>
              )}
              {webhookCallbackUrl ? (
                <div className="space-y-2">
                  <p className="text-sm text-success">{t('imbot.onboarding.successMessage')}</p>
                  <div className="space-y-1">
                    <Label>{t('imbot.onboarding.webhookUrlLabel')}</Label>
                    <p className="text-xs text-muted-foreground">{t('imbot.onboarding.webhookUrlHint')}</p>
                    <div className="flex items-center gap-2">
                      <Input
                        readOnly
                        value={webhookCallbackUrl}
                        className="font-mono text-xs"
                      />
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => void handleCopy()}
                        title={t('imbot.copied')}
                      >
                        <Copy className="h-4 w-4" />
                      </Button>
                    </div>
                    {copyStatus === 'copied' && (
                      <p className="text-xs text-success">{t('imbot.copied')}</p>
                    )}
                    {copyStatus === 'failed' && (
                      <p className="text-xs text-destructive">{t('imbot.copyFailed')}</p>
                    )}
                  </div>
                </div>
              ) : (
                <p className="text-sm text-success">{t('imbot.onboarding.successMessage')}</p>
              )}
              <Button type="button" variant="outline" className="w-full" onClick={goBack}>
                {t('imbot.onboarding.backToChat')}
              </Button>
            </div>
          )}

          {status !== 'success' &&
            status !== 'expired' &&
            status !== 'loading' &&
            (info?.platform === 'wechat' ? (
              renderWechat()
            ) : (
              <form onSubmit={(e) => void handleSubmit(e)} className="space-y-4">
                <div className="space-y-1">
                  <Label htmlFor="bot-name">{t('imbot.onboarding.nameLabel')}</Label>
                  <Input
                    id="bot-name"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder={t('imbot.onboarding.namePlaceholder')}
                  />
                  <p className="text-xs text-muted-foreground">{t('imbot.onboarding.nameHint')}</p>
                </div>
                {renderFields()}
                <Button type="submit" className="w-full" disabled={status === 'submitting'}>
                  {status === 'submitting'
                    ? t('imbot.onboarding.submitting')
                    : t('imbot.onboarding.submit')}
                </Button>
              </form>
            ))}
        </CardContent>
      </Card>
    </div>
  );
}
