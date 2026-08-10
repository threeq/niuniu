import { render, screen } from '@testing-library/react';
import { describe, it, expect } from 'vitest';
import { useTranslation } from 'react-i18next';
import i18n from './index';

// Probe + regression test for `src/i18n/test-setup.ts`. If any of these fail,
// the workaround `vi.mock('react-i18next', ...)` blocks in B0/B1 component
// tests (notification-dispatcher.test.ts, theme-toggle.test.tsx) become
// necessary again. See `test-setup.ts` for the root-cause writeup.
describe('i18n test-setup probe', () => {
  it('i18n.language === "zh-CN"', () => {
    expect(i18n.language).toBe('zh-CN');
  });

  it('i18n.languages includes "zh-CN" (non-empty hierarchy)', () => {
    expect(i18n.languages.length).toBeGreaterThan(0);
    expect(i18n.languages).toContain('zh-CN');
  });

  it('t("common:actions.save") returns the actual Chinese translation', () => {
    expect(i18n.t('common:actions.save')).toBe('保存');
  });

  it('t("common:notifications.agentDone") interpolates {{id}} correctly', () => {
    expect(i18n.t('common:notifications.agentDone', { id: 218 })).toBe(
      'Workspace !218: agent 完成，等待 review',
    );
  });

  it('useTranslation("common").t() works in a rendered component', () => {
    function Probe() {
      const { t } = useTranslation('common');
      return <span data-testid="probe">{t('actions.save')}</span>;
    }
    render(<Probe />);
    expect(screen.getByTestId('probe').textContent).toBe('保存');
  });
});
