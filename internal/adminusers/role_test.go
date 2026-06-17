package adminusers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRole(t *testing.T) {
	role, err := ParseRole(" Admin ")
	require.NoError(t, err)
	assert.Equal(t, RoleAdmin, role)

	_, err = ParseRole("superuser")
	require.Error(t, err)
}

func TestRoleAtLeast(t *testing.T) {
	assert.True(t, RoleAdmin.AtLeast(RoleViewer))
	assert.True(t, RoleAdmin.AtLeast(RoleEditor))
	assert.True(t, RoleEditor.AtLeast(RoleViewer))
	assert.False(t, RoleViewer.AtLeast(RoleEditor))
	assert.False(t, Role("").AtLeast(RoleViewer))
}
