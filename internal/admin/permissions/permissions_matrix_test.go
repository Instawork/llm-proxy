package permissions

import (
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// canCase documents the role × capability matrix for dashboard permissions.
type canCase struct {
	role       adminusers.Role
	permission Permission
	want       bool
}

func TestCanPermissionMatrix(t *testing.T) {
	cases := []canCase{
		// Viewer: personal keys (not renameable, see CanRenameKey) + key requests; no fleet policy or monitoring.
		{adminusers.RoleViewer, ListKeys, true},
		{adminusers.RoleViewer, CreateKey, true},
		{adminusers.RoleViewer, UpdateKeyDescription, true},
		{adminusers.RoleViewer, CreateKeyRequest, true},
		{adminusers.RoleViewer, ShareKey, true},
		{adminusers.RoleViewer, DeleteOwnKey, true},
		{adminusers.RoleViewer, SpendOverview, true},
		{adminusers.RoleViewer, ViewSharedOrgKeys, true},
		{adminusers.RoleViewer, ManageKeyViewers, false},
		{adminusers.RoleViewer, ViewMonitoring, false},
		{adminusers.RoleViewer, UpdateKeyPolicy, false},
		{adminusers.RoleViewer, ViewAllOrgKeys, false},
		{adminusers.RoleViewer, CreateOrgKey, false},
		{adminusers.RoleViewer, PasteProviderKey, false},
		{adminusers.RoleViewer, DeleteOrgKey, false},
		{adminusers.RoleViewer, ManageUsers, false},
		{adminusers.RoleViewer, ManageBYO, false},
		{adminusers.RoleViewer, ReviewKeyRequest, false},
		{adminusers.RoleViewer, BulkGeneratePersonalKeys, true},
		{adminusers.RoleViewer, BulkGenerateOrgKeys, false},

		// Editor: monitoring + org key policy; no admin governance.
		{adminusers.RoleEditor, ViewMonitoring, true},
		{adminusers.RoleEditor, ViewConfig, true},
		{adminusers.RoleEditor, UpdateKeyPolicy, true},
		{adminusers.RoleEditor, ViewAllOrgKeys, true},
		{adminusers.RoleEditor, ManageKeyViewers, true},
		{adminusers.RoleEditor, CreateOrgKey, true},
		{adminusers.RoleEditor, UpdateKeyDescription, true},
		{adminusers.RoleEditor, PasteProviderKey, false},
		{adminusers.RoleEditor, DeleteOrgKey, false},
		{adminusers.RoleEditor, ManageUsers, false},
		{adminusers.RoleEditor, ManageBYO, false},
		{adminusers.RoleEditor, ReviewKeyRequest, false},
		{adminusers.RoleEditor, BulkGeneratePersonalKeys, false},
		{adminusers.RoleEditor, BulkGenerateOrgKeys, true},
		{adminusers.RoleEditor, CreateKeyRequest, true},

		// Admin: full governance.
		{adminusers.RoleAdmin, UpdateKeyPolicy, true},
		{adminusers.RoleAdmin, PasteProviderKey, true},
		{adminusers.RoleAdmin, DeleteOrgKey, true},
		{adminusers.RoleAdmin, ManageUsers, true},
		{adminusers.RoleAdmin, ManageKeyViewers, true},
		{adminusers.RoleAdmin, ManageBYO, true},
		{adminusers.RoleAdmin, ReviewKeyRequest, true},
		{adminusers.RoleAdmin, ListKeyRequests, true},
		{adminusers.RoleAdmin, BulkGeneratePersonalKeys, true},
		{adminusers.RoleAdmin, BulkGenerateOrgKeys, false},
		{adminusers.RoleAdmin, CreateKeyRequest, false},
	}

	for _, tc := range cases {
		t.Run(string(tc.role)+"/"+string(tc.permission), func(t *testing.T) {
			assert.Equal(t, tc.want, Can(tc.role, tc.permission))
		})
	}
}

func TestMinRoleRouteMatrix(t *testing.T) {
	cases := []struct {
		permission Permission
		want       adminusers.Role
	}{
		{ViewMonitoring, adminusers.RoleEditor},
		{ViewConfig, adminusers.RoleEditor},
		{ListKeys, adminusers.RoleViewer},
		{ManageKeyViewers, adminusers.RoleEditor},
		{CreateKey, adminusers.RoleViewer},
		{SpendOverview, adminusers.RoleViewer},
		{ManageUsers, adminusers.RoleAdmin},
		{ManageBYO, adminusers.RoleAdmin},
		{ListKeyRequests, adminusers.RoleAdmin},
		{ReviewKeyRequest, adminusers.RoleAdmin},
	}

	for _, tc := range cases {
		t.Run(string(tc.permission), func(t *testing.T) {
			assert.Equal(t, tc.want, MinRole(tc.permission))
		})
	}
}

func TestMinRoleRouteReachable(t *testing.T) {
	for permission, min := range minRole {
		t.Run(string(permission), func(t *testing.T) {
			switch min {
			case adminusers.RoleViewer:
				assert.True(t, adminusers.RoleViewer.AtLeast(min))
				assert.True(t, adminusers.RoleEditor.AtLeast(min))
				assert.True(t, adminusers.RoleAdmin.AtLeast(min))
			case adminusers.RoleEditor:
				assert.False(t, adminusers.RoleViewer.AtLeast(min))
				assert.True(t, adminusers.RoleEditor.AtLeast(min))
				assert.True(t, adminusers.RoleAdmin.AtLeast(min))
			case adminusers.RoleAdmin:
				assert.False(t, adminusers.RoleViewer.AtLeast(min))
				assert.False(t, adminusers.RoleEditor.AtLeast(min))
				assert.True(t, adminusers.RoleAdmin.AtLeast(min))
			default:
				t.Fatalf("unexpected min role %q for %q", min, permission)
			}
		})
	}
}

func TestKeyAccessMatrix(t *testing.T) {
	orgKey := &apikeys.APIKey{}
	personal := &apikeys.APIKey{OwnerEmail: "viewer@example.com"}
	otherPersonal := &apikeys.APIKey{OwnerEmail: "other@example.com"}
	shared := &apikeys.APIKey{ViewerEmails: []string{"grantee@example.com"}}
	requested := &apikeys.APIKey{RequesterEmail: "requester@example.com"}
	taggedPersonal := &apikeys.APIKey{Tags: map[string]string{"personal": "true"}, ViewerEmails: []string{"grantee@example.com"}}

	cases := []struct {
		name  string
		role  adminusers.Role
		email string
		key   *apikeys.APIKey
		want  KeyAccess
		view  bool
		write bool
	}{
		{"editor org key", adminusers.RoleEditor, "editor@example.com", orgKey, AccessManager, true, true},
		{"admin org key", adminusers.RoleAdmin, "admin@example.com", orgKey, AccessManager, true, true},
		{"owner personal", adminusers.RoleViewer, "viewer@example.com", personal, AccessOwner, true, true},
		{"owner email case-insensitive", adminusers.RoleViewer, "Viewer@Example.com", personal, AccessOwner, true, true},
		{"other personal", adminusers.RoleViewer, "viewer@example.com", otherPersonal, AccessNone, false, false},
		{"unrelated viewer org key", adminusers.RoleViewer, "viewer@example.com", orgKey, AccessNone, false, false},
		{"listed viewer", adminusers.RoleViewer, "grantee@example.com", shared, AccessViewer, true, false},
		{"listed viewer case-insensitive", adminusers.RoleViewer, "Grantee@Example.com", shared, AccessViewer, true, false},
		{"unlisted viewer on shared key", adminusers.RoleViewer, "viewer@example.com", shared, AccessNone, false, false},
		{"requester implicit", adminusers.RoleViewer, "requester@example.com", requested, AccessViewer, true, false},
		{"grant on personal key is ignored", adminusers.RoleViewer, "grantee@example.com", taggedPersonal, AccessNone, false, false},
		{"nil key", adminusers.RoleViewer, "viewer@example.com", nil, AccessNone, false, false},
		{"empty email", adminusers.RoleViewer, "", requested, AccessNone, false, false},
		{"unknown role", adminusers.Role("bogus"), "grantee@example.com", shared, AccessNone, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, AccessToKey(tc.role, tc.email, tc.key))
			assert.Equal(t, tc.view, CanViewKey(tc.role, tc.email, tc.key))
			assert.Equal(t, tc.write, CanMutateKey(tc.role, tc.email, tc.key))
		})
	}
}

func TestGranteeCannotDeleteKey(t *testing.T) {
	shared := &apikeys.APIKey{ViewerEmails: []string{"grantee@example.com"}}
	assert.False(t, CanDeleteKey(adminusers.RoleViewer, "grantee@example.com", shared))
}

func TestCanDeleteKeyMatrix(t *testing.T) {
	orgKey := &apikeys.APIKey{OwnerEmail: ""}
	personal := &apikeys.APIKey{OwnerEmail: "viewer@example.com"}

	assert.True(t, CanDeleteKey(adminusers.RoleAdmin, "admin@example.com", orgKey))
	assert.False(t, CanDeleteKey(adminusers.RoleEditor, "editor@example.com", orgKey))
	assert.False(t, CanDeleteKey(adminusers.RoleEditor, "editor@example.com", personal))

	assert.True(t, CanDeleteKey(adminusers.RoleViewer, "viewer@example.com", personal))
	assert.False(t, CanDeleteKey(adminusers.RoleViewer, "viewer@example.com", orgKey))
}

func TestCanRenameKeyMatrix(t *testing.T) {
	orgKey := &apikeys.APIKey{OwnerEmail: ""}
	ownedPersonal := &apikeys.APIKey{OwnerEmail: "viewer@example.com"}
	taggedPersonal := &apikeys.APIKey{Tags: map[string]string{"personal": "true"}}

	assert.True(t, CanRenameKey(orgKey))
	assert.False(t, CanRenameKey(ownedPersonal))
	assert.False(t, CanRenameKey(taggedPersonal))
}

func TestUpdateKeyPolicyFieldsAllowedMatrix(t *testing.T) {
	assert.False(t, UpdateKeyPolicyFieldsAllowed(adminusers.RoleViewer))
	assert.True(t, UpdateKeyPolicyFieldsAllowed(adminusers.RoleEditor))
	assert.True(t, UpdateKeyPolicyFieldsAllowed(adminusers.RoleAdmin))
}

func TestRequiresAutoProvisionMatrix(t *testing.T) {
	assert.True(t, RequiresAutoProvision(adminusers.RoleViewer))
	assert.True(t, RequiresAutoProvision(adminusers.RoleEditor))
	assert.False(t, RequiresAutoProvision(adminusers.RoleAdmin))
}

func TestAllRoutePermissionsHaveMinRole(t *testing.T) {
	routePermissions := []Permission{
		ViewMonitoring, ViewConfig, ListKeys, CreateKey, GetKey, UpdateKey, DeleteKey,
		ShareKey, Provisioning, KeyStats, SpendOverview, ManageUsers, ManageBYO, ListKeyRequests,
		CreateKeyRequest, ReviewKeyRequest, ListMyKeyRequests, ManageKeyViewers,
	}
	for _, p := range routePermissions {
		_, ok := minRole[p]
		require.True(t, ok, "permission %q missing from minRole map", p)
	}
}
