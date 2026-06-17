package live

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func (r *Runner) runRedactAPI(ctx context.Context) []Result {
	start := time.Now()

	if !presidioReachable(r.cfg.PresidioURL) {
		return []Result{skipResult("redact", "ssn", "Presidio sidecar not reachable — run `make test-pii-up`")}
	}

	apiKey := strings.TrimSpace(os.Getenv("LLM_PROXY_REDACT_KEY"))

	const raw = "patient SSN is 222-33-4444"
	status, body, err := r.proxy.Redact(ctx, apiKey, raw)
	if err != nil {
		return []Result{failResult("redact", "ssn", err.Error())}
	}
	if status == http.StatusUnauthorized {
		return []Result{skipResult("redact", "ssn", "401 — set LLM_PROXY_REDACT_KEY to an iw-* key or enable redact_api.dev_allow_unauthenticated in dev.yml")}
	}
	if status == http.StatusServiceUnavailable {
		return []Result{failResult("redact", "ssn", "503 — Presidio fail-closed or sidecar error")}
	}
	if status != http.StatusOK {
		return []Result{failResult("redact", "ssn", fmt.Sprintf("status %d: %s", status, truncate(body, 200)))}
	}
	if strings.Contains(body, "222-33-4444") {
		return []Result{failResult("redact", "ssn", "raw SSN still present in response")}
	}
	if !strings.Contains(body, "[REDACTED:US_SSN]") {
		return []Result{failResult("redact", "ssn", fmt.Sprintf("expected [REDACTED:US_SSN] marker, got %q", truncate(body, 120)))}
	}
	return []Result{passResult("redact", "ssn", truncate(body, 80), elapsed(start))}
}
