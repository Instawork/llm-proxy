import { isPersonalKey } from "./format";
import { KEY_PROVIDERS } from "./key-form";
import type { APIKey, Provider } from "../types";

export interface KeyLeafRow {
  kind: "key";
  id: string;
  key: APIKey;
}

/**
 * Two or more personal keys sharing an owner (case-insensitive) and a trimmed name, one per
 * provider. A would-be stack of one is returned as a KeyLeafRow instead, so "leaf ⇒ one key"
 * is structural rather than a render-time check.
 */
export interface KeyStackRow {
  kind: "stack";
  id: string;
  name: string;
  ownerEmail: string;
  keys: APIKey[];
  subRows: KeyLeafRow[];
  providers: Provider[];
  enabledCount: number;
  created_at: string;
}

export type KeyRow = KeyLeafRow | KeyStackRow;

export interface KeyDisplayOptions {
  /** false → every key renders as a leaf. Default true. */
  stack?: boolean;
}

function providerRank(provider: Provider): number {
  const index = KEY_PROVIDERS.indexOf(provider);
  return index === -1 ? KEY_PROVIDERS.length : index;
}

function leaf(key: APIKey): KeyLeafRow {
  return { kind: "key", id: key.key, key };
}

function groupKey(key: APIKey): string | null {
  if (!isPersonalKey(key)) return null;
  const owner = key.owner_email?.trim().toLowerCase();
  const name = key.description?.trim();
  if (!owner || !name) return null;
  return `${owner}\u0000${name}`;
}

function buildStack(ownerEmail: string, name: string, keys: APIKey[]): KeyStackRow {
  const sorted = [...keys].sort((a, b) => {
    const rank = providerRank(a.provider) - providerRank(b.provider);
    if (rank !== 0) return rank;
    return a.created_at.localeCompare(b.created_at);
  });
  const providers = [...new Set(sorted.map((k) => k.provider))].sort(
    (a, b) => providerRank(a) - providerRank(b),
  );
  return {
    kind: "stack",
    id: `stack:${ownerEmail}:${name}`,
    name,
    ownerEmail,
    keys: sorted,
    subRows: sorted.map(leaf),
    providers,
    enabledCount: sorted.filter((k) => k.enabled).length,
    created_at: sorted.reduce((earliest, k) => (k.created_at < earliest ? k.created_at : earliest), sorted[0].created_at),
  };
}

/**
 * Turns a flat key list into the table's row tree. Personal keys (per `isPersonalKey`) with a
 * non-empty owner email and a non-empty trimmed name are grouped by owner + name; groups of two
 * or more become stacks positioned at their first key's index. Everything else, including
 * non-personal keys and singleton groups, stays a leaf in input order.
 */
export function keyDisplayRows(keys: APIKey[], options?: KeyDisplayOptions): KeyRow[] {
  if (options?.stack === false) {
    return keys.map(leaf);
  }

  const groups = new Map<string, APIKey[]>();
  const order: string[] = [];
  const rows: (KeyRow | null)[] = [];
  const groupRowIndex = new Map<string, number>();

  for (const key of keys) {
    const group = groupKey(key);
    if (!group) {
      rows.push(leaf(key));
      continue;
    }
    if (!groups.has(group)) {
      groups.set(group, []);
      order.push(group);
      groupRowIndex.set(group, rows.length);
      rows.push(null); // placeholder, filled once the group's size is known
    }
    groups.get(group)!.push(key);
  }

  for (const group of order) {
    const members = groups.get(group)!;
    const index = groupRowIndex.get(group)!;
    if (members.length < 2) {
      rows[index] = leaf(members[0]);
      continue;
    }
    const [ownerEmail, name] = group.split("\u0000");
    rows[index] = buildStack(ownerEmail, name, members);
  }

  return rows as KeyRow[];
}

/** The shared value when `pick` agrees for every key, otherwise `fallback`. */
export function uniformOr<T, F>(keys: readonly APIKey[], pick: (key: APIKey) => T, fallback: F): T | F {
  const [first, ...rest] = keys.map(pick);
  return rest.every((value) => value === first) ? first : fallback;
}
