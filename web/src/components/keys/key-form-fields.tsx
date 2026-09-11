import { type KeyFormState, tomorrowDateString } from "../../lib/key-form";
import type { APIKey } from "../../types";

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
      {!piiOffRequiresBedrock && form.redact_pii === "off" && form.provider !== "bedrock" ? (
        <p className="mt-1.5 text-xs text-warning">
          Requests on this key will reach the provider with PII unredacted. You will be asked
          to confirm when saving.
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

export function ExpiryField({
  form,
  setForm,
}: {
  form: KeyFormState;
  setForm: React.Dispatch<React.SetStateAction<KeyFormState>>;
}) {
  return (
    <div className="form-control w-full">
      <span className="label-text mb-1.5">Expires</span>
      <div className="flex flex-col gap-2 sm:flex-row">
        <select
          className="select select-bordered w-full sm:w-48"
          value={form.expiry_preset}
          onChange={(event) =>
            setForm((current) => ({
              ...current,
              expiry_preset: event.target.value as KeyFormState["expiry_preset"],
            }))
          }
        >
          <option value="never">Never</option>
          <option value="1d">1 day</option>
          <option value="7d">7 days</option>
          <option value="30d">30 days</option>
          <option value="90d">90 days</option>
          <option value="custom">Custom date</option>
        </select>
        {form.expiry_preset === "custom" ? (
          <input
            type="date"
            className="input input-bordered flex-1"
            min={tomorrowDateString()}
            value={form.expiry_custom}
            onChange={(event) =>
              setForm((current) => ({
                ...current,
                expiry_custom: event.target.value,
              }))
            }
          />
        ) : null}
      </div>
      <span className="label-text-alt mt-1.5 block text-base-content/60">
        After expiry the key stops accepting requests and is deleted a few days later.
      </span>
    </div>
  );
}

export function KeyStatusToggle({
  form,
  setForm,
}: {
  form: KeyFormState;
  setForm: React.Dispatch<React.SetStateAction<KeyFormState>>;
}) {
  return (
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
        <span className="text-sm font-medium">{form.enabled ? "Enabled" : "Disabled"}</span>
      </label>
    </div>
  );
}
