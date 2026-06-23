import type { AdminRole } from "../types";

const ROLE_RANK: Record<AdminRole, number> = {
  viewer: 1,
  editor: 2,
  admin: 3,
};

export function roleAtLeast(role: AdminRole, min: AdminRole): boolean {
  return ROLE_RANK[role] >= ROLE_RANK[min];
}

export function roleAtMost(role: AdminRole, max: AdminRole): boolean {
  return ROLE_RANK[role] <= ROLE_RANK[max];
}

export function canManageByoBans(role: AdminRole | undefined): boolean {
  return role === "admin";
}
