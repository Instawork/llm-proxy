import type { AdminRole } from "../types";

const ROLE_RANK: Record<AdminRole, number> = {
  viewer: 1,
  editor: 2,
  admin: 3,
};

export function roleAtLeast(role: AdminRole, min: AdminRole): boolean {
  return ROLE_RANK[role] >= ROLE_RANK[min];
}
