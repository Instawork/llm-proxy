package admin

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/config"
)

func (a *authenticator) userRole(r *http.Request) (adminusers.Role, error) {
	user, err := a.currentUser(r)
	if err != nil {
		return "", err
	}
	role, err := adminusers.ParseRole(user.Role)
	if err != nil {
		return adminusers.RoleViewer, nil
	}
	return role, nil
}

func (a *authenticator) requireRole(min adminusers.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role, err := a.userRole(r)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			if !role.AtLeast(min) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isAllowedDomainEmail(email, allowedDomain string) bool {
	email = strings.TrimSpace(email)
	allowedDomain = strings.TrimSpace(allowedDomain)
	if email == "" || allowedDomain == "" {
		return false
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	return strings.EqualFold(email[at+1:], allowedDomain)
}

func validateAllowedEmail(email, allowedDomain string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !strings.Contains(email, "@") {
		return fmt.Errorf("invalid email")
	}
	if !isAllowedDomainEmail(email, allowedDomain) {
		return fmt.Errorf("email domain not allowed")
	}
	return nil
}

func userRecordFromStore(u adminusers.User) AdminUserRecord {
	return AdminUserRecord{
		Email:       u.Email,
		Name:        u.Name,
		Picture:     u.Picture,
		Role:        string(u.Role),
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
		LastLoginAt: u.LastLoginAt,
	}
}

func (h *handler) editorMaxDailyCostCents() int64 {
	if h.deps.YAMLConfig == nil {
		return 0
	}
	return h.deps.YAMLConfig.Features.AdminDashboard.EditorLimits.MaxDailyCostLimitCents
}

func viewerPersonalMonthlyLimitFromConfig(cfg config.ViewerLimitsConfig) int64 {
	if cfg.PersonalMonthlyCostLimitCents > 0 {
		return cfg.PersonalMonthlyCostLimitCents
	}
	return 2000
}

func (h *handler) viewerPersonalMonthlyLimit() int64 {
	if h.deps.YAMLConfig == nil {
		return 2000
	}
	return viewerPersonalMonthlyLimitFromConfig(h.deps.YAMLConfig.Features.AdminDashboard.ViewerLimits)
}

func (h *handler) keyRequestDefaultDailyCostCents() int64 {
	max := h.editorMaxDailyCostCents()
	if max > 0 {
		return max
	}
	return 10000
}

func (h *handler) validateKeyRequestDailyCostLimit(cents int64) error {
	if cents < 0 {
		return fmt.Errorf("daily_cost_limit cannot be negative (got %d); use 0 for unlimited", cents)
	}
	max := h.keyRequestDefaultDailyCostCents()
	if max > 0 && cents > max {
		return fmt.Errorf("daily_cost_limit exceeds maximum of %d cents", max)
	}
	return nil
}

func (h *handler) validateEditorCostLimit(r *http.Request, cents int64) error {
	// A negative limit is invalid input for ALL roles: the enforcement
	// middleware treats <= 0 as "unlimited", so persisting a negative value
	// would silently disable the cap (the opposite of an operator's likely
	// intent). Reject it at the API boundary so it can never be stored.
	if cents < 0 {
		return fmt.Errorf("daily_cost_limit cannot be negative (got %d); use 0 for unlimited", cents)
	}
	role, err := h.auth.userRole(r)
	if err != nil {
		return fmt.Errorf("unable to resolve user role: %w", err)
	}
	if role != adminusers.RoleEditor {
		return nil
	}
	max := h.editorMaxDailyCostCents()
	if max > 0 && cents > max {
		return fmt.Errorf("daily_cost_limit exceeds editor maximum of %d cents", max)
	}
	return nil
}
