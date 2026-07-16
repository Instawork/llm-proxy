import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import { ProxyKeyUsagePanel } from "../../components/keys/proxy-key-usage-panel";
import LLMProxyLogo from "../../components/llm-proxy-logo";
import { CopyButton } from "../../components/ui/copy-button";
import { maskKey } from "../../components/ui/masked-key";
import {
  ErrorAlert,
  LoadingBlock,
  ProviderBadge,
} from "../../components/ui/page-header";
import { useKey } from "../../hooks/queries";
import {
  decodeKeyRouteParam,
  isKeyRouteParam,
  isMaskedKeyRouteParam,
  isProxyKey,
  keyDetailPath,
  keySetupPath,
} from "../../lib/key-routes";

function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <div className="text-xs font-medium uppercase tracking-wide text-base-content/50">{label}</div>
      <div className="flex items-center gap-2">{children}</div>
    </div>
  );
}

export default function KeySetupPage() {
  const navigate = useNavigate();
  const { key: keyParam } = useParams<{ key: string }>();
  const routeKeyId = keyParam ? decodeKeyRouteParam(keyParam) : undefined;
  const validRoute = isKeyRouteParam(routeKeyId) ? routeKeyId : undefined;
  const keyQuery = useKey(validRoute);
  const [revealed, setRevealed] = useState(false);

  useEffect(() => {
    if (!routeKeyId || !isProxyKey(routeKeyId) || isMaskedKeyRouteParam(routeKeyId)) return;
    navigate(keySetupPath(routeKeyId), { replace: true });
  }, [routeKeyId, navigate]);

  if (!routeKeyId || !validRoute) {
    return (
      <div className="space-y-4">
        <Link to="/keys" className="link link-hover text-sm text-base-content/60">
          ← My API Keys
        </Link>
        <ErrorAlert message="Invalid key link — open How to use from your API Keys list." />
      </div>
    );
  }

  if (keyQuery.isPending) {
    return <LoadingBlock />;
  }

  const data = keyQuery.data;
  if (keyQuery.error || !data) {
    return (
      <div className="space-y-4">
        <Link to="/keys" className="link link-hover text-sm text-base-content/60">
          ← My API Keys
        </Link>
        <ErrorAlert
          message={
            keyQuery.error instanceof Error
              ? keyQuery.error.message
              : "Key not found or you do not have access."
          }
        />
      </div>
    );
  }

  const baseUrl = data.base_url ?? "";

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <div className="text-sm">
        <Link to="/keys" className="link link-hover text-base-content/60">
          ← My API Keys
        </Link>
        <span className="mx-2 text-base-content/30">·</span>
        <Link to={keyDetailPath(data.key)} className="link link-hover text-base-content/60">
          Key details
        </Link>
      </div>

      <header className="flex items-start gap-4">
        <LLMProxyLogo size="lg" />
        <div className="min-w-0 space-y-2">
          <h1 className="text-2xl font-bold">How to use this LLM Proxy key</h1>
          <p className="text-sm text-base-content/70">
            This is an <span className="font-medium">llm-proxy</span> credential (
            <code className="rounded bg-base-200 px-1 text-xs">sk-iw-*</code>), not an OpenAI,
            Anthropic, Google, or AWS API key. Put it in your SDK&apos;s API key field and point the
            client base URL at the proxy. Upstream traffic still goes to{" "}
            <ProviderBadge provider={data.provider} /> — the proxy applies spend caps, rate limits,
            and PII redaction.
          </p>
        </div>
      </header>

      <section className="glass-panel space-y-5 p-6">
        <Field label="Proxy API key (sk-iw-*)">
          <code className="flex-1 truncate rounded-lg bg-base-200/70 px-3 py-2 font-mono text-sm">
            {revealed ? data.key : maskKey(data.key)}
          </code>
          <button
            type="button"
            className="btn btn-sm btn-ghost"
            onClick={() => setRevealed((v) => !v)}
          >
            {revealed ? "Hide" : "Reveal"}
          </button>
          <CopyButton value={data.key} label="Copy key" />
        </Field>

        <div className="grid gap-5 sm:grid-cols-2">
          <Field label={`Proxy base URL (${data.provider})`}>
            <code className="flex-1 truncate rounded-lg bg-base-200/70 px-3 py-2 font-mono text-sm">
              {baseUrl || "—"}
            </code>
            {baseUrl ? (
              <CopyButton value={baseUrl} label="Copy" className="btn btn-sm btn-ghost gap-2" />
            ) : null}
          </Field>
          <Field label="Upstream route">
            <div className="flex flex-wrap items-center gap-2">
              <ProviderBadge provider={data.provider} />
              <span className="text-xs text-base-content/50">via llm-proxy</span>
            </div>
          </Field>
        </div>

        {data.description ? (
          <p className="text-sm text-base-content/60">{data.description}</p>
        ) : null}

        <div className="rounded-lg bg-warning/10 px-4 py-3 text-sm text-warning-content/80">
          Personal keys are for local testing only — do not deploy with them. Treat the proxy key
          like a password; store it in an environment variable, never in git.
        </div>
      </section>

      {baseUrl ? (
        <ProxyKeyUsagePanel provider={data.provider} baseUrl={baseUrl} proxyKey={data.key} />
      ) : (
        <div className="glass-panel p-6 text-sm text-base-content/60">
          Proxy base URL is unavailable — refresh the page or open this key from the API Keys list.
        </div>
      )}
    </div>
  );
}
