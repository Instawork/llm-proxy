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
	SpendOverview     Permission = "spend_overview"
	ManageUsers       Permission = "manage_users"
	ManageBYO         Permission = "manage_byo"
	ListKeyRequests   Permission = "list_key_requests"
	CreateKeyRequest  Permission = "create_key_request"
	ReviewKeyRequest  Permission = "review_key_request"
	ListMyKeyRequests Permission = "list_my_key_requests"
	ManageKeyViewers  Permission = "manage_key_viewers"

	ViewSharedOrgKeys        Permission = "view_shared_org_keys"
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
	SpendOverview:     adminusers.RoleViewer,
	ManageUsers:       adminusers.RoleAdmin,
	ManageBYO:         adminusers.RoleAdmin,
	ListKeyRequests:   adminusers.RoleAdmin,
	CreateKeyRequest:  adminusers.RoleViewer,
	ReviewKeyRequest:  adminusers.RoleAdmin,
	ListMyKeyRequests: adminusers.RoleViewer,
	ManageKeyViewers:  adminusers.RoleEditor,
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
	case ViewMonitoring, ViewConfig, UpdateKeyPolicy, ViewAllOrgKeys, CreateOrgKey, ManageKeyViewers:
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
		SpendOverview, ListMyKeyRequests, UpdateKeyDescription, DeleteOwnKey, ViewSharedOrgKeys:
		return role.AtLeast(adminusers.RoleViewer)
	default:
		return false
	}
}

// KeyAccess is how a caller relates to a key record.
type KeyAccess string

const (
	// AccessNone means the caller may not see the key at all.
	AccessNone KeyAccess = ""
	// AccessManager is editor/admin access to every key.
	AccessManager KeyAccess = "manager"
	// AccessOwner is a viewer's access to their own personal key.
	AccessOwner KeyAccess = "owner"
	// AccessViewer is read-only access to an org-wide key the user was
	// granted (or requested). It never implies any write.
	AccessViewer KeyAccess = "viewer"
)

// AccessToKey classifies the caller's relationship to key.
func AccessToKey(role adminusers.Role, userEmail string, key *apikeys.APIKey) KeyAccess {
	if role.AtLeast(adminusers.RoleEditor) {
		return AccessManager
	}
	if key == nil || !role.AtLeast(adminusers.RoleViewer) {
		return AccessNone
	}
	if owner := strings.TrimSpace(key.OwnerEmail); owner != "" {
		if strings.EqualFold(owner, userEmail) {
			return AccessOwner
		}
		return AccessNone
	}
	if Can(role, ViewSharedOrgKeys) && apikeys.IsKeyViewer(key, userEmail) {
		return AccessViewer
	}
	return AccessNone
}

// CanViewKey reports whether the user may read a specific key record. It is
// strictly wider than CanMutateKey: grantees can read but never write.
func CanViewKey(role adminusers.Role, userEmail string, key *apikeys.APIKey) bool {
	return AccessToKey(role, userEmail, key) != AccessNone
}

// CanMutateKey reports whether the user may modify, delete, or share a
// specific key record.
func CanMutateKey(role adminusers.Role, userEmail string, key *apikeys.APIKey) bool {
	switch AccessToKey(role, userEmail, key) {
	case AccessManager, AccessOwner:
		return true
	default:
		return false
	}
}

// CanRenameKey reports whether key's description may be changed. Personal
// keys keep the name they were created with, since it mirrors the upstream
// credential name.
func CanRenameKey(key *apikeys.APIKey) bool {
	return !apikeys.IsPersonalKey(key)
}

// CanDeleteKey reports whether the user may delete the key.
func CanDeleteKey(role adminusers.Role, userEmail string, key *apikeys.APIKey) bool {
	if !CanMutateKey(role, userEmail, key) {
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
