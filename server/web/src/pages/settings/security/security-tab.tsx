import { useTranslation } from 'react-i18next'
import LoginHistory from './login-history'
import { MfaSection } from './mfa-section'

export function SecurityTab() {
  const { t } = useTranslation('settings')

  return (
    <div className="space-y-8 pt-6">
      {/* MFA section */}
      <MfaSection />

      {/* Login history section */}
      <section>
        <h2 className="text-lg font-semibold text-warm-text mb-4">
          {t('security.loginHistory.title')}
        </h2>
        <LoginHistory />
      </section>
    </div>
  )
}
