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
		// Viewer: personal keys + rename + key requests; no fleet policy or monitoring.
		{adminusers.RoleViewer, ListKeys, true},
		{adminusers.RoleViewer, CreateKey, true},
		{adminusers.RoleViewer, UpdateKeyDescription, true},
		{adminusers.RoleViewer, CreateKeyRequest, true},
		{adminusers.RoleViewer, ShareKey, true},
		{adminusers.RoleViewer, DeleteOwnKey, true},
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
		{CreateKey, adminusers.RoleViewer},
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

func TestCanAccessKeyMatrix(t *testing.T) {
	orgKey := &apikeys.APIKey{OwnerEmail: ""}
	personal := &apikeys.APIKey{OwnerEmail: "viewer@example.com"}
	otherPersonal := &apikeys.APIKey{OwnerEmail: "other@example.com"}

	assert.True(t, CanAccessKey(adminusers.RoleEditor, "editor@example.com", orgKey))
	assert.True(t, CanAccessKey(adminusers.RoleAdmin, "admin@example.com", orgKey))

	assert.True(t, CanAccessKey(adminusers.RoleViewer, "viewer@example.com", personal))
	assert.False(t, CanAccessKey(adminusers.RoleViewer, "viewer@example.com", orgKey))
	assert.False(t, CanAccessKey(adminusers.RoleViewer, "viewer@example.com", otherPersonal))
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
		ShareKey, Provisioning, KeyStats, ManageUsers, ManageBYO, ListKeyRequests,
		CreateKeyRequest, ReviewKeyRequest, ListMyKeyRequests,
	}
	for _, p := range routePermissions {
		_, ok := minRole[p]
		require.True(t, ok, "permission %q missing from minRole map", p)
	}
}
