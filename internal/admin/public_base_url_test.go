package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
)

func TestPublicBaseURL_DevUsesLocalhost(t *testing.T) {
	t.Setenv("PORT", "9002")
	t.Setenv("ADMIN_PUBLIC_BASE_URL", "")

	h := &handler{deps: &Deps{YAMLConfig: &config.YAMLConfig{
		Features: config.FeaturesConfig{
			AdminDashboard: config.AdminDashboardConfig{
				DevBypassLogin: true,
			},
		},
	}}}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "llm-proxy:9002"

	got := h.publicBaseURL(req)
	if got != "http://localhost:9002" {
		t.Fatalf("expected localhost dev URL, got %q", got)
	}
}

func TestPublicBaseURL_EnvOverridesDevDefault(t *testing.T) {
	t.Setenv("ADMIN_PUBLIC_BASE_URL", "http://localhost:9010")

	h := &handler{deps: &Deps{YAMLConfig: &config.YAMLConfig{
		Features: config.FeaturesConfig{
			AdminDashboard: config.AdminDashboardConfig{
				DevBypassLogin: true,
			},
		},
	}}}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "llm-proxy:9002"

	got := h.publicBaseURL(req)
	if got != "http://localhost:9010" {
		t.Fatalf("expected env override, got %q", got)
	}
}

func TestPublicBaseURL_ProdUsesRequestHost(t *testing.T) {
	t.Setenv("ADMIN_PUBLIC_BASE_URL", "")

	h := &handler{deps: &Deps{YAMLConfig: &config.YAMLConfig{
		Features: config.FeaturesConfig{
			AdminDashboard: config.AdminDashboardConfig{
				Enabled: true,
			},
		},
	}}}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "llm.example.com"

	got := h.publicBaseURL(req)
	if got != "http://llm.example.com" {
		t.Fatalf("expected request host, got %q", got)
	}
}
