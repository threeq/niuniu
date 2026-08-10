// Passwordless email-code login screen.
//
// Two stages, both rendered in the same screen so the back-end can roll a
// 60s resend cooldown without losing the typed-in email:
//   stage 'email' — user enters address, taps Continue, we call
//       requestLoginCode(...) and advance to stage 'code'
//   stage 'code'  — CodeInput + auto-verify on 6 digits + 60s resend
//       countdown + 改邮箱 (back to stage 'email')
//
// Stage transitions go through `switchStage`, which fades the inner
// Animated.View to opacity 0 over 200ms, swaps `stage`, then fades back
// to opacity 1 — keeps both `email`/`code` blocks rendered behind the
// same animated wrapper so the cross-fade actually has something to hand
// off to. Direct `setStage(...)` is reserved for "no animation" use; in
// this file every transition is a `switchStage`.
//
// Error contract: api/relay/client.ts throws Error(body.error), so
// `err.message` is the bare backend error code string. Code-stage handler
// switches on it: expired_code / too_many_attempts / invalid_code render
// inline (under the cells); anything else is treated as a network/500
// failure and surfaces via <InlineToast> at the top of the screen
// (mounted inside KeyboardAvoidingView, *before* `content`, so it floats
// above without pushing the form layout — M6 fix).
//
// "使用自托管 Relay" caption acts as an inline show/hide toggle for the
// self-host URL editor (default points at https://niuniu-relay.niu6ai.com).
// Hidden by default; tapping the link expands a URL TextInput directly
// under the link in the form flow. There is no separate Save button —
// the URL is auto-committed to relayAccountStore by `handleContinue`
// when the user taps Continue (and that same `effectiveUrl` is used for
// the in-flight `requestLoginCode` call to keep verify consistent).

import { useEffect, useRef, useState } from 'react';
import {
  Animated,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  View,
} from 'react-native';
import { router } from 'expo-router';
import { useTranslation } from 'react-i18next';
import { CodeInput } from '../../src/components/CodeInput';
import { InlineToast } from '../../src/components/InlineToast';
import { useRelayAccountStore } from '../../src/stores/relayAccountStore';
import { useThemeColors } from '../../src/theme/useTheme';
import { radius, spacing, typography } from '../../src/theme/tokens';

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const DEFAULT_RELAY_URL = 'https://niuniu-relay.niu6ai.com';

type Stage = 'email' | 'code';

export default function AuthEmailScreen() {
  const { t } = useTranslation();
  const colors = useThemeColors();
  const relayBaseUrl = useRelayAccountStore((s) => s.relayBaseUrl) ?? DEFAULT_RELAY_URL;
  const requestLoginCode = useRelayAccountStore((s) => s.requestLoginCode);
  const loginWithCode = useRelayAccountStore((s) => s.loginWithCode);
  const setRelayBaseUrl = useRelayAccountStore((s) => s.setRelayBaseUrl);

  const [stage, setStage] = useState<Stage>('email');
  const [email, setEmail] = useState('');
  const [emailError, setEmailError] = useState<string | null>(null);
  const [requesting, setRequesting] = useState(false);

  // Code stage state.
  const [code, setCode] = useState('');
  const [codeError, setCodeError] = useState<string | null>(null);
  const [verifying, setVerifying] = useState(false);
  const [resendIn, setResendIn] = useState(60);

  // Top-of-screen network toast state (rendered by <InlineToast/> inside
  // the KeyboardAvoidingView). Inline cell-level errors are reserved for
  // recognized backend code states (expired/invalid/too-many).
  const [networkError, setNetworkError] = useState<string | null>(null);

  // Self-host inline editor — hidden by default, toggled by the caption
  // link below the Continue button.
  const [showSelfHost, setShowSelfHost] = useState(false);
  const [selfHostUrl, setSelfHostUrl] = useState(relayBaseUrl);

  // Stage cross-fade. 200ms each direction; the closing fade's `start`
  // callback flips `stage` and kicks the opening fade so the whole swap
  // is one continuous gesture.
  const stageOpacity = useRef(new Animated.Value(1)).current;
  const switchStage = (next: Stage) => {
    Animated.timing(stageOpacity, {
      toValue: 0,
      duration: 200,
      useNativeDriver: true,
    }).start(() => {
      setStage(next);
      Animated.timing(stageOpacity, {
        toValue: 1,
        duration: 200,
        useNativeDriver: true,
      }).start();
    });
  };

  const emailValid = EMAIL_RE.test(email.trim());

  // Reset countdown each time we enter the code stage.
  useEffect(() => {
    if (stage !== 'code') return;
    setResendIn(60);
    const id = setInterval(() => {
      setResendIn((s) => (s > 0 ? s - 1 : 0));
    }, 1000);
    return () => clearInterval(id);
  }, [stage]);

  const handleContinue = async () => {
    if (!emailValid) {
      setEmailError(t('authEmail.error.invalidEmail'));
      return;
    }
    setEmailError(null);

    // If the inline self-host editor is expanded, commit its URL to the
    // store before issuing the request so verify (and any subsequent
    // resend) hit the same relay. Empty input falls back to the default.
    // setRelayBaseUrl is async w.r.t. this render scope, so we use the
    // freshly-computed value directly for requestLoginCode rather than
    // relying on the next-render `relayBaseUrl`.
    const effectiveUrl = showSelfHost
      ? (selfHostUrl.trim() || DEFAULT_RELAY_URL)
      : relayBaseUrl;
    if (showSelfHost && effectiveUrl !== relayBaseUrl) {
      setRelayBaseUrl(effectiveUrl);
    }

    setRequesting(true);
    try {
      await requestLoginCode(effectiveUrl, email.trim());
      switchStage('code');
    } catch (err: any) {
      setEmailError(err?.message ?? t('authEmail.error.network'));
    } finally {
      setRequesting(false);
    }
  };

  const handleVerify = async (codeToVerify: string) => {
    setVerifying(true);
    setCodeError(null);
    try {
      await loginWithCode(relayBaseUrl, email.trim(), codeToVerify);
      // server.tsx (router/dispatcher) handles next destination based on
      // local pairedDesktopsStore, so we just go back to it.
      router.replace('/(auth)/server');
    } catch (err: any) {
      // err.message is the backend error code string (api/relay/client.ts
      // throws Error(body.error)). Map to localized text.
      const msg: string = err?.message ?? '';
      switch (msg) {
        case 'expired_code':
          setCodeError(t('authEmail.error.expiredCode'));
          setResendIn(0);
          break;
        case 'too_many_attempts':
          setCodeError(t('authEmail.error.tooManyAttempts'));
          setResendIn(0); // user must request a new code
          break;
        case 'invalid_code':
          setCodeError(t('authEmail.error.invalidCode'));
          break;
        default:
          // Network / 500 / unknown — show as toast (top), keep stage.
          setNetworkError(t('authEmail.error.network'));
      }
      setCode(''); // clear so user can retry
    } finally {
      setVerifying(false);
    }
  };

  const handleResend = async () => {
    setCodeError(null);
    setRequesting(true);
    try {
      await requestLoginCode(relayBaseUrl, email.trim());
      setResendIn(60);
    } catch (err: any) {
      setNetworkError(err?.message ?? t('authEmail.error.network'));
    } finally {
      setRequesting(false);
    }
  };

  return (
    <KeyboardAvoidingView
      style={[styles.container, { backgroundColor: colors.bg.base }]}
      behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
    >
      <InlineToast
        message={networkError}
        onDismiss={() => setNetworkError(null)}
      />
      <View style={styles.content}>
        <View style={styles.header}>
          <View style={[styles.logoBox, { backgroundColor: '#000000' }]}>
            <Text style={styles.logoLetter}>N</Text>
          </View>
          <Text style={[styles.title, { color: colors.text.primary }]}>
            {t('authEmail.welcome')}
          </Text>
          <Text style={[styles.subtitle, { color: colors.text.secondary }]}>
            {t('authEmail.tagline')}
          </Text>
        </View>

        <Animated.View style={{ opacity: stageOpacity }}>
          {stage === 'email' && (
            <View>
              <TextInput
                style={[
                  styles.input,
                  {
                    backgroundColor: colors.bg.base,
                    borderColor: emailError ? colors.status.error : colors.border.default,
                    color: colors.text.primary,
                  },
                ]}
                placeholder={t('authEmail.emailPlaceholder')}
                placeholderTextColor={colors.text.tertiary}
                value={email}
                onChangeText={(next) => {
                  setEmail(next);
                  setEmailError(null);
                }}
                autoCapitalize="none"
                keyboardType="email-address"
                autoComplete="email"
                autoFocus
              />
              {emailError && (
                <Text style={[styles.errorText, { color: colors.status.error }]}>
                  {emailError}
                </Text>
              )}
              <Pressable
                style={[
                  styles.button,
                  {
                    backgroundColor: emailValid ? colors.brand.accent : colors.bg.surface,
                    opacity: requesting ? 0.6 : 1,
                  },
                ]}
                onPress={handleContinue}
                disabled={!emailValid || requesting}
              >
                <Text
                  style={[
                    styles.buttonText,
                    { color: emailValid ? '#fff' : colors.text.tertiary },
                  ]}
                >
                  {requesting ? '...' : t('authEmail.continue')}
                </Text>
              </Pressable>
              <Pressable
                onPress={() => {
                  setShowSelfHost((v) => {
                    const next = !v;
                    // Reset the field to the persisted URL each time we
                    // expand, so a previous edit-then-collapse doesn't
                    // leak stale text into the next open.
                    if (next) setSelfHostUrl(relayBaseUrl);
                    return next;
                  });
                }}
                style={styles.selfHostLink}
                hitSlop={12}
              >
                <Text style={[styles.selfHostText, { color: colors.text.tertiary }]}>
                  {t('authEmail.useSelfHosted')}
                </Text>
              </Pressable>
              {showSelfHost && (
                <View style={styles.selfHostBox}>
                  <TextInput
                    style={[
                      styles.input,
                      {
                        backgroundColor: colors.bg.surface,
                        borderColor: colors.border.default,
                        color: colors.text.primary,
                      },
                    ]}
                    value={selfHostUrl}
                    onChangeText={setSelfHostUrl}
                    placeholder="https://relay.example.com"
                    placeholderTextColor={colors.text.tertiary}
                    autoCapitalize="none"
                    keyboardType="url"
                    autoCorrect={false}
                  />
                </View>
              )}
            </View>
          )}

          {stage === 'code' && (
            <View>
              <Text style={[styles.codeTitle, { color: colors.text.primary }]}>
                {t('authEmail.codeSent')}
              </Text>
              <Pressable
                onPress={() => {
                  switchStage('email');
                  setCode('');
                  setCodeError(null);
                }}
              >
                <Text style={[styles.codeEmail, { color: colors.text.secondary }]}>
                  {email.trim()}  ✏  {t('authEmail.changeEmail')}
                </Text>
              </Pressable>
              <View style={{ marginTop: spacing.xl, marginBottom: spacing.md }}>
                <CodeInput
                  value={code}
                  onChange={setCode}
                  onComplete={handleVerify}
                  hasError={!!codeError}
                />
              </View>
              {codeError && (
                <Text style={[styles.errorText, { color: colors.status.error }]}>
                  {codeError}
                </Text>
              )}
              <Pressable
                disabled={resendIn > 0 || requesting}
                onPress={handleResend}
                style={styles.resendLink}
              >
                <Text
                  style={[
                    styles.resendText,
                    { color: resendIn > 0 ? colors.text.tertiary : colors.brand.accent },
                  ]}
                >
                  {resendIn > 0
                    ? t('authEmail.resendIn', { seconds: resendIn })
                    : t('authEmail.resendNow')}
                </Text>
              </Pressable>
              <Pressable
                style={[
                  styles.button,
                  {
                    backgroundColor: code.length === 6 ? colors.brand.accent : colors.bg.surface,
                    opacity: verifying ? 0.6 : 1,
                  },
                ]}
                onPress={() => handleVerify(code)}
                disabled={code.length !== 6 || verifying}
              >
                <Text
                  style={[
                    styles.buttonText,
                    { color: code.length === 6 ? '#fff' : colors.text.tertiary },
                  ]}
                >
                  {verifying ? '...' : t('authEmail.verifyAndSignIn')}
                </Text>
              </Pressable>
            </View>
          )}
        </Animated.View>
      </View>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1 },
  content: { flex: 1, padding: spacing.lg, paddingTop: 80 },
  header: { alignItems: 'center', marginBottom: spacing['3xl'] },
  logoBox: {
    width: 40,
    height: 40,
    borderRadius: 10,
    alignItems: 'center',
    justifyContent: 'center',
    marginBottom: spacing.lg,
  },
  logoLetter: { fontSize: 22, fontWeight: '700', color: '#6366F1' },
  title: { ...typography.display, marginBottom: spacing.xs },
  subtitle: { ...typography.body, textAlign: 'center' },
  input: {
    borderRadius: radius.sm,
    padding: spacing.md,
    marginBottom: spacing.sm,
    borderWidth: 1,
    ...typography.body,
  },
  errorText: { ...typography.caption, marginBottom: spacing.sm, marginLeft: spacing.xs },
  button: {
    borderRadius: radius.sm,
    padding: spacing.lg,
    alignItems: 'center',
    marginTop: spacing.sm,
  },
  buttonText: { ...typography.bodyMedium },
  selfHostLink: { alignItems: 'center', marginTop: spacing.xl },
  selfHostText: { ...typography.caption },
  selfHostBox: { marginTop: spacing.md },
  codeTitle: { ...typography.sectionHead, textAlign: 'center', marginBottom: spacing.xs },
  codeEmail: { ...typography.body, textAlign: 'center', marginBottom: spacing.lg },
  resendLink: { alignItems: 'center', marginTop: spacing.md, marginBottom: spacing.sm },
  resendText: { ...typography.caption },
});
