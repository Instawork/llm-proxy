import { describe, expect, it } from "vitest";

import { can, permissions, type Permission } from "./permissions";
import type { AdminRole } from "../types";

type CanCase = {
  role: AdminRole;
  permission: Permission;
  want: boolean;
};

const canMatrix: CanCase[] = [
  { role: "viewer", permission: "list_keys", want: true },
  { role: "viewer", permission: "create_key", want: true },
  { role: "viewer", permission: "update_key_description", want: true },
  { role: "viewer", permission: "create_key_request", want: true },
  { role: "viewer", permission: "share_key", want: true },
  { role: "viewer", permission: "delete_own_key", want: true },
  { role: "viewer", permission: "view_monitoring", want: false },
  { role: "viewer", permission: "update_key_policy", want: false },
  { role: "viewer", permission: "view_all_org_keys", want: false },
  { role: "viewer", permission: "create_org_key", want: false },
  { role: "viewer", permission: "paste_provider_key", want: false },
  { role: "viewer", permission: "delete_org_key", want: false },
  { role: "viewer", permission: "manage_users", want: false },
  { role: "viewer", permission: "manage_byo", want: false },
  { role: "viewer", permission: "review_key_request", want: false },
  { role: "viewer", permission: "bulk_generate_personal_keys", want: true },
  { role: "viewer", permission: "bulk_generate_org_keys", want: false },
  { role: "admin", permission: "create_key_request", want: false },

  { role: "editor", permission: "view_monitoring", want: true },
  { role: "editor", permission: "view_config", want: true },
  { role: "editor", permission: "update_key_policy", want: true },
  { role: "editor", permission: "view_all_org_keys", want: true },
  { role: "editor", permission: "create_org_key", want: true },
  { role: "editor", permission: "update_key_description", want: true },
  { role: "editor", permission: "paste_provider_key", want: false },
  { role: "editor", permission: "delete_org_key", want: false },
  { role: "editor", permission: "manage_users", want: false },
  { role: "editor", permission: "manage_byo", want: false },
  { role: "editor", permission: "review_key_request", want: false },
  { role: "editor", permission: "bulk_generate_personal_keys", want: false },
  { role: "editor", permission: "bulk_generate_org_keys", want: true },
  { role: "editor", permission: "create_key_request", want: true },

  { role: "admin", permission: "update_key_policy", want: true },
  { role: "admin", permission: "paste_provider_key", want: true },
  { role: "admin", permission: "delete_org_key", want: true },
  { role: "admin", permission: "manage_users", want: true },
  { role: "admin", permission: "manage_byo", want: true },
  { role: "admin", permission: "review_key_request", want: true },
  { role: "admin", permission: "list_key_requests", want: true },
  { role: "admin", permission: "bulk_generate_personal_keys", want: true },
  { role: "admin", permission: "bulk_generate_org_keys", want: false },
];

describe("can permission matrix", () => {
  it.each(canMatrix)("$role may $permission = $want", ({ role, permission, want }) => {
    expect(can(role, permission)).toBe(want);
  });

  it("denies undefined role", () => {
    expect(can(undefined, "list_keys")).toBe(false);
  });

  it("admins cannot request service keys", () => {
    expect(permissions.canRequestServiceKey("admin")).toBe(false);
    expect(permissions.canRequestServiceKey("editor")).toBe(true);
    expect(permissions.canRequestServiceKey("viewer")).toBe(true);
  });
});

describe("permissions helpers", () => {
  it("viewer personal-key workflow", () => {
    expect(permissions.isViewer("viewer")).toBe(true);
    expect(permissions.canManageKeyPolicy("viewer")).toBe(false);
    expect(permissions.canDeleteKeys("viewer")).toBe(true);
    expect(permissions.canDeleteOrgKeys("viewer")).toBe(false);
    expect(permissions.requiresProvisionedKeysOnly("viewer")).toBe(true);
    expect(permissions.canRequestServiceKey("viewer")).toBe(true);
    expect(permissions.canBulkGeneratePersonalKeys("viewer")).toBe(true);
    expect(permissions.canBulkGenerateOrgKeys("viewer")).toBe(false);
  });

  it("editor fleet operator workflow", () => {
    expect(permissions.canManageKeyPolicy("editor")).toBe(true);
    expect(permissions.canViewMonitoring("editor")).toBe(true);
    expect(permissions.canPasteProviderKey("editor")).toBe(false);
    expect(permissions.canDeleteKeys("editor")).toBe(false);
    expect(permissions.canDeleteOrgKeys("editor")).toBe(false);
    expect(permissions.canReviewKeyRequests("editor")).toBe(false);
    expect(permissions.requiresProvisionedKeysOnly("editor")).toBe(true);
    expect(permissions.canBulkGeneratePersonalKeys("editor")).toBe(false);
    expect(permissions.canBulkGenerateOrgKeys("editor")).toBe(true);
  });

  it("admin governance workflow", () => {
    expect(permissions.isAdmin("admin")).toBe(true);
    expect(permissions.canManageUsers("admin")).toBe(true);
    expect(permissions.canManageByo("admin")).toBe(true);
    expect(permissions.canPasteProviderKey("admin")).toBe(true);
    expect(permissions.canDeleteKeys("admin")).toBe(true);
    expect(permissions.canDeleteOrgKeys("admin")).toBe(true);
    expect(permissions.canBulkGeneratePersonalKeys("admin")).toBe(true);
    expect(permissions.canBulkGenerateOrgKeys("admin")).toBe(false);
    expect(permissions.canRequestServiceKey("admin")).toBe(false);
    expect(permissions.requiresProvisionedKeysOnly("admin")).toBe(false);
  });
});
