import { Suspense, lazy, useMemo, useState } from "react";
import { useParams } from "react-router-dom";

import LLMProxyLogo from "../components/llm-proxy-logo";
import { CopyButton } from "../components/ui/copy-button";
import { maskKey } from "../components/ui/masked-key";
import { ProviderBadge } from "../components/ui/page-header";
import { useShare } from "../hooks/queries";
import { assistantPrompt, codeExamples, scrubProxyKeyFromText } from "../lib/code-examples";
import { formatShareExpiry } from "../lib/share-expiry";

const CodeBlock = lazy(() =>
  import("../components/ui/code-block").then((m) => ({ default: m.CodeBlock })),
);

function CodeBlockFallback({ code }: { code: string }) {
  return (
    <pre className="overflow-x-auto whitespace-pre px-5 py-4 font-mono text-sm text-[#abb2bf]">{code}</pre>
  );
}

function Field({
  label,
  value,
  mono = true,
  children,
}: {
  label: string;
  value?: string;
  mono?: boolean;
  children?: React.ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <div className="text-xs font-medium uppercase tracking-wide text-base-content/50">{label}</div>
      <div className="flex items-center gap-2">
        {children ?? (
          <code className={`flex-1 truncate rounded-lg bg-base-200/70 px-3 py-2 ${mono ? "font-mono" : ""} text-sm`}>
            {value}
          </code>
        )}
      </div>
    </div>
  );
}

export default function SharePage() {
  const { id } = useParams<{ id: string }>();
  const { data, isLoading, error } = useShare(id);
  const [revealed, setRevealed] = useState(false);
  const [activeTab, setActiveTab] = useState(0);

  const examples = useMemo(
    () =>
      data ? codeExamples({ provider: data.provider, baseUrl: data.base_url, key: data.key }) : [],
    [data],
  );
  const prompt = useMemo(() => {
    if (!data) return "";
    const raw = assistantPrompt({ provider: data.provider, baseUrl: data.base_url });
    return scrubProxyKeyFromText(raw, data.key);
  }, [data]);

  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <span className="loading loading-spinner loading-lg text-primary" />
      </div>
    );
  }

  if (error || !data) {
    return (
      <div className="flex min-h-screen items-center justify-center p-6">
        <div className="glass-panel max-w-md space-y-3 p-8 text-center">
          <LLMProxyLogo size="lg" className="mx-auto" />
          <h1 className="text-xl font-semibold">Link not found</h1>
          <p className="text-sm text-base-content/60">
            This share link is invalid, has been revoked, or has expired. Ask whoever sent it for a
            fresh link.
          </p>
        </div>
      </div>
    );
  }

  const active = examples[activeTab] ?? examples[0];
  const expiry = data.expires_at ? formatShareExpiry(data.expires_at) : null;

  return (
    <div className="min-h-screen bg-base-200/40">
      <div className="mx-auto max-w-3xl space-y-6 px-4 py-10">
        <header className="flex items-center gap-4">
          <LLMProxyLogo size="lg" />
          <div>
            <h1 className="text-2xl font-bold">Your LLM Proxy key</h1>
            <p className="text-sm text-base-content/60">
              Use this key to route <ProviderBadge provider={data.provider} /> requests through the
              LLM proxy — cost limits, rate limits, and PII redaction are applied automatically.
            </p>
          </div>
        </header>

        {expiry ? (
          <div
            className={`rounded-lg px-4 py-3 text-sm ${expiry.urgent
              ? "bg-warning/15 text-warning-content"
              : "bg-info/10 text-base-content/70"
              }`}
          >
            {expiry.message}. After that, whoever shared it will need to generate a new link.
          </div>
        ) : null}

        <section className="glass-panel space-y-5 p-6">
          <Field label="API key">
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
            <Field label={`Base URL (${data.provider})`}>
              <code className="flex-1 truncate rounded-lg bg-base-200/70 px-3 py-2 font-mono text-sm">
                {data.base_url}
              </code>
              <CopyButton value={data.base_url} label="Copy" className="btn btn-sm btn-ghost gap-2" />
            </Field>
            <Field label="Provider" mono={false} value={data.provider} />
          </div>

          {data.description ? (
            <p className="text-sm text-base-content/60">{data.description}</p>
          ) : null}

          <div className="rounded-lg bg-warning/10 px-4 py-3 text-sm text-warning-content/80">
            Treat this key like a password. Store it in an environment variable or secret manager —
            never commit it to source control.
          </div>
        </section>

        <section className="glass-panel overflow-hidden">
          <div className="border-b border-base-300/70 px-6 py-4">
            <h2 className="font-semibold">Drop-in usage</h2>
            <p className="text-sm text-base-content/60">
              Point your existing {data.provider} SDK at the proxy — same models, same request shape.
            </p>
          </div>

          <div role="tablist" className="tabs tabs-bordered px-4 pt-3">
            {examples.map((ex, i) => (
              <button
                key={ex.id}
                role="tab"
                type="button"
                className={`tab ${i === activeTab ? "tab-active font-medium" : ""}`}
                onClick={() => setActiveTab(i)}
              >
                {ex.label}
              </button>
            ))}
          </div>

          {active ? (
            <div className="relative p-4">
              <div className="absolute right-6 top-6 z-10">
                <CopyButton value={active.code} label="Copy" className="btn btn-xs btn-neutral gap-1.5" />
              </div>
              <div className="overflow-x-auto rounded-xl bg-[#282c34] px-1 py-1 text-sm">
                <Suspense fallback={<CodeBlockFallback code={active.code} />}>
                  <CodeBlock code={active.code} language={active.language} />
                </Suspense>
              </div>
            </div>
          ) : null}
        </section>

        <section className="glass-panel overflow-hidden">
          <div className="flex items-center justify-between border-b border-base-300/70 px-6 py-4">
            <div>
              <h2 className="font-semibold">Prompt for Cursor / Replit</h2>
              <p className="text-sm text-base-content/60">
                Safe to paste into an AI assistant — uses a placeholder for the key, not your real secret.
                Copy the key from the field above separately when the assistant asks.
              </p>
            </div>
            <CopyButton value={prompt} label="Copy prompt" />
          </div>
          <pre className="overflow-x-auto whitespace-pre-wrap px-6 py-4 text-sm text-base-content/80">
            {prompt}
          </pre>
        </section>

        <footer className="pb-6 text-center text-xs text-base-content/40">
          Shared via the LLM Proxy admin dashboard
          {data.created_by ? ` · by ${data.created_by}` : ""}
        </footer>
      </div>
    </div>
  );
}
