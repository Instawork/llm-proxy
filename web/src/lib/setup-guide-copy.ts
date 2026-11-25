/** Warning above the code snippet. Shared keys use a placeholder credential, never a real one. */
export function snippetWarning(sharedKey: boolean): string {
  if (sharedKey) {
    return "This snippet uses the placeholder YOUR_PROXY_KEY — this key is shared read-only, so its credential isn't shown here. Replace the placeholder with the key from whoever holds it, and never paste a real key into Cursor, Replit, or any other AI chat.";
  }
  return "Don't paste this code into Cursor, Replit, or any other AI chat — it contains your real proxy key. Use the prompt below instead.";
}

/** Hint under the assistant-prompt heading. */
export function promptHint(sharedKey: boolean): string {
  if (sharedKey) {
    return "Safe to paste into an AI assistant — uses a placeholder for the key. Get the real key from whoever holds this shared key when the assistant asks.";
  }
  return "Safe to paste into an AI assistant — uses a placeholder for the key, not your real secret. Copy the key from above separately when the assistant asks.";
}
