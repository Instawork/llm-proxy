import type { AdminRole } from "../types";

import { roleAtLeast } from "./rbac";

export type Permission =
  | "view_monitoring"
  | "view_config"
  | "list_keys"
  | "create_key"
  | "get_key"
  | "update_key"
  | "delete_key"
  | "share_key"
  | "provisioning"
  | "key_stats"
  | "manage_users"
  | "manage_byo"
  | "list_key_requests"
  | "create_key_request"
  | "review_key_request"
  | "list_my_key_requests"
  | "update_key_description"
  | "update_key_policy"
  | "delete_own_key"
  | "delete_org_key"
  | "paste_provider_key"
  | "view_all_org_keys"
  | "create_org_key"
  | "bulk_generate_personal_keys"
  | "bulk_generate_org_keys";

const EDITOR_PLUS: Permission[] = [
  "view_monitoring",
  "view_config",
  "update_key_policy",
  "view_all_org_keys",
  "create_org_key",
];

const ADMIN_ONLY: Permission[] = [
  "paste_provider_key",
  "delete_org_key",
  "manage_users",
  "manage_byo",
  "review_key_request",
  "list_key_requests",
];

const PERSONAL_BULK: Permission[] = ["bulk_generate_personal_keys"];

const ORG_BULK: Permission[] = ["bulk_generate_org_keys"];

const VIEWER_PLUS: Permission[] = [
  "list_keys",
  "create_key",
  "get_key",
  "update_key",
  "share_key",
  "provisioning",
  "key_stats",
  "create_key_request",
  "list_my_key_requests",
  "update_key_description",
  "delete_own_key",
];

const adminOnly = new Set<Permission>(ADMIN_ONLY);
const personalBulk = new Set<Permission>(PERSONAL_BULK);
const orgBulk = new Set<Permission>(ORG_BULK);
const editorPlus = new Set<Permission>(EDITOR_PLUS);
const viewerPlus = new Set<Permission>(VIEWER_PLUS);

export function can(role: AdminRole | undefined, permission: Permission): boolean {
  if (!role) return false;
  if (adminOnly.has(permission)) {
    return role === "admin";
  }
  if (personalBulk.has(permission)) {
    return role === "viewer" || role === "admin";
  }
  if (orgBulk.has(permission)) {
    return role === "editor";
  }
  if (permission === "create_key_request") {
    return role === "viewer" || role === "editor";
  }
  if (editorPlus.has(permission)) {
    return roleAtLeast(role, "editor");
  }
  if (viewerPlus.has(permission)) {
    return roleAtLeast(role, "viewer");
  }
  return false;
}

export const permissions = {
  can,
  isViewer: (role: AdminRole | undefined) => role === "viewer",
  isEditor: (role: AdminRole | undefined) => role === "editor",
  isAdmin: (role: AdminRole | undefined) => role === "admin",
  canViewMonitoring: (role: AdminRole | undefined) => can(role, "view_monitoring"),
  canManageKeyPolicy: (role: AdminRole | undefined) => can(role, "update_key_policy"),
  canPasteProviderKey: (role: AdminRole | undefined) => can(role, "paste_provider_key"),
  canDeleteKeys: (role: AdminRole | undefined) =>
    (can(role, "delete_own_key") && role === "viewer") || can(role, "delete_org_key"),
  canDeleteOrgKeys: (role: AdminRole | undefined) => can(role, "delete_org_key"),
  canManageUsers: (role: AdminRole | undefined) => can(role, "manage_users"),
  canManageByo: (role: AdminRole | undefined) => can(role, "manage_byo"),
  canReviewKeyRequests: (role: AdminRole | undefined) => can(role, "review_key_request"),
  canRequestServiceKey: (role: AdminRole | undefined) =>
    can(role, "create_key_request"),
  requiresProvisionedKeysOnly: (role: AdminRole | undefined) => !can(role, "paste_provider_key"),
  canBulkGeneratePersonalKeys: (role: AdminRole | undefined) =>
    can(role, "bulk_generate_personal_keys"),
  canBulkGenerateOrgKeys: (role: AdminRole | undefined) =>
    can(role, "bulk_generate_org_keys"),
  canManageConfigPage: (role: AdminRole | undefined) => role === "admin",
};

export function canManageByoBans(role: AdminRole | undefined): boolean {
  return permissions.canManageByo(role);
}

export function canManageKeyPolicy(role: AdminRole | undefined): boolean {
  return permissions.canManageKeyPolicy(role);
}
