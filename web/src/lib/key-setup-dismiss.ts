const PREFIX = "llm-proxy:key-setup-dismissed:";

export function isKeySetupDismissed(proxyKey: string): boolean {
  try {
    return localStorage.getItem(PREFIX + proxyKey) === "1";
  } catch {
    return false;
  }
}

export function dismissKeySetup(proxyKey: string): void {
  try {
    localStorage.setItem(PREFIX + proxyKey, "1");
  } catch {
    // ignore quota / private browsing
  }
}
