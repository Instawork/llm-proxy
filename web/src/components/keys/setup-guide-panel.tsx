import { useState } from "react";

import { dismissKeySetup, isKeySetupDismissed } from "../../lib/key-setup-dismiss";
import type { Provider } from "../../types";
import { ProxyKeyUsagePanel } from "./proxy-key-usage-panel";

const SHARED_KEY_PLACEHOLDER = "YOUR_PROXY_KEY";

/**
 * Secondary, collapsible setup guidance for a key with no traffic yet. It sits below the normal
 * read-only detail so limits and spend stay visible. Shared keys get the same snippets with a
 * placeholder credential, since viewers never receive the key itself.
 */
export default function SetupGuidePanel({
  dismissId,
  provider,
  baseUrl,
  proxyKey,
  shared = false,
}: {
  /** Stable per-key id used to remember a dismissal (the proxy key, or the masked id when shared). */
  dismissId: string;
  provider: Provider;
  baseUrl?: string;
  /** Real proxy key for own keys; omit for shared keys. */
  proxyKey?: string;
  shared?: boolean;
}) {
  const [dismissed, setDismissed] = useState(() => isKeySetupDismissed(dismissId));
  if (dismissed) return null;

  return (
    <details className="glass-panel group" open>
      <summary className="flex cursor-pointer list-none items-center justify-between gap-3 px-5 py-4">
        <span className="font-semibold">Get set up</span>
        <span className="text-xs text-base-content/60">
          No requests yet — <span className="group-open:hidden">show</span>
          <span className="hidden group-open:inline">hide</span> setup guide
        </span>
      </summary>
      <div className="space-y-4 border-t border-base-300/60 p-5">
        <p className="text-sm text-base-content/70">
          {shared
            ? "This key is shared with you read-only, so its credential is not shown here. Ask whoever holds the key to point their SDK at the proxy as below; spend will appear on this page once requests arrive."
            : "It looks like you haven't used this key yet. Point your SDK at the proxy with the snippets below — once we see your first request, usage and spend stats will show up here automatically."}
        </p>
        {baseUrl ? (
          <ProxyKeyUsagePanel
            provider={provider}
            baseUrl={baseUrl}
            proxyKey={proxyKey ?? SHARED_KEY_PLACEHOLDER}
            embedded
            sharedKey={shared}
          />
        ) : (
          <p className="text-sm text-base-content/60">
            SDK base URL is unavailable — refresh the page or open this key from the API Keys list.
          </p>
        )}
        <div className="flex flex-wrap items-center gap-3 border-t border-base-300/60 pt-4">
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            onClick={() => {
              dismissKeySetup(dismissId);
              setDismissed(true);
            }}
          >
            Already set up? Hide this guide
          </button>
        </div>
      </div>
    </details>
  );
}
