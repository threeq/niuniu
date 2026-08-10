// Cross-platform clipboard copy used by the chat message actions.
//
// The desktop editions wrap this SPA in a Wails webview on Windows, macOS and
// Linux, and LAN access can serve the SPA over plain http where the async
// Clipboard API is unavailable (it requires a secure context). We try
// navigator.clipboard first and fall back to a hidden-textarea +
// document.execCommand('copy'), which works in every webview regardless of
// secure-context status. Returns whether the copy succeeded so callers can
// surface a failure toast.
export async function copyTextToClipboard(text: string): Promise<boolean> {
  try {
    if (window.isSecureContext && navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // Permission denied / not allowed in this context — fall through to the
    // execCommand path below.
  }
  return legacyCopy(text);
}

function legacyCopy(text: string): boolean {
  try {
    const textarea = document.createElement('textarea');
    textarea.value = text;
    // Keep it out of view and out of the layout/scroll flow while still
    // selectable. `readonly` avoids the soft keyboard popping on touch hosts.
    textarea.setAttribute('readonly', '');
    textarea.style.position = 'fixed';
    textarea.style.top = '0';
    textarea.style.left = '0';
    textarea.style.opacity = '0';
    document.body.appendChild(textarea);
    textarea.select();
    textarea.setSelectionRange(0, textarea.value.length);
    const ok = document.execCommand('copy');
    document.body.removeChild(textarea);
    return ok;
  } catch {
    return false;
  }
}
