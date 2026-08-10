const STORAGE_KEY_PREFIX = 'niuniu:chat-draft:';

export function saveDraft(workspaceId: string | number, text: string) {
  try {
    const key = STORAGE_KEY_PREFIX + workspaceId;
    if (text) {
      sessionStorage.setItem(key, text);
    } else {
      sessionStorage.removeItem(key);
    }
  } catch (_e) {
    // localStorage may be unavailable (private browsing mode, quota exceeded) — ignore
  }
}

export function loadDraft(workspaceId: string | number): string {
  try {
    return sessionStorage.getItem(STORAGE_KEY_PREFIX + workspaceId) ?? '';
  } catch {
    return '';
  }
}

export function clearDraft(workspaceId: string | number) {
  try {
    sessionStorage.removeItem(STORAGE_KEY_PREFIX + workspaceId);
  } catch (_e) {
    // localStorage may be unavailable (private browsing mode, quota exceeded) — ignore
  }
}
