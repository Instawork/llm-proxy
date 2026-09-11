import { FormEvent, useState } from "react";

import { CostFields, ExpiryField, PiiFields, RateLimitFields } from "./key-form-fields";
import {
  modalTabClass,
  providerNeedsUpstreamKey,
  type KeyFormState,
  type KeyFormTab,
} from "../../lib/key-form";
import type { Provider } from "../../types";
import { ProviderLabel, ProviderSelect } from "../ui/page-header";

type ApiKeysModalProps = {
  open: boolean;
  onClose: () => void;
  onSubmit: (event: FormEvent) => void;
  saving: boolean;
  form: KeyFormState;
  setForm: React.Dispatch<React.SetStateAction<KeyFormState>>;
  isViewer: boolean;
  canManagePolicy: boolean;
  treatAsPersonal: boolean;
  personalMode: boolean;
  onTogglePersonalMode: (on: boolean) => void;
  availableProviders: Provider[];
  provisionedKeysOnly: boolean;
  providerAutoProvision: boolean;
  useAutoProvision: boolean;
  manualKeyEntry: boolean;
  setManualKeyEntry: (open: boolean) => void;
  showAnthropicTierSelect: boolean;
  anthropicTierOptions: string[];
  piiOffRequiresBedrock: boolean;
  viewerMonthlyLimitLabel: string;
  editorMaxDollars: number | null;
};

export default function ApiKeysModal(props: ApiKeysModalProps) {
  if (!props.open) return null;
  return <ApiKeysModalContent {...props} />;
}

// Split into a component that only mounts while open, so `tab` always
// starts fresh on "general" instead of carrying over from the last open.
function ApiKeysModalContent({
  onClose,
  onSubmit,
  saving,
  form,
  setForm,
  isViewer,
  canManagePolicy,
  treatAsPersonal,
  personalMode,
  onTogglePersonalMode,
  availableProviders,
  provisionedKeysOnly,
  providerAutoProvision,
  useAutoProvision,
  manualKeyEntry,
  setManualKeyEntry,
  showAnthropicTierSelect,
  anthropicTierOptions,
  piiOffRequiresBedrock,
  viewerMonthlyLimitLabel,
  editorMaxDollars,
}: Omit<ApiKeysModalProps, "open">) {
  const [tab, setTab] = useState<KeyFormTab>("general");

  const policyTabs: KeyFormTab[] = canManagePolicy && !treatAsPersonal
    ? ["cost", "pii", "rate-limits"]
    : [];
  const visibleTabs: KeyFormTab[] = ["general", ...policyTabs];

  const title = treatAsPersonal ? "Create personal key" : "Create API key";
  const needsUpstreamKey = providerNeedsUpstreamKey(form.provider);

  return (
    <dialog className="modal modal-open" open>
      <div className="modal-box max-w-2xl">
        <h3 className="text-lg font-semibold">{title}</h3>

        {policyTabs.length > 0 ? (
          <div className="mt-4 border-b border-base-300/70 pb-2">
            <div role="tablist" className="flex flex-wrap gap-2" aria-label="Key form sections">
              {visibleTabs.map((item) => (
                <button
                  key={item}
                  type="button"
                  role="tab"
                  aria-selected={tab === item}
                  className={modalTabClass(tab === item)}
                  onClick={() => setTab(item)}
                >
                  {item === "general"
                    ? "General"
                    : item === "cost"
                      ? "Cost"
                      : item === "pii"
                        ? "PII"
                        : "Rate limits"}
                </button>
              ))}
            </div>
          </div>
        ) : null}

        <form className="mt-4 space-y-5" onSubmit={onSubmit}>
          {tab === "general" ? (
            <>
              {!isViewer ? (
                <label className="flex cursor-pointer items-center justify-between gap-3 rounded-lg border border-base-300/70 bg-base-200/40 px-3 py-2">
                  <span className="text-sm">
                    <span className="font-medium">Personal key</span>
                    <span className="block text-xs text-base-content/60">
                      Owned by you, one per provider, capped at {viewerMonthlyLimitLabel}/month.
                    </span>
                  </span>
                  <input
                    type="checkbox"
                    className="toggle toggle-primary"
                    checked={personalMode}
                    onChange={(event) => onTogglePersonalMode(event.target.checked)}
                  />
                </label>
              ) : null}

              <div className="space-y-2">
                <label className="form-control w-full">
                  <span className="label-text mb-1.5">Provider</span>
                  <ProviderSelect
                    value={form.provider}
                    onChange={(value) =>
                      setForm((current) => ({
                        ...current,
                        provider: value as Provider,
                      }))
                    }
                    options={availableProviders}
                  />
                </label>
                {piiOffRequiresBedrock && !treatAsPersonal ? (
                  <p className="text-xs text-base-content/60">
                    PII redaction off requires the Bedrock provider.
                  </p>
                ) : null}
                {!needsUpstreamKey ? (
                  <p className="text-xs text-base-content/60">
                    Proxy key still required · no AWS/Bedrock API key to paste (upstream uses
                    SigV4).
                  </p>
                ) : null}
              </div>

              {useAutoProvision ? (
                <div className="rounded-lg border border-primary/20 bg-primary/5 px-4 py-3 text-sm text-base-content/80">
                  Upstream key will be created automatically for{" "}
                  <ProviderLabel provider={form.provider} />.
                </div>
              ) : null}

              {(provisionedKeysOnly || treatAsPersonal) && !providerAutoProvision && needsUpstreamKey ? (
                <div className="rounded-lg border border-warning/30 bg-warning/10 px-4 py-3 text-sm text-warning">
                  Automatic key provisioning is not available for{" "}
                  <ProviderLabel provider={form.provider} />. Choose another provider or contact an
                  administrator.
                </div>
              ) : null}

              {!treatAsPersonal && showAnthropicTierSelect ? (
                <label className="form-control w-full">
                  <span className="label-text mb-1.5">Anthropic tier</span>
                  <select
                    className="select select-bordered w-full"
                    value={form.anthropic_tier}
                    onChange={(event) =>
                      setForm((current) => ({
                        ...current,
                        anthropic_tier: event.target.value,
                      }))
                    }
                  >
                    {anthropicTierOptions.map((tier) => (
                      <option key={tier} value={tier}>
                        {tier}
                      </option>
                    ))}
                  </select>
                  <span className="label-text-alt mt-2 block text-base-content/60">
                    metered = tight limits; elevated = trusted workloads; unrestricted =
                    administrators only.
                  </span>
                </label>
              ) : null}

              {!needsUpstreamKey ? (
                <div className="rounded-lg border border-base-300/70 bg-base-200/40 px-4 py-3.5 text-sm text-base-content/80">
                  <p className="font-medium text-base-content">
                    You still create an llm-proxy key (<code className="text-xs">sk-iw-*</code>).
                  </p>
                  <p className="mt-2 leading-relaxed">
                    Put it in your app as the Bedrock key for caller identity, rate limits, and
                    cost tracking. Outbound Bedrock requests (Converse and Mantle) are signed with
                    AWS SigV4 by the proxy — you never paste an AWS access key here.
                  </p>
                </div>
              ) : null}

              {needsUpstreamKey && providerAutoProvision && !provisionedKeysOnly && !treatAsPersonal ? (
                <details
                  className="rounded-lg border border-base-300 px-3 py-2"
                  open={manualKeyEntry}
                  onToggle={(event) => setManualKeyEntry(event.currentTarget.open)}
                >
                  <summary className="cursor-pointer text-sm font-medium">
                    Advanced: paste provider key
                  </summary>
                  <label className="form-control mt-3 w-full">
                    <span className="label-text">Provider API key</span>
                    <input
                      type="password"
                      autoComplete="new-password"
                      className="input input-bordered w-full font-mono"
                      placeholder="sk-..."
                      value={form.actual_key}
                      onChange={(event) =>
                        setForm((current) => ({
                          ...current,
                          actual_key: event.target.value,
                        }))
                      }
                    />
                  </label>
                </details>
              ) : null}

              {needsUpstreamKey && !providerAutoProvision && !provisionedKeysOnly && !treatAsPersonal ? (
                <label className="form-control w-full">
                  <span className="label-text">Provider API key</span>
                  <input
                    type="password"
                    autoComplete="new-password"
                    className="input input-bordered w-full font-mono"
                    placeholder="sk-..."
                    value={form.actual_key}
                    onChange={(event) =>
                      setForm((current) => ({
                        ...current,
                        actual_key: event.target.value,
                      }))
                    }
                  />
                </label>
              ) : null}

              <label className="form-control w-full">
                <span className="label-text mb-1.5">Name</span>
                <textarea
                  className="textarea textarea-bordered w-full"
                  rows={2}
                  placeholder="What is this key used for?"
                  value={form.description}
                  onChange={(event) =>
                    setForm((current) => ({
                      ...current,
                      description: event.target.value,
                    }))
                  }
                />
              </label>

              <ExpiryField form={form} setForm={setForm} />

              {treatAsPersonal ? (
                <div className="rounded-lg border border-base-300/70 bg-base-200/40 px-3 py-2 text-sm text-base-content/80">
                  Monthly spend limit: <span className="font-medium">{viewerMonthlyLimitLabel}</span>{" "}
                  (set by your organization).
                </div>
              ) : null}
            </>
          ) : null}

          {tab === "cost" && canManagePolicy && !treatAsPersonal ? (
            <CostFields form={form} setForm={setForm} editorMaxDollars={editorMaxDollars} />
          ) : null}

          {tab === "pii" && canManagePolicy && !treatAsPersonal ? (
            <PiiFields
              form={form}
              setForm={setForm}
              editingKey={null}
              piiOffRequiresBedrock={piiOffRequiresBedrock}
            />
          ) : null}

          {tab === "rate-limits" && canManagePolicy && !treatAsPersonal ? (
            <RateLimitFields form={form} setForm={setForm} />
          ) : null}

          <div className="modal-action">
            <button type="button" className="btn btn-ghost" onClick={onClose}>
              Cancel
            </button>
            <button
              type="submit"
              className="btn btn-primary"
              disabled={
                saving ||
                ((provisionedKeysOnly || treatAsPersonal) && !useAutoProvision && needsUpstreamKey)
              }
            >
              {saving ? <span className="loading loading-spinner loading-sm" /> : null}
              Create key
            </button>
          </div>
        </form>
      </div>
      <form method="dialog" className="modal-backdrop">
        <button type="button" aria-label="Close" onClick={onClose} />
      </form>
    </dialog>
  );
}
