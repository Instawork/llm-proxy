package permissions

import (
	"strings"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
)

// Permission names a dashboard capability. Route handlers register the minimum
// role via MinRole; business logic uses Can for finer checks inside handlers.
type Permission string

const (
	ViewMonitoring    Permission = "view_monitoring"
	ViewConfig        Permission = "view_config"
	ListKeys          Permission = "list_keys"
	CreateKey         Permission = "create_key"
	GetKey            Permission = "get_key"
	UpdateKey         Permission = "update_key"
	DeleteKey         Permission = "delete_key"
	ShareKey          Permission = "share_key"
	Provisioning      Permission = "provisioning"
	KeyStats          Permission = "key_stats"
	ManageUsers       Permission = "manage_users"
	ManageBYO         Permission = "manage_byo"
	ListKeyRequests   Permission = "list_key_requests"
	CreateKeyRequest  Permission = "create_key_request"
	ReviewKeyRequest  Permission = "review_key_request"
	ListMyKeyRequests Permission = "list_my_key_requests"

	UpdateKeyDescription     Permission = "update_key_description"
	UpdateKeyPolicy          Permission = "update_key_policy"
	DeleteOwnKey             Permission = "delete_own_key"
	DeleteOrgKey             Permission = "delete_org_key"
	PasteProviderKey         Permission = "paste_provider_key"
	ViewAllOrgKeys           Permission = "view_all_org_keys"
	CreateOrgKey             Permission = "create_org_key"
	BulkGeneratePersonalKeys Permission = "bulk_generate_personal_keys"
	BulkGenerateOrgKeys      Permission = "bulk_generate_org_keys"
)

var minRole = map[Permission]adminusers.Role{
	ViewMonitoring:    adminusers.RoleEditor,
	ViewConfig:        adminusers.RoleEditor,
	ListKeys:          adminusers.RoleViewer,
	CreateKey:         adminusers.RoleViewer,
	GetKey:            adminusers.RoleViewer,
	UpdateKey:         adminusers.RoleViewer,
	DeleteKey:         adminusers.RoleViewer,
	ShareKey:          adminusers.RoleViewer,
	Provisioning:      adminusers.RoleViewer,
	KeyStats:          adminusers.RoleViewer,
	ManageUsers:       adminusers.RoleAdmin,
	ManageBYO:         adminusers.RoleAdmin,
	ListKeyRequests:   adminusers.RoleAdmin,
	CreateKeyRequest:  adminusers.RoleViewer,
	ReviewKeyRequest:  adminusers.RoleAdmin,
	ListMyKeyRequests: adminusers.RoleViewer,
}

// MinRole returns the minimum role required to reach a route guarded by p.
func MinRole(p Permission) adminusers.Role {
	if r, ok := minRole[p]; ok {
		return r
	}
	return adminusers.RoleAdmin
}

// Can reports whether role may exercise a capability (may be stricter than MinRole).
func Can(role adminusers.Role, p Permission) bool {
	switch p {
	case ViewMonitoring, ViewConfig, UpdateKeyPolicy, ViewAllOrgKeys, CreateOrgKey:
		return role.AtLeast(adminusers.RoleEditor)
	case PasteProviderKey, DeleteOrgKey, ManageUsers, ManageBYO, ReviewKeyRequest,
		ListKeyRequests:
		return role == adminusers.RoleAdmin
	case BulkGeneratePersonalKeys:
		return role == adminusers.RoleViewer || role == adminusers.RoleAdmin
	case BulkGenerateOrgKeys:
		return role == adminusers.RoleEditor
	case CreateKeyRequest:
		return role == adminusers.RoleViewer || role == adminusers.RoleEditor
	case ListKeys, CreateKey, GetKey, UpdateKey, ShareKey, Provisioning, KeyStats,
		ListMyKeyRequests, UpdateKeyDescription, DeleteOwnKey:
		return role.AtLeast(adminusers.RoleViewer)
	default:
		return false
	}
}

// CanAccessKey reports whether the user may read or mutate a specific key record.
func CanAccessKey(role adminusers.Role, userEmail string, key *apikeys.APIKey) bool {
	if role.AtLeast(adminusers.RoleEditor) {
		return true
	}
	if key == nil || strings.TrimSpace(key.OwnerEmail) == "" {
		return false
	}
	return strings.EqualFold(key.OwnerEmail, userEmail)
}

// CanDeleteKey reports whether the user may delete the key.
func CanDeleteKey(role adminusers.Role, userEmail string, key *apikeys.APIKey) bool {
	if !CanAccessKey(role, userEmail, key) {
		return false
	}
	if Can(role, DeleteOrgKey) {
		return true
	}
	if role != adminusers.RoleViewer {
		return false
	}
	return Can(role, DeleteOwnKey) && key != nil && strings.TrimSpace(key.OwnerEmail) != ""
}

// CanCreateKeyRequest reports whether the role may submit an org key request (not admins).
func CanCreateKeyRequest(role adminusers.Role) bool {
	return Can(role, CreateKeyRequest)
}

// RequiresAutoProvision reports whether create must use auto_provision (no pasted keys).
func RequiresAutoProvision(role adminusers.Role) bool {
	return !Can(role, PasteProviderKey)
}

// UpdateKeyPolicyFields reports fields a role may set on PATCH /keys/{key}.
func UpdateKeyPolicyFieldsAllowed(role adminusers.Role) bool {
	return Can(role, UpdateKeyPolicy)
}
