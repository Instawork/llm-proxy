import type { AdminRole } from "../types";

import { roleAtLeast, roleAtMost } from "./rbac";

export type NavItem = {
  to: string;
  label: string;
  minRole: AdminRole;
  maxRole?: AdminRole;
};

export const MONITORING_NAV: NavItem[] = [
  { to: "/", label: "Overview", minRole: "editor" },
  { to: "/usage", label: "Usage", minRole: "editor" },
  { to: "/circuit", label: "Circuit Breaker", minRole: "editor" },
  { to: "/rate-limits", label: "Rate Limits", minRole: "editor" },
  { to: "/cost", label: "Cost Tracking", minRole: "editor" },
  { to: "/pii", label: "PII Redaction", minRole: "editor" },
  { to: "/model-status", label: "Model Status", minRole: "editor" },
];

export const MANAGE_NAV: NavItem[] = [
  { to: "/keys", label: "API Keys", minRole: "viewer" },
  { to: "/config", label: "Configuration", minRole: "admin" },
  { to: "/users", label: "Users", minRole: "admin" },
];

export function navItemsForRole(role: AdminRole) {
  const visible = (items: NavItem[]) =>
    items.filter(
      (item) =>
        roleAtLeast(role, item.minRole) &&
        (item.maxRole == null || roleAtMost(role, item.maxRole)),
    );
  return {
    monitoring: visible(MONITORING_NAV),
    manage: visible(MANAGE_NAV),
  };
}

export function defaultPathForRole(role: AdminRole): string {
  if (role === "viewer") {
    return "/keys";
  }
  const { monitoring } = navItemsForRole(role);
  return monitoring[0]?.to ?? "/usage";
}

export function minRoleForPath(pathname: string): AdminRole | null {
  const path = pathname === "/" ? "/" : pathname.replace(/\/$/, "");
  for (const item of [...MONITORING_NAV, ...MANAGE_NAV]) {
    if (item.to === "/") {
      if (path === "/") return item.minRole;
      continue;
    }
    if (path === item.to || path.startsWith(`${item.to}/`)) {
      return item.minRole;
    }
  }
  return null;
}
