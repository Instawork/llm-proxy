import { FormEvent, useState } from "react";

import { useAddKeyViewers, useKeyViewers, useRemoveKeyViewer } from "../../hooks/queries";
import { useToast } from "../ui/toast";
import { ErrorAlert } from "../ui/page-header";

const EMAIL_PATTERN = /^[^\s@,;]+@[^\s@,;]+\.[^\s@,;]+$/;

/** Splits pasted input on commas, semicolons, and whitespace. */
export function parseEmailInput(raw: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const part of raw.split(/[\s,;]+/)) {
    const email = part.trim().toLowerCase();
    if (email && !seen.has(email)) {
      seen.add(email);
      out.push(email);
    }
  }
  return out;
}

export function invalidEmails(emails: string[]): string[] {
  return emails.filter((e) => !EMAIL_PATTERN.test(e));
}

/**
 * Editor/admin control to grant or revoke read-only access to one key. Viewers
 * see masked metadata and spend only; the requester is always an implicit viewer.
 */
export default function KeyViewersPanel({ routeKey }: { routeKey: string }) {
  const { push } = useToast();
  const viewersQuery = useKeyViewers(routeKey);
  const add = useAddKeyViewers(routeKey);
  const remove = useRemoveKeyViewer(routeKey);
  const [input, setInput] = useState("");
  const [inputError, setInputError] = useState<string | null>(null);

  const viewers = viewersQuery.data?.viewers ?? [];
  const requester = viewersQuery.data?.requester_email;
  const busy = add.isPending || remove.isPending;

  const onAdd = async (event: FormEvent) => {
    event.preventDefault();
    const emails = parseEmailInput(input);
    if (emails.length === 0) {
      setInputError("Enter at least one email address.");
      return;
    }
    const bad = invalidEmails(emails);
    if (bad.length > 0) {
      setInputError(`Not a valid email: ${bad.join(", ")}`);
      return;
    }
    setInputError(null);
    try {
      await add.mutateAsync(emails);
      setInput("");
      push(emails.length === 1 ? `Added ${emails[0]} as a viewer` : `Added ${emails.length} viewers`, "success");
    } catch (err) {
      setInputError(err instanceof Error ? err.message : "Failed to add viewers");
    }
  };

  const onRemove = async (email: string) => {
    try {
      await remove.mutateAsync(email);
      push(`Removed ${email}`, "success");
    } catch (err) {
      push(err instanceof Error ? err.message : "Failed to remove viewer", "error");
    }
  };

  return (
    <section className="glass-panel space-y-4 p-5" aria-labelledby="key-viewers-heading">
      <div>
        <h3 id="key-viewers-heading" className="font-semibold">
          Viewers
        </h3>
        <p className="mt-1 text-sm text-base-content/60">
          People who can see this key&apos;s ID, limits, and spend. They cannot see the key itself
          or change anything.
        </p>
      </div>

      {viewersQuery.error ? (
        <ErrorAlert
          message={viewersQuery.error instanceof Error ? viewersQuery.error.message : "Failed to load viewers"}
        />
      ) : null}

      <ul className="flex flex-wrap gap-2" aria-label="Current viewers">
        {requester ? (
          <li
            className="badge badge-outline badge-lg gap-1"
            title="The requester of this key can always view it"
          >
            {requester}
            <span className="text-xs text-base-content/50">requester</span>
          </li>
        ) : null}
        {viewers.map((email) => (
          <li key={email} className="badge badge-lg gap-2">
            {email}
            <button
              type="button"
              className="btn btn-ghost btn-xs h-auto min-h-0 p-0"
              aria-label={`Remove ${email}`}
              disabled={busy}
              onClick={() => void onRemove(email)}
            >
              ✕
            </button>
          </li>
        ))}
        {!viewersQuery.isPending && viewers.length === 0 && !requester ? (
          <li className="text-sm text-base-content/50">No viewers yet.</li>
        ) : null}
      </ul>

      <form className="space-y-2" onSubmit={(e) => void onAdd(e)}>
        <label className="form-control w-full">
          <span className="label-text mb-1 text-sm">Add viewers by email</span>
          <div className="flex flex-wrap gap-2">
            <input
              type="text"
              className={`input input-bordered input-sm min-w-64 flex-1 ${inputError ? "input-error" : ""}`}
              placeholder="name@company.com, other@company.com"
              value={input}
              onChange={(e) => {
                setInput(e.target.value);
                setInputError(null);
              }}
              disabled={busy}
              aria-invalid={Boolean(inputError)}
            />
            <button type="submit" className="btn btn-primary btn-sm" disabled={busy || input.trim() === ""}>
              {add.isPending ? <span className="loading loading-spinner loading-xs" /> : null}
              Add viewer
            </button>
          </div>
        </label>
        {inputError ? (
          <p role="alert" className="text-sm text-error">
            {inputError}
          </p>
        ) : null}
      </form>
    </section>
  );
}
