// Promise-based confirmation dialog state.
//
// Replaces the native window.confirm(), which is unresponsive on iPad/iOS
// Safari (the synchronous dialog is silently suppressed there). Usage:
//
//   import { confirm } from '@/lib/confirm'
//   if (!(await confirm(t('foo.deleteConfirm')))) return
//   if (await confirm({ description: msg, destructive: true })) { ... }
//
// A single <ConfirmHost /> (see ./confirm-host) mounted at the app root renders
// the active request; confirm() resolves to true on the action button, false on
// cancel/dismiss. The store lives in this component-free module so the host file
// can satisfy react-refresh/only-export-components.

export type ConfirmOptions = {
  /** Body text. Newlines render as line breaks. */
  description: string
  /** Heading; defaults to the localized "确认" when omitted. */
  title?: string
  /** Action button label; defaults to the localized "确定". */
  confirmText?: string
  /** Cancel button label; defaults to the localized "取消". */
  cancelText?: string
  /** Style the action button as a destructive (red) action. */
  destructive?: boolean
}

export type ConfirmRequest = ConfirmOptions & { resolve: (ok: boolean) => void }

let current: ConfirmRequest | null = null
const listeners = new Set<() => void>()

function emit() {
  for (const l of listeners) l()
}

/** Subscribe to active-request changes (for useSyncExternalStore). */
export function subscribeConfirm(l: () => void) {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

/** Current active request, or null (for useSyncExternalStore). */
export function getConfirmSnapshot() {
  return current
}

/** Open the shared confirmation dialog. Resolves true if confirmed. */
export function confirm(options: ConfirmOptions | string): Promise<boolean> {
  const opts = typeof options === 'string' ? { description: options } : options
  return new Promise<boolean>((resolve) => {
    current = { ...opts, resolve }
    emit()
  })
}

/** Resolve and clear the active request (called by the host's buttons). */
export function settleConfirm(ok: boolean) {
  const req = current
  current = null
  emit()
  req?.resolve(ok)
}
