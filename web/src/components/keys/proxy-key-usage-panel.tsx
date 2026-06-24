import { Suspense, lazy, useMemo, useState } from "react";

import { CopyButton } from "../ui/copy-button";
import { assistantPrompt, codeExamples, scrubProxyKeyFromText } from "../../lib/code-examples";
import type { Provider } from "../../types";
import { N8nSetupGuidePanel } from "./n8n-setup-guide";

const CodeBlock = lazy(() =>
  import("../ui/code-block").then((m) => ({ default: m.CodeBlock })),
);

const CODE_SECRET_WARNING =
  "Don't paste this code into Cursor, Replit, or any other AI chat — it contains your real proxy key. Use the prompt below instead.";

function CodeBlockFallback({ code }: { code: string }) {
  return (
    <pre className="overflow-x-auto whitespace-pre px-5 py-4 font-mono text-sm text-[#abb2bf]">{code}</pre>
  );
}

export interface ProxyKeyUsagePanelProps {
  provider: Provider;
  baseUrl: string;
  proxyKey: string;
  /** When true, omit outer glass panels (e.g. nested inside key detail tabs). */
  embedded?: boolean;
}

export function ProxyKeyUsagePanel({ provider, baseUrl, proxyKey, embedded = false }: ProxyKeyUsagePanelProps) {
  const [activeTab, setActiveTab] = useState(0);

  const examples = useMemo(
    () => codeExamples({ provider, baseUrl, key: proxyKey }),
    [provider, baseUrl, proxyKey],
  );
  const prompt = useMemo(() => {
    const raw = assistantPrompt({ provider, baseUrl });
    return scrubProxyKeyFromText(raw, proxyKey);
  }, [provider, baseUrl, proxyKey]);

  const n8nTabIndex = examples.length;
  const isN8nTab = activeTab === n8nTabIndex;
  const active = !isN8nTab ? (examples[activeTab] ?? examples[0]) : null;
  const usageSectionClass = embedded ? "space-y-3" : "glass-panel overflow-hidden";
  const promptSectionClass = embedded ? "space-y-3" : "glass-panel overflow-hidden";

  return (
    <div className={embedded ? "space-y-4" : "space-y-6"}>
      <section className={usageSectionClass}>
        <div className={embedded ? "space-y-1" : "border-b border-base-300/70 px-6 py-4"}>
          <h2 className={embedded ? "text-sm font-semibold" : "font-semibold"}>Drop-in usage</h2>
          <p className="text-sm text-base-content/60">
            {isN8nTab
              ? "Point n8n at the proxy with a custom credential host — same models, same request shape."
              : `Point your existing ${provider} SDK at the proxy — same models, same request shape.`}
          </p>
        </div>

        <div role="tablist" className={`tabs tabs-bordered ${embedded ? "px-0 pt-1" : "px-4 pt-3"}`}>
          {examples.map((ex, i) => (
            <button
              key={ex.id}
              role="tab"
              type="button"
              aria-selected={i === activeTab}
              className={`tab ${i === activeTab ? "tab-active font-medium" : ""}`}
              onClick={() => setActiveTab(i)}
            >
              {ex.label}
            </button>
          ))}
          <button
            key="n8n"
            role="tab"
            type="button"
            aria-selected={isN8nTab}
            className={`tab ${isN8nTab ? "tab-active font-medium" : ""}`}
            onClick={() => setActiveTab(n8nTabIndex)}
          >
            n8n
          </button>
        </div>

        {isN8nTab ? (
          <div className={embedded ? "pt-2" : "p-4"}>
            <N8nSetupGuidePanel
              provider={provider}
              baseUrl={baseUrl}
              embedded={embedded}
              tabContent
            />
          </div>
        ) : active ? (
          <div className={embedded ? "space-y-3 pt-2" : "space-y-3 p-4"}>
            <div
              role="alert"
              className="alert alert-error animate-code-secret-warning text-sm font-medium"
            >
              <span>{CODE_SECRET_WARNING}</span>
            </div>
            <div className="relative rounded-xl bg-[#282c34] p-3 pt-11">
              <div className="absolute right-3 top-3 z-10">
                <CopyButton
                  value={active.code}
                  label="Copy"
                  className="btn btn-sm btn-primary gap-2 px-3 shadow-md"
                />
              </div>
              <div className="overflow-x-auto text-sm">
                <Suspense fallback={<CodeBlockFallback code={active.code} />}>
                  <CodeBlock code={active.code} language={active.language} />
                </Suspense>
              </div>
            </div>
          </div>
        ) : null}
      </section>

      <section className={promptSectionClass}>
        <div
          className={
            embedded
              ? "flex items-start justify-between gap-3"
              : "flex items-center justify-between border-b border-base-300/70 px-6 py-4"
          }
        >
          <div>
            <h2 className={embedded ? "text-sm font-semibold" : "font-semibold"}>Prompt for Cursor / Replit / n8n / etc.</h2>
            <p className="text-sm text-base-content/60">
              Safe to paste into an AI assistant — uses a placeholder for the key, not your real secret. Copy the key
              from above separately when the assistant asks.
            </p>
          </div>
          <CopyButton value={prompt} label="Copy prompt" />
        </div>
        <pre
          className={`overflow-x-auto whitespace-pre-wrap text-sm text-base-content/80 ${embedded ? "rounded-lg bg-base-300 px-4 py-3" : "px-6 py-4"
            }`}
        >
          {prompt}
        </pre>
      </section>
    </div>
  );
}
