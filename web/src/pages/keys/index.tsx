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
  useCost,
  useCreateKey,
  useCreateShare,
  useDeleteKey,
  useKeys,
  useMe,
  usePII,
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
  if (record.rate_limit_tpm)
    parts.push(`${record.rate_limit_tpm.toLocaleString()} tpm`);
  if (record.rate_limit_rpd) parts.push(`${record.rate_limit_rpd} rpd`);
  if (record.rate_limit_tpd)
    parts.push(`${record.rate_limit_tpd.toLocaleString()} tpd`);
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

function keyStatsDescription(hasCostRedis: boolean, hasPiiRedis: boolean): string {
  if (hasCostRedis && hasPiiRedis) {
    return "Key registry is DynamoDB. Per-key spend and PII stats use Redis rollups (today updates live).";
  }
  if (hasCostRedis) {
    return "Key registry is DynamoDB. Per-key spend uses Redis rollups; PII stats are in-memory (today only).";
  }
  if (hasPiiRedis) {
    return "Key registry is DynamoDB. Per-key PII stats use Redis rollups; spend is in-memory (today only).";
  }
  return "Key registry is DynamoDB. Spend and PII stats on each key are in-memory (today only).";
}

export default function KeysPage() {
  const { push } = useToast();
  const { data: me } = useMe();
  const { data: config } = useConfig();
  const { data: costData } = useCost();
  const { data: piiData } = usePII();
  const hasCostRedis = Boolean(costData?.stats?.daily_history_available);
  const hasPiiRedis = Boolean(piiData?.stats?.daily_history_available);
  const globalPiiEnabled = Boolean(config?.features?.pii_redact);
  const canBypassPiiBedrockPolicy = Boolean(
    me?.can_bypass_pii_off_non_bedrock_policy,
  );
  const isViewer = me?.role === "viewer";
  const isAdmin = me?.role === "admin";
  const provisionedKeysOnly = !isAdmin;
  const canDeleteKeys = isViewer || me?.role === "admin";
  const viewerMonthlyCents =
    me?.viewer_limits?.personal_monthly_cost_limit_cents ?? 1000;
  const viewerMonthlyLimitLabel =
    viewerMonthlyCents > 0
      ? `$${(viewerMonthlyCents / 100).toFixed(2)}`
      : "Unlimited";
  const editorMaxCents = me?.editor_limits?.max_daily_cost_limit_cents ?? 0;
  const editorMaxDollars = editorMaxCents > 0 ? editorMaxCents / 100 : null;
  const bulkKeyDescription = me?.email?.split("@")[0]?.trim() ?? "";
  const [providerFilter, setProviderFilter] = useState<Provider | "">("");
  const [modalOpen, setModalOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<APIKey | null>(null);
  const [editingKey, setEditingKey] = useState<APIKey | null>(null);
  const [form, setForm] = useState<KeyFormState>(defaultForm);
  const [manualKeyEntry, setManualKeyEntry] = useState(false);
  const [personalMode, setPersonalMode] = useState(false);
  const [shareResult, setShareResult] = useState<ShareCreateResponse | null>(
    null,
  );
  const [sharingKey, setSharingKey] = useState<string | null>(null);
  const [bulkGenerating, setBulkGenerating] = useState(false);

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
    if (
      !modalOpen ||
      editingKey ||
      form.provider !== "anthropic" ||
      anthropicTierOptions.length === 0
    ) {
      return;
    }
    if (anthropicTierOptions.includes(form.anthropic_tier)) {
      return;
    }
    setForm((current) => ({
      ...current,
      anthropic_tier: anthropicDefaultTier,
    }));
  }, [
    modalOpen,
    editingKey,
    form.provider,
    form.anthropic_tier,
    anthropicTierOptions,
    anthropicDefaultTier,
  ]);

  useEffect(() => {
    if (
      !modalOpen ||
      editingKey ||
      !piiOffRequiresBedrock ||
      form.provider === "bedrock"
    ) {
      return;
    }
    setForm((current) => ({ ...current, provider: "bedrock" }));
  }, [modalOpen, editingKey, piiOffRequiresBedrock, form.provider]);

  const treatAsPersonal = isViewer || (personalMode && !editingKey);

  const myPersonalProviders = useMemo(() => {
    const set = new Set<Provider>();
    const email = me?.email?.toLowerCase();
    if (!email) return set;
    for (const k of keys) {
      if (
        k.tags?.personal === "true" &&
        k.owner_email?.toLowerCase() === email
      ) {
        set.add(k.provider);
      }
    }
    return set;
  }, [keys, me?.email]);

  const availableProviders = useMemo(() => {
    let providers: Provider[];
    if (isViewer) {
      const owned = new Set(keys.map((k) => k.provider));
      providers = VIEWER_PROVIDERS.filter(
        (p) => !owned.has(p) || p === editingKey?.provider,
      );
    } else if (personalMode) {
      providers = VIEWER_PROVIDERS.filter((p) => !myPersonalProviders.has(p));
    } else if (!piiOffRequiresBedrock) {
      providers = [...PROVIDERS];
    } else {
      providers = ["bedrock"];
    }
    if ((isViewer || personalMode) && provisioning?.enabled) {
      return providers.filter(
        (p) => provisioning.providers?.[p]?.auto_provision,
      );
    }
    if (!provisionedKeysOnly || !provisioning?.enabled) {
      return providers;
    }
    return providers.filter((p) => provisioning.providers?.[p]?.auto_provision);
  }, [
    isViewer,
    personalMode,
    keys,
    editingKey?.provider,
    myPersonalProviders,
    piiOffRequiresBedrock,
    provisionedKeysOnly,
    provisioning,
  ]);

  const canCreateKey = availableProviders.length > 0;

  const missingProvidersForBulk = useMemo(() => {
    if (!provisionedKeysOnly) {
      return [];
    }
    const owned = new Set(keys.map((k) => k.provider));
    return availableProviders.filter((provider) => !owned.has(provider));
  }, [provisionedKeysOnly, keys, availableProviders]);

  const missingPersonalProvidersForBulk = useMemo(() => {
    if (!provisioning?.enabled) {
      return [];
    }
    return VIEWER_PROVIDERS.filter(
      (provider) =>
        provisioning.providers?.[provider]?.auto_provision &&
        !myPersonalProviders.has(provider),
    );
  }, [provisioning, myPersonalProviders]);

  const bulkTargetProviders = isViewer
    ? missingProvidersForBulk
    : isAdmin
      ? missingPersonalProvidersForBulk
      : missingProvidersForBulk;

  const canBulkGeneratePersonalKeys = bulkTargetProviders.length > 0;

  const providerAutoProvision = Boolean(
    provisioning?.enabled &&
    provisioning.providers?.[form.provider]?.auto_provision,
  );
  const useAutoProvision =
    treatAsPersonal || provisionedKeysOnly
      ? providerAutoProvision
      : providerAutoProvision && !manualKeyEntry;
  const showAnthropicTierSelect =
    form.provider === "anthropic" &&
    useAutoProvision &&
    anthropicTierOptions.length > 0;

  const onShare = async (record: APIKey) => {
    setSharingKey(record.key);
    try {
      const result = await createShare.mutateAsync(record.key);
      setShareResult(result);
    } catch (err) {
      push(
        err instanceof Error ? err.message : "Failed to create share link",
        "error",
      );
    } finally {
      setSharingKey(null);
    }
  };

  const openCreate = () => {
    setEditingKey(null);
    const nextProvider = provisionedKeysOnly
      ? (availableProviders[0] ?? defaultForm.provider)
      : isViewer
        ? (availableProviders[0] ?? "openai")
        : defaultForm.provider;
    setForm({ ...defaultForm, provider: nextProvider });
    setManualKeyEntry(false);
    setPersonalMode(false);
    setModalOpen(true);
  };

  const onTogglePersonalMode = (on: boolean) => {
    setPersonalMode(on);
    setManualKeyEntry(false);
    setForm((current) => ({
      ...current,
      provider: on
        ? (VIEWER_PROVIDERS.find((p) => !myPersonalProviders.has(p)) ??
          current.provider)
        : defaultForm.provider,
    }));
  };

  const openEdit = (record: APIKey) => {
    setEditingKey(record);
    setPersonalMode(false);
    setForm({
      provider: record.provider,
      actual_key: "",
      description: record.description ?? "",
      daily_cost_limit_dollars: dailyCostLimitFormDollars(
        record.daily_cost_limit,
      ),
      enabled: record.enabled,
      redact_pii: piiToFormValue(record.redact_pii),
      rate_limit_rpm: record.rate_limit_rpm
        ? String(record.rate_limit_rpm)
        : "",
      rate_limit_tpm: record.rate_limit_tpm
        ? String(record.rate_limit_tpm)
        : "",
      rate_limit_rpd: record.rate_limit_rpd
        ? String(record.rate_limit_rpd)
        : "",
      rate_limit_tpd: record.rate_limit_tpd
        ? String(record.rate_limit_tpd)
        : "",
      anthropic_tier: record.tags?.tier ?? anthropicDefaultTier,
    });
    setModalOpen(true);
  };

  const closeModal = () => {
    setModalOpen(false);
    setEditingKey(null);
    setForm(defaultForm);
    setManualKeyEntry(false);
    setPersonalMode(false);
  };

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    const dailyCostLimit = Math.round(
      Number(form.daily_cost_limit_dollars || "0") * 100,
    );
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
      } else if (treatAsPersonal) {
        if (!useAutoProvision) {
          push(
            "Automatic key provisioning is not available for this provider",
            "error",
          );
          return;
        }
        const body: CreateAPIKeyRequest = {
          provider: form.provider,
          description: form.description,
          auto_provision: true,
        };
        if (!isViewer) {
          body.personal = true;
        }
        await createKey.mutateAsync(body);
        push("Personal key created", "success");
      } else {
        if (!useAutoProvision) {
          if (provisionedKeysOnly) {
            push(
              "Automatic key provisioning is not available for this provider",
              "error",
            );
            return;
          }
          if (!form.actual_key.trim()) {
            push("Provider API key is required", "error");
            return;
          }
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

  const onBulkGeneratePersonalKeys = async () => {
    if (bulkTargetProviders.length === 0) {
      return;
    }
    setBulkGenerating(true);
    let created = 0;
    let failed = 0;
    try {
      for (const provider of bulkTargetProviders) {
        try {
          if (isViewer || isAdmin) {
            await createKey.mutateAsync({
              provider,
              description: bulkKeyDescription,
              auto_provision: true,
              ...(isAdmin ? { personal: true } : {}),
            });
          } else {
            const dailyCostLimit = Math.round(
              Number(defaultForm.daily_cost_limit_dollars || "0") * 100,
            );
            if (editorMaxCents > 0 && dailyCostLimit > editorMaxCents) {
              push(
                `Daily cost limit cannot exceed $${editorMaxDollars}`,
                "error",
              );
              return;
            }
            const body: CreateAPIKeyRequest = {
              provider,
              description: "",
              daily_cost_limit: dailyCostLimit,
              enabled: defaultForm.enabled,
              redact_pii: piiFromFormValue(defaultForm.redact_pii),
              auto_provision: true,
              ...rateLimitsFromForm(defaultForm),
            };
            if (provider === "anthropic" && anthropicDefaultTier) {
              body.tags = { tier: anthropicDefaultTier };
            }
            await createKey.mutateAsync(body);
          }
          created += 1;
        } catch {
          failed += 1;
        }
      }
      if (created > 0) {
        push(
          isViewer || isAdmin
            ? `Created ${created} personal key${created === 1 ? "" : "s"}`
            : `Created ${created} key${created === 1 ? "" : "s"}`,
          "success",
        );
      }
      if (failed > 0) {
        push(
          `Failed to create ${failed} key${failed === 1 ? "" : "s"}`,
          "error",
        );
      }
    } finally {
      setBulkGenerating(false);
    }
  };

  const saving = createKey.isPending || updateKey.isPending;
  const bulkGenerateBusy = bulkGenerating || createKey.isPending;

  const visibleKeys = useMemo(() => keys, [keys]);

  return (
    <div className="space-y-6">
      <PageHeader
        title={isViewer ? "My API Keys" : "API Keys"}
        description={
          isViewer
            ? viewerMonthlyCents > 0
              ? `Personal proxy keys (one per provider). Monthly spend is capped at ${viewerMonthlyLimitLabel}.`
              : "Personal proxy keys (one per provider). Monthly spend is unlimited."
            : keyStatsDescription(hasCostRedis, hasPiiRedis)
        }
        actions={
          <>
            {!isViewer ? (
              <select
                className="select select-bordered select-sm"
                value={providerFilter}
                onChange={(event) =>
                  setProviderFilter(event.target.value as Provider | "")
                }
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

      {isViewer || isAdmin ? (
        <div className="glass-panel border-l-4 border-l-[#4A154B] px-4 py-3 text-sm text-base-content/80">
          {isViewer ? (
            <>
              These are meant for local testing. If you are deploying something,
              request a key for that service in{" "}
              <span className="rounded bg-[#4A154B]/10 px-1 font-bold text-[#4A154B]">
                #it-helpdesk
              </span>{" "}
              in{" "}
              <span className="rounded bg-[#4A154B]/10 px-1 font-bold text-[#4A154B]">
                Slack
              </span>
              .
            </>
          ) : (
            <>
              Use <span className="font-medium">Generate Personal Keys</span> below
              or toggle <span className="font-medium">Personal key</span> in Create
              key for your own testing keys (one per provider, capped at{" "}
              {viewerMonthlyLimitLabel}/month). Org-wide service keys stay separate.
            </>
          )}
        </div>
      ) : null}

      {error ? (
        <ErrorAlert
          message={
            error instanceof Error ? error.message : "Failed to load keys"
          }
        />
      ) : null}

      {canBulkGeneratePersonalKeys && visibleKeys.length > 0 ? (
        <div className="glass-panel flex flex-col items-center gap-3 px-6 py-8 text-center sm:items-start sm:text-left">
          <button
            type="button"
            className="btn btn-primary btn-lg min-h-16 w-full px-8 text-lg sm:w-auto"
            disabled={bulkGenerateBusy}
            onClick={onBulkGeneratePersonalKeys}
          >
            {bulkGenerateBusy ? (
              <span className="loading loading-spinner loading-md" />
            ) : null}
            Generate Personal Keys
          </button>
          <p className="max-w-2xl text-sm text-base-content/60">
            {isViewer || isAdmin
              ? `Creates one auto-provisioned personal key for each provider you do not have yet (${bulkTargetProviders.join(", ")}).`
              : `Creates one auto-provisioned key for each provider without a key yet (${bulkTargetProviders.join(", ")}).`}
          </p>
        </div>
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
              canBulkGeneratePersonalKeys ? (
                <button
                  type="button"
                  className="btn btn-primary btn-lg min-h-16 w-full max-w-md px-8 text-lg"
                  disabled={bulkGenerateBusy}
                  onClick={onBulkGeneratePersonalKeys}
                >
                  {bulkGenerateBusy ? (
                    <span className="loading loading-spinner loading-md" />
                  ) : null}
                  Generate Personal Keys
                </button>
              ) : canCreateKey ? (
                <button
                  type="button"
                  className="btn btn-primary btn-sm"
                  onClick={openCreate}
                >
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
              {editingKey
                ? "Edit API key"
                : treatAsPersonal
                  ? "Create personal key"
                  : "Create API key"}
            </h3>
            <form className="mt-4 space-y-4" onSubmit={onSubmit}>
              {!editingKey && !isViewer ? (
                <label className="flex cursor-pointer items-center justify-between gap-3 rounded-lg border border-base-300/70 bg-base-200/40 px-3 py-2">
                  <span className="text-sm">
                    <span className="font-medium">Personal key</span>
                    <span className="block text-xs text-base-content/60">
                      Owned by you, one per provider, capped at{" "}
                      {viewerMonthlyLimitLabel}/month.
                    </span>
                  </span>
                  <input
                    type="checkbox"
                    className="toggle toggle-primary"
                    checked={personalMode}
                    onChange={(event) =>
                      onTogglePersonalMode(event.target.checked)
                    }
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
                      {provider}
                    </option>
                  ))}
                </select>
                {piiOffRequiresBedrock && !treatAsPersonal ? (
                  <span className="label-text-alt text-base-content/60">
                    PII redaction off requires the Bedrock provider.
                  </span>
                ) : null}
              </label>

              {!editingKey && useAutoProvision ? (
                <div className="rounded-lg border border-primary/20 bg-primary/5 px-3 py-2 text-sm text-base-content/80">
                  Upstream key will be created automatically for {form.provider}
                  .
                </div>
              ) : null}

              {!editingKey &&
                (provisionedKeysOnly || treatAsPersonal) &&
                !providerAutoProvision ? (
                <div className="rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-sm text-warning">
                  Automatic key provisioning is not available for{" "}
                  {form.provider}. Choose another provider or contact an
                  administrator.
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
                    metered = tight limits; elevated = trusted workloads;
                    unrestricted = administrators only.
                  </span>
                </label>
              ) : null}

              {!editingKey &&
                providerAutoProvision &&
                !provisionedKeysOnly &&
                !treatAsPersonal ? (
                <details
                  className="rounded-lg border border-base-300 px-3 py-2"
                  open={manualKeyEntry}
                  onToggle={(event) =>
                    setManualKeyEntry(event.currentTarget.open)
                  }
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

              {!editingKey &&
                !providerAutoProvision &&
                !provisionedKeysOnly &&
                !treatAsPersonal ? (
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
                <span className="label-text">Description</span>
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
                  Monthly spend limit:{" "}
                  <span className="font-medium">{viewerMonthlyLimitLabel}</span>{" "}
                  (set by your organization).
                </div>
              ) : null}

              {!treatAsPersonal ? (
                <>
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
                        {editorMaxDollars != null
                          ? ` · Editor max $${editorMaxDollars}/day`
                          : null}
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
                    {piiOffRequiresBedrock &&
                      editingKey &&
                      editingKey.provider !== "bedrock" ? (
                      <p className="mt-1.5 text-xs text-warning">
                        Turning PII off requires a Bedrock key. Create a new
                        Bedrock key instead.
                      </p>
                    ) : null}
                  </label>

                  <div className="rounded-xl border border-base-300/70 p-4">
                    <div className="mb-3 text-sm font-medium">Rate limits</div>
                    <p className="mb-3 text-xs text-base-content/60">
                      Optional per-key overrides. Leave blank to inherit global
                      limits. Zero clears an override.
                    </p>
                    <div className="grid gap-3 sm:grid-cols-2">
                      <label className="form-control">
                        <span className="label-text text-xs">
                          Requests / minute
                        </span>
                        <input
                          type="number"
                          min="0"
                          className="input input-bordered input-sm w-full"
                          placeholder="inherit"
                          value={form.rate_limit_rpm}
                          onChange={(event) =>
                            setForm((current) => ({
                              ...current,
                              rate_limit_rpm: event.target.value,
                            }))
                          }
                        />
                      </label>
                      <label className="form-control">
                        <span className="label-text text-xs">
                          Tokens / minute
                        </span>
                        <input
                          type="number"
                          min="0"
                          className="input input-bordered input-sm w-full"
                          placeholder="inherit"
                          value={form.rate_limit_tpm}
                          onChange={(event) =>
                            setForm((current) => ({
                              ...current,
                              rate_limit_tpm: event.target.value,
                            }))
                          }
                        />
                      </label>
                      <label className="form-control">
                        <span className="label-text text-xs">
                          Requests / day
                        </span>
                        <input
                          type="number"
                          min="0"
                          className="input input-bordered input-sm w-full"
                          placeholder="inherit"
                          value={form.rate_limit_rpd}
                          onChange={(event) =>
                            setForm((current) => ({
                              ...current,
                              rate_limit_rpd: event.target.value,
                            }))
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
                            setForm((current) => ({
                              ...current,
                              rate_limit_tpd: event.target.value,
                            }))
                          }
                        />
                      </label>
                    </div>
                  </div>
                </>
              ) : null}

              <div className="modal-action">
                <button
                  type="button"
                  className="btn btn-ghost"
                  onClick={closeModal}
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="btn btn-primary"
                  disabled={
                    saving ||
                    (!editingKey &&
                      (provisionedKeysOnly || treatAsPersonal) &&
                      !useAutoProvision)
                  }
                >
                  {saving ? (
                    <span className="loading loading-spinner loading-sm" />
                  ) : null}
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
                {formatShareExpiry(shareResult.expires_at).message}. Re-sharing
                the same key within 24 hours reuses this URL.
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
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => setShareResult(null)}
              >
                Done
              </button>
            </div>
          </div>
          <form method="dialog" className="modal-backdrop">
            <button
              type="button"
              aria-label="Close"
              onClick={() => setShareResult(null)}
            />
          </form>
        </dialog>
      ) : null}

      {deleteTarget ? (
        <dialog className="modal modal-open" open>
          <div className="modal-box">
            <h3 className="text-lg font-semibold">Delete API key?</h3>
            <p className="py-4 text-sm text-base-content/70">
              This will permanently remove{" "}
              <span className="code-chip font-mono">
                {deleteTarget ? maskKey(deleteTarget.key) : ""}
              </span>
              .
            </p>
            <div className="modal-action">
              <button
                type="button"
                className="btn btn-ghost"
                onClick={() => setDeleteTarget(null)}
              >
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
            <button
              type="button"
              aria-label="Close"
              onClick={() => setDeleteTarget(null)}
            />
          </form>
        </dialog>
      ) : null}
    </div>
  );
}
