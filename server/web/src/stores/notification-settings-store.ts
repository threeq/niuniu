import { create } from 'zustand'
import { persist } from 'zustand/middleware'

export type SoundName = 'cowMooing' | 'ding' | 'chime' | 'bell' | 'horse' | 'cat' | 'chicken' | 'frog' | 'cricket' | 'nightingale' | 'cuckoo' | 'robin' | 'blackbird' | 'owl'

export type AlertScope = 'all' | 'mine' | 'none'

export interface NotificationSettings {
  /** Whether to show toast notifications */
  toastEnabled: boolean
  /** Whether to play sound notifications */
  soundEnabled: boolean
  /** Which sound to play */
  soundName: SoundName
  /** Volume level 0-100 */
  volume: number
  /**
   * Which workspaces trigger toast + sound alerts.
   * - 'all'  — every accessible workspace
   * - 'mine' — workspaces where my user id appears in the event's
   *            should_alert_user_ids (creator OR issue assignee)
   * - 'none' — no toast and no sound for any workspace
   * Cache invalidation (status display in the UI) is unaffected.
   */
  alertScope: AlertScope
}

interface NotificationSettingsStore extends NotificationSettings {
  setToastEnabled: (enabled: boolean) => void
  setSoundEnabled: (enabled: boolean) => void
  setSoundName: (name: SoundName) => void
  setVolume: (volume: number) => void
  setAlertScope: (scope: AlertScope) => void
}

// Display labels are resolved at render time via i18n: t(`common:soundOptions.${value}`).
// Keep the array as `value`-only so this module stays free of translatable
// strings (it's a non-React Zustand store; importing useTranslation here is
// not viable, and i18n.t() at module-load time may run before init).
export const SOUND_OPTIONS: { value: SoundName }[] = [
  { value: 'cowMooing' },
  { value: 'ding' },
  { value: 'chime' },
  { value: 'bell' },
  { value: 'horse' },
  { value: 'cat' },
  { value: 'chicken' },
  { value: 'frog' },
  { value: 'cricket' },
  { value: 'nightingale' },
  { value: 'cuckoo' },
  { value: 'robin' },
  { value: 'blackbird' },
  { value: 'owl' },
]

export const useNotificationSettings = create<NotificationSettingsStore>()(
  persist(
    (set) => ({
      toastEnabled: true,
      soundEnabled: true,
      soundName: 'cowMooing',
      volume: 80,
      alertScope: 'mine',
      setToastEnabled: (enabled) => set({ toastEnabled: enabled }),
      setSoundEnabled: (enabled) => set({ soundEnabled: enabled }),
      setSoundName: (name) => set({ soundName: name }),
      setVolume: (volume) => set({ volume: Math.max(0, Math.min(100, volume)) }),
      setAlertScope: (scope) => set({ alertScope: scope }),
    }),
    { name: 'niuniu-notification-settings' }
  )
)
