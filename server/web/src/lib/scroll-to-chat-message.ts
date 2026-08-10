// Shared "jump to a chat message" helper. Chat-flow messages render with a
// DOM id of `msg-<messageId>` (see chat-message.tsx). Both the chat input's
// task-click handler and the pin-message panel use this to scroll a message
// into view and flash a ring highlight.
//
// Returns true when the target element exists, false when it isn't currently
// in the DOM (e.g. an older message not yet loaded by pagination) so the
// caller can surface a hint.
export function scrollToChatMessage(messageId: string): boolean {
  const el = document.getElementById(`msg-${messageId}`);
  if (!el) return false;
  el.scrollIntoView({ behavior: 'smooth', block: 'center' });
  el.classList.add('ring-2', 'ring-ring', 'ring-offset-2');
  setTimeout(() => el.classList.remove('ring-2', 'ring-ring', 'ring-offset-2'), 2000);
  return true;
}
