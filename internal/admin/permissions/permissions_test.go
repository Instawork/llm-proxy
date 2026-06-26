package permissions

import (
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/stretchr/testify/assert"
)

func TestMinRole(t *testing.T) {
	assert.Equal(t, adminusers.RoleEditor, MinRole(ViewMonitoring))
	assert.Equal(t, adminusers.RoleViewer, MinRole(ListKeys))
	assert.Equal(t, adminusers.RoleAdmin, MinRole(ManageUsers))
}

func TestCanUpdateKeyPolicy(t *testing.T) {
	assert.False(t, Can(adminusers.RoleViewer, UpdateKeyPolicy))
	assert.True(t, Can(adminusers.RoleEditor, UpdateKeyPolicy))
	assert.True(t, Can(adminusers.RoleAdmin, UpdateKeyPolicy))
}

func TestCanPasteProviderKey(t *testing.T) {
	assert.False(t, Can(adminusers.RoleEditor, PasteProviderKey))
	assert.True(t, Can(adminusers.RoleAdmin, PasteProviderKey))
}

func TestRequiresAutoProvision(t *testing.T) {
	assert.True(t, RequiresAutoProvision(adminusers.RoleViewer))
	assert.True(t, RequiresAutoProvision(adminusers.RoleEditor))
	assert.False(t, RequiresAutoProvision(adminusers.RoleAdmin))
}

func TestCanDeleteKey(t *testing.T) {
	orgKey := &apikeys.APIKey{OwnerEmail: ""}
	personal := &apikeys.APIKey{OwnerEmail: "viewer@example.com"}

	assert.True(t, CanDeleteKey(adminusers.RoleAdmin, "admin@example.com", orgKey))
	assert.False(t, CanDeleteKey(adminusers.RoleEditor, "editor@example.com", orgKey))
	assert.True(t, CanDeleteKey(adminusers.RoleViewer, "viewer@example.com", personal))
	assert.False(t, CanDeleteKey(adminusers.RoleViewer, "viewer@example.com", orgKey))
}
