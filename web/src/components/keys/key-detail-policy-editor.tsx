import { FormEvent, useEffect, useState } from "react";

import {
  costLimitsFromForm,
  formPiiOffRequiresBedrock,
  keyFormFromRecord,
  piiFromFormValue,
  rateLimitsFromForm,
  type KeyFormState,
} from "../../lib/key-form";
import { useConfig, useMe, useUpdateKey } from "../../hooks/queries";
import type { APIKey } from "../../types";
import { useToast } from "../ui/toast";
import { CostFields, PiiFields, RateLimitFields } from "./api-keys-modal";

type KeyDetailPolicyEditorProps = {
  keyRecord: APIKey;
  routeKey: string;
  section: "cost" | "pii" | "rate-limits";
  editorMaxDollars: number | null;
};

export default function KeyDetailPolicyEditor({
  keyRecord,
  routeKey,
  section,
  editorMaxDollars,
}: KeyDetailPolicyEditorProps) {
  const { push } = useToast();
  const updateKey = useUpdateKey();
  const { data: me } = useMe();
  const { data: config } = useConfig();
  const globalPiiEnabled = Boolean(config?.features?.pii_redact);
  const canBypassPiiBedrockPolicy = Boolean(me?.can_bypass_pii_off_non_bedrock_policy);

  const [editing, setEditing] = useState(false);
  const [form, setForm] = useState<KeyFormState>(() =>
    keyFormFromRecord(keyRecord, "metered"),
  );

  useEffect(() => {
    if (!editing) {
      setForm(keyFormFromRecord(keyRecord, "metered"));
    }
  }, [keyRecord, editing]);

  const piiOffRequiresBedrock = formPiiOffRequiresBedrock(
    form.redact_pii,
    globalPiiEnabled,
    canBypassPiiBedrockPolicy,
  );

  const onSave = async (event: FormEvent) => {
    event.preventDefault();
    const { daily_cost_limit: dailyCostLimit, monthly_cost_limit: monthlyCostLimit } =
      costLimitsFromForm(form);
    const editorMaxCents = me?.editor_limits?.max_daily_cost_limit_cents ?? 0;
    const activeCostLimit =
      form.cost_limit_period === "monthly" ? monthlyCostLimit : dailyCostLimit;
    if (section === "cost" && editorMaxCents > 0 && activeCostLimit > editorMaxCents) {
      push(
        `${form.cost_limit_period === "monthly" ? "Monthly" : "Daily"} cost limit cannot exceed $${editorMaxDollars ?? editorMaxCents / 100}`,
        "error",
      );
      return;
    }

    try {
      if (section === "cost") {
        await updateKey.mutateAsync({
          key: routeKey,
          body: {
            daily_cost_limit: dailyCostLimit,
            monthly_cost_limit: monthlyCostLimit,
          },
        });
      } else if (section === "pii") {
        await updateKey.mutateAsync({
          key: routeKey,
          body: { redact_pii: piiFromFormValue(form.redact_pii) },
        });
      } else {
        await updateKey.mutateAsync({
          key: routeKey,
          body: rateLimitsFromForm(form),
        });
      }
      push("Key settings updated", "success");
      setEditing(false);
    } catch (err) {
      push(err instanceof Error ? err.message : "Failed to update key", "error");
    }
  };

  const onCancel = () => {
    setForm(keyFormFromRecord(keyRecord, "metered"));
    setEditing(false);
  };

  if (!editing) {
    return (
      <div className="flex justify-end border-b border-base-300/70 px-5 py-3">
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => setEditing(true)}>
          Edit settings
        </button>
      </div>
    );
  }

  return (
    <form className="border-b border-base-300/70 bg-base-200/30 p-5" onSubmit={onSave}>
      {section === "cost" ? (
        <CostFields
          form={form}
          setForm={setForm}
          editorMaxDollars={editorMaxDollars}
          showEnabled={false}
        />
      ) : null}
      {section === "pii" ? (
        <PiiFields
          form={form}
          setForm={setForm}
          editingKey={keyRecord}
          piiOffRequiresBedrock={piiOffRequiresBedrock}
        />
      ) : null}
      {section === "rate-limits" ? <RateLimitFields form={form} setForm={setForm} /> : null}
      <div className="mt-4 flex flex-wrap gap-2">
        <button type="submit" className="btn btn-primary btn-sm" disabled={updateKey.isPending}>
          {updateKey.isPending ? <span className="loading loading-spinner loading-xs" /> : null}
          Save
        </button>
        <button type="button" className="btn btn-ghost btn-sm" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}
