import { FormEvent, useEffect, useMemo, useState } from "react";

import KeysTable from "../../components/keys/keys-table";
import PageHeader, {
  EmptyState,
  ErrorAlert,
  LoadingBlock,
} from "../../components/ui/page-header";
import { CopyButton } from "../../components/ui/copy-button";
import { maskKey } from "../../components/ui/masked-key";
import { useToast } from "../../components/ui/toast";
import { formatShareExpiry } from "../../lib/share-expiry";
import { dailyCostLimitFormDollars } from "../../lib/format";
import {
  useConfig,
  useCreateKey,
  useCreateShare,
  useDeleteKey,
  useKeys,
  useMe,
  useProvisioning,
  useUpdateKey,
} from "../../hooks/queries";
import type {
  APIKey,
  CreateAPIKeyRequest,
  PiiRedactSetting,
  Provider,
  ShareCreateResponse,
} from "../../types";

const PROVIDERS: Provider[] = ["openai", "anthropic", "gemini", "bedrock"];
const VIEWER_PROVIDERS: Provider[] = ["openai", "anthropic", "gemini"];

type PiiFormValue = "inherit" | "on" | "off";

type KeyFormState = {
  provider: Provider;
  actual_key: string;
  description: string;
  daily_cost_limit_dollars: string;
  enabled: boolean;
  redact_pii: PiiFormValue;
  rate_limit_rpm: string;
  rate_limit_tpm: string;
  rate_limit_rpd: string;
  rate_limit_tpd: string;
  anthropic_tier: string;
};

const defaultForm: KeyFormState = {
  provider: "openai",
  actual_key: "",
  description: "",
  daily_cost_limit_dollars: "100",
  enabled: true,
  redact_pii: "inherit",
  rate_limit_rpm: "",
  rate_limit_tpm: "",
  rate_limit_rpd: "",
  rate_limit_tpd: "",
  anthropic_tier: "metered",
};

function piiToFormValue(value?: PiiRedactSetting): PiiFormValue {
  if (value === true) return "on";
  if (value === false) return "off";
  return "inherit";
}

function piiFromFormValue(value: PiiFormValue): PiiRedactSetting {
  if (value === "on") return true;
  if (value === "off") return false;
  return null;
}

function formPiiOffRequiresBedrock(
  redactPii: PiiFormValue,
  globalPiiEnabled: boolean,
  canBypass: boolean,
): boolean {
  if (canBypass) return false;
  if (redactPii === "off") return true;
  if (redactPii === "inherit" && globalPiiEnabled) return false;
  return false;
}

function piiLabel(value?: PiiRedactSetting): string {
  if (value === true) return "On";
  if (value === false) return "Off";
  return "Inherit";
}

function parseLimitField(value: string): number {
  const n = Number(value.trim());
  return Number.isFinite(n) && n > 0 ? Math.round(n) : 0;
}

function formatRateLimits(record: APIKey): string {
  const parts: string[] = [];
  if (record.rate_limit_rpm) parts.push(`${record.rate_limit_rpm} rpm`);
  if (record.rate_limit_tpm) parts.push(`${record.rate_limit_tpm.toLocaleString()} tpm`);
  if (record.rate_limit_rpd) parts.push(`${record.rate_limit_rpd} rpd`);
  if (record.rate_limit_tpd) parts.push(`${record.rate_limit_tpd.toLocaleString()} tpd`);
  return parts.length ? parts.join(" · ") : "—";
}

function rateLimitsFromForm(form: KeyFormState) {
  return {
    rate_limit_rpm: parseLimitField(form.rate_limit_rpm),
    rate_limit_tpm: parseLimitField(form.rate_limit_tpm),
    rate_limit_rpd: parseLimitField(form.rate_limit_rpd),
    rate_limit_tpd: parseLimitField(form.rate_limit_tpd),
  };
}

export default function KeysPage() {
  const { push } = useToast();
  const { data: me } = useMe();
  const { data: config } = useConfig();
  const globalPiiEnabled = Boolean(config?.features?.pii_redact);
  const canBypassPiiBedrockPolicy = Boolean(me?.can_bypass_pii_off_non_bedrock_policy);
  const isViewer = me?.role === "viewer";
  const canDeleteKeys = isViewer || me?.role === "admin";
  const viewerMonthlyCents = me?.viewer_limits?.personal_monthly_cost_limit_cents ?? 1000;
  const viewerMonthlyDollars = (viewerMonthlyCents / 100).toFixed(2);
  const editorMaxCents = me?.editor_limits?.max_daily_cost_limit_cents ?? 0;
  const editorMaxDollars = editorMaxCents > 0 ? editorMaxCents / 100 : null;
  const [providerFilter, setProviderFilter] = useState<Provider | "">("");
  const [modalOpen, setModalOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<APIKey | null>(null);
  const [editingKey, setEditingKey] = useState<APIKey | null>(null);
  const [form, setForm] = useState<KeyFormState>(defaultForm);
  const [manualKeyEntry, setManualKeyEntry] = useState(false);
  const [shareResult, setShareResult] = useState<ShareCreateResponse | null>(null);
  const [sharingKey, setSharingKey] = useState<string | null>(null);

  const filter = providerFilter || undefined;
  const { data: keys = [], isLoading, error } = useKeys(filter);
  const { data: provisioning } = useProvisioning();
  const createKey = useCreateKey();
  const updateKey = useUpdateKey();
  const deleteKey = useDeleteKey();
  const createShare = useCreateShare();

  const piiOffRequiresBedrock = formPiiOffRequiresBedrock(
    form.redact_pii,
    globalPiiEnabled,
    canBypassPiiBedrockPolicy,
  );

  const anthropicProvisioning = provisioning?.providers?.anthropic;
  const anthropicTierOptions = anthropicProvisioning?.tiers ?? [];
  const anthropicDefaultTier = anthropicProvisioning?.default_tier ?? "metered";

  useEffect(() => {
    if (!modalOpen || editingKey || form.provider !== "anthropic" || anthropicTierOptions.length === 0) {
      return;
    }
    if (anthropicTierOptions.includes(form.anthropic_tier)) {
      return;
    }
    setForm((current) => ({
      ...current,
      anthropic_tier: anthropicDefaultTier,
    }));
  }, [modalOpen, editingKey, form.provider, form.anthropic_tier, anthropicTierOptions, anthropicDefaultTier]);

  useEffect(() => {
    if (!modalOpen || editingKey || !piiOffRequiresBedrock || form.provider === "bedrock") {
      return;
    }
    setForm((current) => ({ ...current, provider: "bedrock" }));
  }, [modalOpen, editingKey, piiOffRequiresBedrock, form.provider]);

  const availableProviders = useMemo(() => {
    if (isViewer) {
      const owned = new Set(keys.map((k) => k.provider));
      return VIEWER_PROVIDERS.filter((p) => !owned.has(p));
    }
    if (!piiOffRequiresBedrock) return PROVIDERS;
    return ["bedrock"] as Provider[];
  }, [isViewer, keys, piiOffRequiresBedrock]);

  const canCreateKey = !isViewer || availableProviders.length > 0;

  const providerAutoProvision = Boolean(
    provisioning?.enabled && provisioning.providers?.[form.provider]?.auto_provision,
  );
  const showAnthropicTierSelect =
    form.provider === "anthropic" && providerAutoProvision && !manualKeyEntry && anthropicTierOptions.length > 0;

  const onShare = async (record: APIKey) => {
    setSharingKey(record.key);
    try {
      const result = await createShare.mutateAsync(record.key);
      setShareResult(result);
    } catch (err) {
      push(err instanceof Error ? err.message : "Failed to create share link", "error");
    } finally {
      setSharingKey(null);
    }
  };

  const openCreate = () => {
    setEditingKey(null);
    const nextProvider = isViewer ? availableProviders[0] ?? "openai" : defaultForm.provider;
    setForm({ ...defaultForm, provider: nextProvider });
    setManualKeyEntry(false);
    setModalOpen(true);
  };

  const openEdit = (record: APIKey) => {
    setEditingKey(record);
    setForm({
      provider: record.provider,
      actual_key: "",
      description: record.description ?? "",
      daily_cost_limit_dollars: dailyCostLimitFormDollars(record.daily_cost_limit),
      enabled: record.enabled,
      redact_pii: piiToFormValue(record.redact_pii),
      rate_limit_rpm: record.rate_limit_rpm ? String(record.rate_limit_rpm) : "",
      rate_limit_tpm: record.rate_limit_tpm ? String(record.rate_limit_tpm) : "",
      rate_limit_rpd: record.rate_limit_rpd ? String(record.rate_limit_rpd) : "",
      rate_limit_tpd: record.rate_limit_tpd ? String(record.rate_limit_tpd) : "",
      anthropic_tier: record.tags?.tier ?? anthropicDefaultTier,
    });
    setModalOpen(true);
  };

  const closeModal = () => {
    setModalOpen(false);
    setEditingKey(null);
    setForm(defaultForm);
    setManualKeyEntry(false);
  };

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    const dailyCostLimit = Math.round(Number(form.daily_cost_limit_dollars || "0") * 100);
    if (editorMaxCents > 0 && dailyCostLimit > editorMaxCents) {
      push(`Daily cost limit cannot exceed $${editorMaxDollars}`, "error");
      return;
    }
    const redactPii = piiFromFormValue(form.redact_pii);

    try {
      if (editingKey) {
        if (isViewer) {
          await updateKey.mutateAsync({
            key: editingKey.key,
            body: { description: form.description },
          });
          push("Key updated", "success");
        } else {
        await updateKey.mutateAsync({
          key: editingKey.key,
          body: {
            description: form.description,
            daily_cost_limit: dailyCostLimit,
            enabled: form.enabled,
            redact_pii: redactPii,
            ...rateLimitsFromForm(form),
          },
        });
        push("Key updated", "success");
        }
      } else if (isViewer) {
        const useAutoProvision = providerAutoProvision && !manualKeyEntry;
        const body: CreateAPIKeyRequest = {
          provider: form.provider,
          description: form.description,
        };
        if (useAutoProvision) {
          body.auto_provision = true;
        } else {
          if (!form.actual_key.trim()) {
            push("Provider API key is required", "error");
            return;
          }
          body.actual_key = form.actual_key;
        }
        await createKey.mutateAsync(body);
        push("Personal key created", "success");
      } else {
        const useAutoProvision = providerAutoProvision && !manualKeyEntry;
        if (!useAutoProvision && !form.actual_key.trim()) {
          push("Provider API key is required", "error");
          return;
        }
        const body: CreateAPIKeyRequest = {
          provider: form.provider,
          description: form.description,
          daily_cost_limit: dailyCostLimit,
          enabled: form.enabled,
          redact_pii: redactPii,
          ...rateLimitsFromForm(form),
        };
        if (useAutoProvision) {
          body.auto_provision = true;
          if (form.provider === "anthropic" && form.anthropic_tier) {
            body.tags = { tier: form.anthropic_tier };
          }
        } else {
          body.actual_key = form.actual_key;
        }
        await createKey.mutateAsync(body);
        push("Key created", "success");
      }
      closeModal();
    } catch (err) {
      push(err instanceof Error ? err.message : "Request failed", "error");
    }
  };

  const onDelete = async () => {
    if (!deleteTarget) return;
    try {
      await deleteKey.mutateAsync(deleteTarget.key);
      push("Key deleted", "success");
      setDeleteTarget(null);
    } catch (err) {
      push(err instanceof Error ? err.message : "Delete failed", "error");
    }
  };

  const saving = createKey.isPending || updateKey.isPending;

  const visibleKeys = useMemo(() => keys, [keys]);

  return (
    <div className="space-y-6">
      <PageHeader
        title={isViewer ? "My API Keys" : "API Keys"}
        description={
          isViewer
            ? `Personal proxy keys (one per provider). Monthly spend is capped at $${viewerMonthlyDollars}.`
            : "Key registry is DynamoDB. Spend and PII stats on each key are in-memory (today only)."
        }
        actions={
          <>
            {!isViewer ? (
              <select
                className="select select-bordered select-sm"
                value={providerFilter}
                onChange={(event) => setProviderFilter(event.target.value as Provider | "")}
              >
                <option value="">All providers</option>
                {PROVIDERS.map((provider) => (
                  <option key={provider} value={provider}>
                    {provider}
                  </option>
                ))}
              </select>
            ) : null}
            <button
              type="button"
              className="btn btn-primary btn-sm"
              disabled={!canCreateKey}
              onClick={openCreate}
            >
              Create key
            </button>
          </>
        }
      />

      {error ? (
        <ErrorAlert message={error instanceof Error ? error.message : "Failed to load keys"} />
      ) : null}

      <div className="glass-panel overflow-hidden">
        {isLoading ? (
          <LoadingBlock />
        ) : visibleKeys.length === 0 ? (
          <EmptyState
            message={
              isViewer
                ? "No personal keys yet. Create one proxy key per provider to route LLM requests."
                : "No API keys yet. Create a proxy key to route provider requests through iw: keys."
            }
            action={
              canCreateKey ? (
                <button type="button" className="btn btn-primary btn-sm" onClick={openCreate}>
                  Create your first key
                </button>
              ) : undefined
            }
          />
        ) : (
          <KeysTable
            keys={visibleKeys}
            onShare={onShare}
            onEdit={openEdit}
            onDelete={setDeleteTarget}
            canDelete={canDeleteKeys}
            viewerMode={isViewer}
            sharingKey={sharingKey}
            maskKey={maskKey}
            formatRateLimits={formatRateLimits}
          />
        )}
      </div>

      {modalOpen ? (
        <dialog className="modal modal-open" open>
          <div className="modal-box max-w-lg">
            <h3 className="text-lg font-semibold">
              {editingKey ? "Edit API key" : isViewer ? "Create personal key" : "Create API key"}
            </h3>
            <form className="mt-4 space-y-4" onSubmit={onSubmit}>
              <label className="form-control w-full">
                <span className="label-text">Provider</span>
                <select
                  className="select select-bordered w-full"
                  disabled={Boolean(editingKey)}
                  value={form.provider}
                  onChange={(event) =>
                    setForm((current) => ({ ...current, provider: event.target.value as Provider }))
                  }
                >
                  {availableProviders.map((provider) => (
                    <option key={provider} value={provider}>
                      {provider}
                    </option>
                  ))}
                </select>
                {piiOffRequiresBedrock && !isViewer ? (
                  <span className="label-text-alt text-base-content/60">
                    PII redaction off requires the Bedrock provider.
                  </span>
                ) : null}
              </label>

              {!editingKey && providerAutoProvision && !manualKeyEntry ? (
                <div className="rounded-lg border border-primary/20 bg-primary/5 px-3 py-2 text-sm text-base-content/80">
                  Upstream key will be created automatically for {form.provider}.
                </div>
              ) : null}

              {!isViewer && showAnthropicTierSelect ? (
                <label className="form-control w-full">
                  <span className="label-text">Anthropic tier</span>
                  <select
                    className="select select-bordered w-full"
                    value={form.anthropic_tier}
                    onChange={(event) =>
                      setForm((current) => ({ ...current, anthropic_tier: event.target.value }))
                    }
                  >
                    {anthropicTierOptions.map((tier) => (
                      <option key={tier} value={tier}>
                        {tier}
                      </option>
                    ))}
                  </select>
                  <span className="label-text-alt text-base-content/60">
                    metered = tight limits; elevated = trusted workloads; unrestricted = administrators only.
                  </span>
                </label>
              ) : null}

              {!editingKey && providerAutoProvision ? (
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
                        setForm((current) => ({ ...current, actual_key: event.target.value }))
                      }
                    />
                  </label>
                </details>
              ) : null}

              {!editingKey && !providerAutoProvision ? (
                <label className="form-control w-full">
                  <span className="label-text">Provider API key</span>
                  <input
                    type="password"
                    autoComplete="new-password"
                    className="input input-bordered w-full font-mono"
                    placeholder="sk-..."
                    value={form.actual_key}
                    onChange={(event) =>
                      setForm((current) => ({ ...current, actual_key: event.target.value }))
                    }
                  />
                </label>
              ) : null}

              <label className="form-control w-full">
                <span className="label-text">Description</span>
                <textarea
                  className="textarea textarea-bordered w-full"
                  rows={2}
                  placeholder="What is this key used for?"
                  value={form.description}
                  onChange={(event) =>
                    setForm((current) => ({ ...current, description: event.target.value }))
                  }
                />
              </label>

              {isViewer && !editingKey ? (
                <div className="rounded-lg border border-base-300/70 bg-base-200/40 px-3 py-2 text-sm text-base-content/80">
                  Monthly spend limit: <span className="font-medium">${viewerMonthlyDollars}</span> (set by
                  your organization).
                </div>
              ) : null}

              {!isViewer ? (
              <div className="grid gap-4 sm:grid-cols-[1fr_auto] sm:items-end">
                <label className="form-control w-full">
                  <span className="label-text">Daily cost limit (USD)</span>
                  <input
                    type="number"
                    min="0"
                    step="1"
                    className="input input-bordered w-full"
                    placeholder="0 = unlimited"
                    value={form.daily_cost_limit_dollars}
                    onChange={(event) =>
                      setForm((current) => ({
                        ...current,
                        daily_cost_limit_dollars: event.target.value,
                      }))
                    }
                  />
                  <p className="mt-1.5 text-xs text-base-content/60">
                    Leave at 0 for unlimited
                    {editorMaxDollars != null ? ` · Editor max $${editorMaxDollars}/day` : null}
                  </p>
                </label>

                <div className="form-control w-full sm:w-auto">
                  <span className="label-text">Key status</span>
                  <label className="flex h-12 cursor-pointer items-center gap-3">
                    <input
                      type="checkbox"
                      className="toggle toggle-primary"
                      checked={form.enabled}
                      onChange={(event) =>
                        setForm((current) => ({ ...current, enabled: event.target.checked }))
                      }
                    />
                    <span className="text-sm font-medium">{form.enabled ? "Enabled" : "Disabled"}</span>
                  </label>
                </div>
              </div>

              <label className="form-control w-full">
                <span className="label-text">PII redaction</span>
                <select
                  className="select select-bordered w-full"
                  value={form.redact_pii}
                  onChange={(event) =>
                    setForm((current) => ({
                      ...current,
                      redact_pii: event.target.value as PiiFormValue,
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

              <div className="rounded-xl border border-base-300/70 p-4">
                <div className="mb-3 text-sm font-medium">Rate limits</div>
                <p className="mb-3 text-xs text-base-content/60">
                  Optional per-key overrides. Leave blank to inherit global limits. Zero clears an override.
                </p>
                <div className="grid gap-3 sm:grid-cols-2">
                  <label className="form-control">
                    <span className="label-text text-xs">Requests / minute</span>
                    <input
                      type="number"
                      min="0"
                      className="input input-bordered input-sm w-full"
                      placeholder="inherit"
                      value={form.rate_limit_rpm}
                      onChange={(event) =>
                        setForm((current) => ({ ...current, rate_limit_rpm: event.target.value }))
                      }
                    />
                  </label>
                  <label className="form-control">
                    <span className="label-text text-xs">Tokens / minute</span>
                    <input
                      type="number"
                      min="0"
                      className="input input-bordered input-sm w-full"
                      placeholder="inherit"
                      value={form.rate_limit_tpm}
                      onChange={(event) =>
                        setForm((current) => ({ ...current, rate_limit_tpm: event.target.value }))
                      }
                    />
                  </label>
                  <label className="form-control">
                    <span className="label-text text-xs">Requests / day</span>
                    <input
                      type="number"
                      min="0"
                      className="input input-bordered input-sm w-full"
                      placeholder="inherit"
                      value={form.rate_limit_rpd}
                      onChange={(event) =>
                        setForm((current) => ({ ...current, rate_limit_rpd: event.target.value }))
                      }
                    />
                  </label>
                  <label className="form-control">
                    <span className="label-text text-xs">Tokens / day</span>
                    <input
                      type="number"
                      min="0"
                      className="input input-bordered input-sm w-full"
                      placeholder="inherit"
                      value={form.rate_limit_tpd}
                      onChange={(event) =>
                        setForm((current) => ({ ...current, rate_limit_tpd: event.target.value }))
                      }
                    />
                  </label>
                </div>
              </div>
              ) : null}

              <div className="modal-action">
                <button type="button" className="btn btn-ghost" onClick={closeModal}>
                  Cancel
                </button>
                <button type="submit" className="btn btn-primary" disabled={saving}>
                  {saving ? <span className="loading loading-spinner loading-sm" /> : null}
                  {editingKey ? "Save changes" : "Create key"}
                </button>
              </div>
            </form>
          </div>
          <form method="dialog" className="modal-backdrop">
            <button type="button" aria-label="Close" onClick={closeModal} />
          </form>
        </dialog>
      ) : null}

      {shareResult ? (
        <dialog className="modal modal-open" open>
          <div className="modal-box max-w-lg">
            <h3 className="text-lg font-semibold">Shareable link created</h3>
            <p className="py-3 text-sm text-base-content/70">
              Send this link to whoever needs the key. They must sign in with an
              account on your configured allowed domain to view it. The URL
              contains no key material.
            </p>
            <div className="flex items-center gap-2">
              <code className="flex-1 truncate rounded-lg bg-base-200/70 px-3 py-2 font-mono text-sm">
                {shareResult.url}
              </code>
              <CopyButton value={shareResult.url} label="Copy link" />
            </div>
            {shareResult.expires_at ? (
              <p
                className={`mt-3 text-sm ${formatShareExpiry(shareResult.expires_at).urgent
                  ? "text-warning"
                  : "text-base-content/60"
                  }`}
              >
                {formatShareExpiry(shareResult.expires_at).message}. Re-sharing the same key within
                24 hours reuses this URL.
              </p>
            ) : null}
            <div className="modal-action">
              <a
                href={shareResult.url}
                target="_blank"
                rel="noreferrer"
                className="btn btn-ghost"
              >
                Open
              </a>
              <button type="button" className="btn btn-primary" onClick={() => setShareResult(null)}>
                Done
              </button>
            </div>
          </div>
          <form method="dialog" className="modal-backdrop">
            <button type="button" aria-label="Close" onClick={() => setShareResult(null)} />
          </form>
        </dialog>
      ) : null}

      {deleteTarget ? (
        <dialog className="modal modal-open" open>
          <div className="modal-box">
            <h3 className="text-lg font-semibold">Delete API key?</h3>
            <p className="py-4 text-sm text-base-content/70">
              This will permanently remove{" "}
              <span className="code-chip font-mono">{deleteTarget ? maskKey(deleteTarget.key) : ""}</span>.
            </p>
            <div className="modal-action">
              <button type="button" className="btn btn-ghost" onClick={() => setDeleteTarget(null)}>
                Cancel
              </button>
              <button
                type="button"
                className="btn btn-error"
                disabled={deleteKey.isPending}
                onClick={onDelete}
              >
                Delete
              </button>
            </div>
          </div>
          <form method="dialog" className="modal-backdrop">
            <button type="button" aria-label="Close" onClick={() => setDeleteTarget(null)} />
          </form>
        </dialog>
      ) : null}
    </div>
  );
}
