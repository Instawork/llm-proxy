import { FormEvent, useState } from "react";

import {
  modalTabClass,
  type KeyFormState,
  type KeyFormTab,
  providerLabel,
  providerNeedsUpstreamKey,
} from "../../lib/key-form";
import type { APIKey, Provider } from "../../types";

type ApiKeysModalProps = {
  open: boolean;
  onClose: () => void;
  onSubmit: (event: FormEvent) => void;
  saving: boolean;
  editingKey: APIKey | null;
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

export default function ApiKeysModal({
  open,
  onClose,
  onSubmit,
  saving,
  editingKey,
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
}: ApiKeysModalProps) {
  const [tab, setTab] = useState<KeyFormTab>("general");

  if (!open) return null;

  const policyTabs: KeyFormTab[] = canManagePolicy && !treatAsPersonal
    ? ["cost", "pii", "rate-limits"]
    : [];
  const visibleTabs: KeyFormTab[] = ["general", ...policyTabs];

  const title = editingKey
    ? "Edit API key"
    : treatAsPersonal
      ? "Create personal key"
      : "Create API key";
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

        <form className="mt-4 space-y-4" onSubmit={onSubmit}>
          {tab === "general" ? (
            <>
              {!editingKey && !isViewer ? (
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

              <label className="form-control w-full">
                <span className="label-text">Provider</span>
                <select
                  className="select select-bordered w-full"
                  disabled={Boolean(editingKey)}
                  value={form.provider}
                  onChange={(event) =>
                    setForm((current) => ({
                      ...current,
                      provider: event.target.value as Provider,
                    }))
                  }
                >
                  {availableProviders.map((provider) => (
                    <option key={provider} value={provider}>
                      {providerLabel(provider)}
                    </option>
                  ))}
                </select>
                {piiOffRequiresBedrock && !treatAsPersonal ? (
                  <span className="label-text-alt text-base-content/60">
                    PII redaction off requires the Bedrock provider.
                  </span>
                ) : null}
                {!needsUpstreamKey ? (
                  <span className="label-text-alt text-base-content/60">
                    No provider API key needed — upstream calls authenticate with AWS SigV4.
                  </span>
                ) : null}
              </label>

              {!editingKey && useAutoProvision ? (
                <div className="rounded-lg border border-primary/20 bg-primary/5 px-3 py-2 text-sm text-base-content/80">
                  Upstream key will be created automatically for {form.provider}.
                </div>
              ) : null}

              {!editingKey && (provisionedKeysOnly || treatAsPersonal) && !providerAutoProvision ? (
                <div className="rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-sm text-warning">
                  Automatic key provisioning is not available for {form.provider}. Choose another
                  provider or contact an administrator.
                </div>
              ) : null}

              {!treatAsPersonal && showAnthropicTierSelect ? (
                <label className="form-control w-full">
                  <span className="label-text">Anthropic tier</span>
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
                  <span className="label-text-alt text-base-content/60">
                    metered = tight limits; elevated = trusted workloads; unrestricted =
                    administrators only.
                  </span>
                </label>
              ) : null}

              {!editingKey && !needsUpstreamKey ? (
                <div className="rounded-lg border border-base-300/70 bg-base-200/40 px-3 py-2 text-sm text-base-content/80">
                  This proxy key is only for caller identity, rate limits, and cost tracking.
                  AWS credentials on the proxy sign outbound Bedrock requests (Converse and Mantle).
                </div>
              ) : null}

              {!editingKey && needsUpstreamKey && providerAutoProvision && !provisionedKeysOnly && !treatAsPersonal ? (
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

              {!editingKey && needsUpstreamKey && !providerAutoProvision && !provisionedKeysOnly && !treatAsPersonal ? (
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
                <span className="label-text">Name</span>
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

              {treatAsPersonal && !editingKey ? (
                <div className="rounded-lg border border-base-300/70 bg-base-200/40 px-3 py-2 text-sm text-base-content/80">
                  Monthly spend limit: <span className="font-medium">{viewerMonthlyLimitLabel}</span>{" "}
                  (set by your organization).
                </div>
              ) : null}

              {editingKey && canManagePolicy && !treatAsPersonal ? (
                <div className="form-control w-full">
                  <span className="label-text">Key status</span>
                  <label className="flex h-12 cursor-pointer items-center gap-3">
                    <input
                      type="checkbox"
                      className="toggle toggle-primary"
                      checked={form.enabled}
                      onChange={(event) =>
                        setForm((current) => ({
                          ...current,
                          enabled: event.target.checked,
                        }))
                      }
                    />
                    <span className="text-sm font-medium">
                      {form.enabled ? "Enabled" : "Disabled"}
                    </span>
                  </label>
                </div>
              ) : null}
            </>
          ) : null}

          {tab === "cost" && canManagePolicy && !treatAsPersonal ? (
            <CostFields
              form={form}
              setForm={setForm}
              editorMaxDollars={editorMaxDollars}
              showEnabled={!editingKey}
            />
          ) : null}

          {tab === "pii" && canManagePolicy && !treatAsPersonal ? (
            <PiiFields
              form={form}
              setForm={setForm}
              editingKey={editingKey}
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
                (!editingKey && (provisionedKeysOnly || treatAsPersonal) && !useAutoProvision)
              }
            >
              {saving ? <span className="loading loading-spinner loading-sm" /> : null}
              {editingKey ? "Save changes" : "Create key"}
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

export function CostFields({
  form,
  setForm,
  editorMaxDollars,
  showEnabled = true,
}: {
  form: KeyFormState;
  setForm: React.Dispatch<React.SetStateAction<KeyFormState>>;
  editorMaxDollars: number | null;
  showEnabled?: boolean;
}) {
  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-[1fr_auto] sm:items-end">
        <div className="form-control w-full">
          <span className="label-text">Cost limit (USD)</span>
          <div className="flex gap-2">
            <select
              className="select select-bordered w-32 shrink-0"
              value={form.cost_limit_period}
              onChange={(event) =>
                setForm((current) => ({
                  ...current,
                  cost_limit_period: event.target.value as KeyFormState["cost_limit_period"],
                  cost_limit_dollars: "",
                }))
              }
            >
              <option value="daily">Daily</option>
              <option value="monthly">Monthly</option>
            </select>
            <input
              type="number"
              min="0"
              step="1"
              className="input input-bordered min-w-0 flex-1"
              placeholder="0 = unlimited"
              value={form.cost_limit_dollars}
              onChange={(event) =>
                setForm((current) => ({
                  ...current,
                  cost_limit_dollars: event.target.value,
                }))
              }
            />
          </div>
          <p className="mt-1.5 text-xs text-base-content/60">
            Leave at 0 for unlimited
            {editorMaxDollars != null && form.cost_limit_period === "daily"
              ? ` · Editor max $${editorMaxDollars}/day`
              : null}
          </p>
        </div>

        {showEnabled ? (
          <div className="form-control w-full sm:w-auto">
            <span className="label-text">Key status</span>
            <label className="flex h-12 cursor-pointer items-center gap-3">
              <input
                type="checkbox"
                className="toggle toggle-primary"
                checked={form.enabled}
                onChange={(event) =>
                  setForm((current) => ({
                    ...current,
                    enabled: event.target.checked,
                  }))
                }
              />
              <span className="text-sm font-medium">{form.enabled ? "Enabled" : "Disabled"}</span>
            </label>
          </div>
        ) : null}
      </div>
    </div>
  );
}

export function PiiFields({
  form,
  setForm,
  editingKey,
  piiOffRequiresBedrock,
}: {
  form: KeyFormState;
  setForm: React.Dispatch<React.SetStateAction<KeyFormState>>;
  editingKey: APIKey | null;
  piiOffRequiresBedrock: boolean;
}) {
  return (
    <label className="form-control w-full">
      <span className="label-text">PII redaction</span>
      <select
        className="select select-bordered w-full"
        value={form.redact_pii}
        onChange={(event) =>
          setForm((current) => ({
            ...current,
            redact_pii: event.target.value as KeyFormState["redact_pii"],
          }))
        }
      >
        <option value="inherit">Inherit global default</option>
        <option value="on">On</option>
        <option value="off">Off</option>
      </select>
      {piiOffRequiresBedrock && editingKey && editingKey.provider !== "bedrock" ? (
        <p className="mt-1.5 text-xs text-warning">
          Turning PII off requires a Bedrock key. Create a new Bedrock key instead.
        </p>
      ) : null}
    </label>
  );
}

export function RateLimitFields({
  form,
  setForm,
}: {
  form: KeyFormState;
  setForm: React.Dispatch<React.SetStateAction<KeyFormState>>;
}) {
  return (
    <div className="rounded-xl border border-base-300/70 p-4">
      <div className="mb-3 text-sm font-medium">Rate limits</div>
      <p className="mb-3 text-xs text-base-content/60">
        Optional per-key overrides. Leave blank to inherit global limits. Zero clears an override.
      </p>
      <div className="grid gap-3 sm:grid-cols-2">
        {(
          [
            ["rate_limit_rpm", "Requests / minute"],
            ["rate_limit_tpm", "Tokens / minute"],
            ["rate_limit_rpd", "Requests / day"],
            ["rate_limit_tpd", "Tokens / day"],
          ] as const
        ).map(([field, label]) => (
          <label key={field} className="form-control">
            <span className="label-text text-xs">{label}</span>
            <input
              type="number"
              min="0"
              className="input input-bordered input-sm w-full"
              placeholder="inherit"
              value={form[field]}
              onChange={(event) =>
                setForm((current) => ({
                  ...current,
                  [field]: event.target.value,
                }))
              }
            />
          </label>
        ))}
      </div>
    </div>
  );
}
