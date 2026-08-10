import { lightColors } from '../../theme/tokens';
import {
  getIdBadgeColors,
  hasAnyBgTask,
} from '../../utils/workspaceCardHelpers';
import type { BgTaskAggregateDTO } from '../types';

describe('getIdBadgeColors', () => {
  test('running → success bg + white text', () => {
    const c = getIdBadgeColors('running', lightColors);
    expect(c.bg).toBe(lightColors.status.success);
    expect(c.text).toBe('#FFFFFF');
  });

  test('attention → error bg + white text', () => {
    const c = getIdBadgeColors('attention', lightColors);
    expect(c.bg).toBe(lightColors.status.error);
    expect(c.text).toBe('#FFFFFF');
  });

  test('needs_review → warning bg + white text', () => {
    const c = getIdBadgeColors('needs_review', lightColors);
    expect(c.bg).toBe(lightColors.status.warning);
    expect(c.text).toBe('#FFFFFF');
  });

  test('created → warning bg (mirrors desktop sidebar getStatusDotClass)', () => {
    const c = getIdBadgeColors('created', lightColors);
    expect(c.bg).toBe(lightColors.status.warning);
    expect(c.text).toBe('#FFFFFF');
  });

  test('completed → success bg + white text', () => {
    const c = getIdBadgeColors('completed', lightColors);
    expect(c.bg).toBe(lightColors.status.success);
    expect(c.text).toBe('#FFFFFF');
  });

  test('failed → error bg + white text', () => {
    const c = getIdBadgeColors('failed', lightColors);
    expect(c.bg).toBe(lightColors.status.error);
    expect(c.text).toBe('#FFFFFF');
  });

  test('idle / unknown / undefined → muted bg + secondary text', () => {
    const c = getIdBadgeColors('idle', lightColors);
    expect(c.bg).toBe(lightColors.bg.muted);
    expect(c.text).toBe(lightColors.text.secondary);

    const u = getIdBadgeColors(undefined, lightColors);
    expect(u.bg).toBe(lightColors.bg.muted);
    expect(u.text).toBe(lightColors.text.secondary);
  });
});

describe('hasAnyBgTask', () => {
  const empty: BgTaskAggregateDTO = {
    agent_busy: false,
    bash_count: 0,
    wakeup_count: 0,
    subagent_count: 0,
    cron_count: 0,
  };

  test('undefined → false', () => {
    expect(hasAnyBgTask(undefined)).toBe(false);
  });

  test('all-zero → false', () => {
    expect(hasAnyBgTask(empty)).toBe(false);
  });

  test('agent_busy true → true', () => {
    expect(hasAnyBgTask({ ...empty, agent_busy: true })).toBe(true);
  });

  test('any count > 0 → true', () => {
    expect(hasAnyBgTask({ ...empty, bash_count: 1 })).toBe(true);
    expect(hasAnyBgTask({ ...empty, wakeup_count: 3 })).toBe(true);
    expect(hasAnyBgTask({ ...empty, subagent_count: 1 })).toBe(true);
    expect(hasAnyBgTask({ ...empty, cron_count: 2 })).toBe(true);
  });
});
