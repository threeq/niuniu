import { describe, expect, it, beforeEach, vi } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { dispatchNotification, __resetForTests } from '../notification-dispatcher';
import { useNotificationSettings } from '@/stores/notification-settings-store';
import { useAuthStore } from '@/stores/auth-store';
import * as soundMod from '@/lib/notification-sound';

vi.mock('sonner', () => ({
  toast: {
    info:    vi.fn(),
    success: vi.fn(),
    error:   vi.fn(),
    dismiss: vi.fn(),
  },
}));

describe('notification-dispatcher alert scope gate', () => {
  let qc: QueryClient;

  beforeEach(() => {
    __resetForTests();
    vi.clearAllMocks();
    qc = new QueryClient();
    useNotificationSettings.setState({
      toastEnabled: true,
      soundEnabled: true,
      soundName: 'ding',
      volume: 50,
      alertScope: 'mine',
    });
    useAuthStore.setState({
      user: { id: 7, username: 'me', display_name: 'Me', role: 'member' },
    } as any);
  });

  const ev = (workspaceID: number, alertable: number[]) => ({
    topic: 'workspace',
    action: 'status_changed',
    id: workspaceID,
    extra: { status: 'needs_review', should_alert_user_ids: alertable },
  });

  it("alertScope='mine' + my id IN should_alert_user_ids → toast + sound fire", () => {
    const playSpy = vi.spyOn(soundMod, 'playNotificationSound').mockImplementation(() => Promise.resolve());
    const invSpy  = vi.spyOn(qc, 'invalidateQueries');

    dispatchNotification(qc, ev(42, [7, 99]));

    expect(toast.info).toHaveBeenCalledTimes(1);
    expect(playSpy).toHaveBeenCalledTimes(1);
    expect(invSpy).toHaveBeenCalled();
  });

  it("alertScope='mine' + my id NOT in list → cache invalidated, NO toast, NO sound", () => {
    const playSpy = vi.spyOn(soundMod, 'playNotificationSound').mockImplementation(() => Promise.resolve());
    const invSpy  = vi.spyOn(qc, 'invalidateQueries');

    dispatchNotification(qc, ev(42, [99]));

    expect(toast.info).not.toHaveBeenCalled();
    expect(playSpy).not.toHaveBeenCalled();
    expect(invSpy).toHaveBeenCalled();
  });

  it("alertScope='mine' + missing should_alert_user_ids → NO toast (safe default)", () => {
    const playSpy = vi.spyOn(soundMod, 'playNotificationSound').mockImplementation(() => Promise.resolve());

    dispatchNotification(qc, {
      topic: 'workspace',
      action: 'status_changed',
      id: 42,
      extra: { status: 'needs_review' }, // no should_alert_user_ids
    });

    expect(toast.info).not.toHaveBeenCalled();
    expect(playSpy).not.toHaveBeenCalled();
  });

  it("alertScope='all' → toast + sound regardless of list contents", () => {
    useNotificationSettings.setState({ alertScope: 'all' });
    const playSpy = vi.spyOn(soundMod, 'playNotificationSound').mockImplementation(() => Promise.resolve());

    dispatchNotification(qc, ev(42, [99])); // not in list, but scope=all

    expect(toast.info).toHaveBeenCalledTimes(1);
    expect(playSpy).toHaveBeenCalledTimes(1);
  });

  it("alertScope='none' → no toast, no sound; cache still invalidated", () => {
    useNotificationSettings.setState({ alertScope: 'none' });
    const playSpy = vi.spyOn(soundMod, 'playNotificationSound').mockImplementation(() => Promise.resolve());
    const invSpy  = vi.spyOn(qc, 'invalidateQueries');

    dispatchNotification(qc, ev(42, [7]));

    expect(toast.info).not.toHaveBeenCalled();
    expect(playSpy).not.toHaveBeenCalled();
    expect(invSpy).toHaveBeenCalled();
  });

  it("agent_done is gated identically (single gate covers all workspace toasts)", () => {
    const playSpy = vi.spyOn(soundMod, 'playNotificationSound').mockImplementation(() => Promise.resolve());

    dispatchNotification(qc, {
      topic: 'workspace',
      action: 'agent_done',
      id: 42,
      extra: { status: 'done', should_alert_user_ids: [99] }, // not me
    });

    expect(toast.success).not.toHaveBeenCalled();
    expect(playSpy).not.toHaveBeenCalled();
  });
});
