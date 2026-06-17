package adminusers

import (
	"fmt"
	"strings"
)

// Role is an admin dashboard permission level.
type Role string

const (
	RoleViewer Role = "viewer"
	RoleEditor Role = "editor"
	RoleAdmin  Role = "admin"
)

// ParseRole validates and normalizes a role string.
func ParseRole(s string) (Role, error) {
	switch Role(strings.ToLower(strings.TrimSpace(s))) {
	case RoleViewer, RoleEditor, RoleAdmin:
		return Role(strings.ToLower(strings.TrimSpace(s))), nil
	default:
		return "", fmt.Errorf("invalid role %q: must be admin, editor, or viewer", s)
	}
}

// AtLeast reports whether r meets or exceeds min.
func (r Role) AtLeast(min Role) bool {
	return roleRank(r) >= roleRank(min)
}

func roleRank(r Role) int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}
