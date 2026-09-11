import { FormEvent, useState } from "react";

import {
  CostFields,
  ExpiryField,
  KeyStatusToggle,
  PiiFields,
  RateLimitFields,
} from "./key-form-fields";
import {
  formPiiOffRequiresBedrock,
  keyFormFromRecord,
  keyUpdateFromForm,
  modalTabClass,
  type KeyFormState,
  type KeyFormTab,
} from "../../lib/key-form";
import { isPersonalKey } from "../../lib/format";
import { permissions } from "../../lib/permissions";
import { useConfig, useMe, useUpdateKey } from "../../hooks/queries";
import type { APIKey, UpdateAPIKeyRequest } from "../../types";
import { useToast } from "../ui/toast";
import { ProviderBadge } from "../ui/page-header";
import PiiOffConfirmDialog, { needsPiiOffConfirmation } from "./pii-off-confirm-dialog";

type EditKeyModalProps = {
  keyRecord: APIKey;
  routeKey: string;
  initialTab?: KeyFormTab;
  onClose: () => void;
};

export default function EditKeyModal({
  keyRecord,
  routeKey,
  initialTab = "general",
  onClose,
}: EditKeyModalProps) {
  const { push } = useToast();
  const { data: me } = useMe();
  const { data: config } = useConfig();
  const updateKey = useUpdateKey();

  const canManagePolicy = permissions.canManageKeyPolicy(me?.role);
  const isPersonal = isPersonalKey(keyRecord);
  const globalPiiEnabled = Boolean(config?.features?.pii_redact);
  const canBypassPiiBedrockPolicy = Boolean(me?.can_bypass_pii_off_non_bedrock_policy);
  const editorMaxCents = me?.editor_limits?.max_daily_cost_limit_cents ?? 0;
  const editorMaxDollars = editorMaxCents > 0 ? editorMaxCents / 100 : null;

  const [form, setForm] = useState<KeyFormState>(() => keyFormFromRecord(keyRecord, "metered"));
  const [tab, setTab] = useState<KeyFormTab>(
    canManagePolicy && !isPersonal ? initialTab : "general",
  );
  const [pendingPiiOffUpdate, setPendingPiiOffUpdate] = useState<UpdateAPIKeyRequest | null>(null);

  const piiOffRequiresBedrock = formPiiOffRequiresBedrock(
    form.redact_pii,
    globalPiiEnabled,
    canBypassPiiBedrockPolicy,
  );

  const policyTabs: KeyFormTab[] = canManagePolicy && !isPersonal
    ? ["cost", "pii", "rate-limits"]
    : [];
  const visibleTabs: KeyFormTab[] = ["general", ...policyTabs];

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    const update = keyUpdateFromForm(keyRecord, form);

    if (update.monthly_cost_limit !== undefined || update.daily_cost_limit !== undefined) {
      const activeCostLimit =
        form.cost_limit_period === "monthly" ? update.monthly_cost_limit : update.daily_cost_limit;
      if (editorMaxCents > 0 && (activeCostLimit ?? 0) > editorMaxCents) {
        push(
          `${form.cost_limit_period === "monthly" ? "Monthly" : "Daily"} cost limit cannot exceed $${editorMaxDollars}`,
          "error",
        );
        return;
      }
    }

    if (Object.keys(update).length === 0) {
      onClose();
      return;
    }

    if (needsPiiOffConfirmation(keyRecord.provider, update.redact_pii ?? null)) {
      setPendingPiiOffUpdate(update);
      return;
    }
    await submitUpdate(update);
  };

  const submitUpdate = async (update: UpdateAPIKeyRequest) => {
    try {
      await updateKey.mutateAsync({ key: routeKey, body: update });
      push("Key updated", "success");
      onClose();
    } catch (err) {
      push(err instanceof Error ? err.message : "Failed to update key", "error");
    }
  };

  return (
    <dialog className="modal modal-open" open>
      <div className="modal-box max-w-2xl">
        <h3 className="text-lg font-semibold">Edit API key</h3>

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
              <label className="form-control w-full">
                <span className="label-text mb-1.5">Provider</span>
                <ProviderBadge provider={keyRecord.provider} />
              </label>

              <label className="form-control w-full">
                <span className="label-text mb-1.5">Name</span>
                <textarea
                  className="textarea textarea-bordered w-full"
                  rows={2}
                  placeholder="What is this key used for?"
                  value={form.description}
                  disabled={isPersonal}
                  onChange={(event) =>
                    setForm((current) => ({
                      ...current,
                      description: event.target.value,
                    }))
                  }
                />
                {isPersonal ? (
                  <span className="label-text-alt mt-1 text-base-content/60">
                    Personal keys can&apos;t be renamed.
                  </span>
                ) : null}
              </label>

              {canManagePolicy ? <ExpiryField form={form} setForm={setForm} /> : null}

              {canManagePolicy && !isPersonal ? (
                <KeyStatusToggle form={form} setForm={setForm} />
              ) : null}
            </>
          ) : null}

          {tab === "cost" && canManagePolicy && !isPersonal ? (
            <CostFields
              form={form}
              setForm={setForm}
              editorMaxDollars={editorMaxDollars}
              showEnabled={false}
            />
          ) : null}

          {tab === "pii" && canManagePolicy && !isPersonal ? (
            <PiiFields
              form={form}
              setForm={setForm}
              editingKey={keyRecord}
              piiOffRequiresBedrock={piiOffRequiresBedrock}
            />
          ) : null}

          {tab === "rate-limits" && canManagePolicy && !isPersonal ? (
            <RateLimitFields form={form} setForm={setForm} />
          ) : null}

          <div className="modal-action">
            <button type="button" className="btn btn-ghost" onClick={onClose}>
              Cancel
            </button>
            <button type="submit" className="btn btn-primary" disabled={updateKey.isPending}>
              {updateKey.isPending ? <span className="loading loading-spinner loading-sm" /> : null}
              Save changes
            </button>
          </div>
        </form>
      </div>
      <form method="dialog" className="modal-backdrop">
        <button type="button" aria-label="Close" onClick={onClose} />
      </form>
      {pendingPiiOffUpdate ? (
        <PiiOffConfirmDialog
          provider={keyRecord.provider}
          pending={updateKey.isPending}
          onCancel={() => setPendingPiiOffUpdate(null)}
          onConfirm={() => void submitUpdate(pendingPiiOffUpdate)}
        />
      ) : null}
    </dialog>
  );
}
