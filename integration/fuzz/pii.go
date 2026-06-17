package fuzz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/integration/live"
)

// Fuzz exercises the real llm-proxy HTTP API with fake_upstream enabled:
// Presidio scrub + wire restore run in-process; only the provider hop is
// synthetic. Run `make test-pii-up` and restart the fuzz proxy after
// enabling pii_redact in configs/fuzz.yml.

func (r *Runner) requirePIIReady(ctx context.Context) (map[string]any, bool, string) {
	cfg, err := r.admin.PII(ctx)
	if err != nil {
		return nil, false, "admin /pii: " + err.Error()
	}
	enabled, _ := cfg["enabled"].(bool)
	if !enabled {
		return nil, false, "pii_redact disabled — set features.pii_redact.enabled in configs/fuzz.yml and restart proxy"
	}
	stats, _ := cfg["stats"].(map[string]any)
	if !live.PIIStatsAvailable(stats) {
		return nil, false, "pii stats unavailable — restart proxy after enabling pii_redact"
	}
	return cfg, true, ""
}

func openAIAssistantContent(body string) (string, error) {
	var root struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return "", err
	}
	if len(root.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return root.Choices[0].Message.Content, nil
}

func normalizeEcho(s string) string {
	return strings.TrimSpace(strings.Trim(s, `"`))
}

// piiPresidioRedaction sends known PII through the real proxy and asserts
// Presidio scrubbed it (admin requests_scanned / requests_with_pii grow).
// Common failure: Presidio sidecar down → fail_open passthrough.
func (r *Runner) piiPresidioRedaction(ctx context.Context) (bool, string) {
	piiCfg, ok, msg := r.requirePIIReady(ctx)
	if !ok {
		return false, msg
	}
	statsBefore, _ := piiCfg["stats"].(map[string]any)
	// Use the monotonic, uncapped counters as the growth signal. The `recent`
	// list is a bounded ring buffer (≤50), so it saturates after enough PII
	// traffic and can no longer "grow" — requests_scanned/with_pii do not.
	scannedBefore := live.PIIStatsScanned(statsBefore)
	withPIIBefore := live.PIIStatsWithPII(statsBefore)

	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.createWithPII(ctx, "fuzz-pii-redact", 1000, 5_000_000, true)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	res := r.proxy.OpenAIChat(ctx, ChatOpts{
		APIKey:    key,
		ChaosRate: &zero,
		Content:   "my ssn is 222-33-4444 and email alice@example.com — say ok",
		OutputTok: 5,
	})
	if res.Status == http.StatusServiceUnavailable {
		return false, "503 — Presidio fail-closed or sidecar unreachable (run: make test-pii-up)"
	}
	if res.Status != http.StatusOK {
		return false, fmt.Sprintf("want 200 got %d body=%s", res.Status, truncate(res.Body, 120))
	}

	deadline := time.Now().Add(5 * time.Second)
	var scannedAfter, withPIIAfter float64
	var outcome string
	var entityTotal float64
	for time.Now().Before(deadline) {
		piiAfter, err := r.admin.PII(ctx)
		if err != nil {
			return false, err.Error()
		}
		statsAfter, _ := piiAfter["stats"].(map[string]any)
		scannedAfter = live.PIIStatsScanned(statsAfter)
		withPIIAfter = live.PIIStatsWithPII(statsAfter)
		outcome = live.PIILatestRecentOutcome(statsAfter)
		entityTotal = live.PIILatestRecentEntityTotal(statsAfter)
		if scannedAfter > scannedBefore && withPIIAfter > withPIIBefore && outcome == "ok" && entityTotal > 0 {
			return true, fmt.Sprintf("scanned %.0f→%.0f with_pii %.0f→%.0f outcome=%s entities=%.0f", scannedBefore, scannedAfter, withPIIBefore, withPIIAfter, outcome, entityTotal)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if outcome == "fail_open" {
		return false, "Presidio analyze failed (fail_open) — run `make test-pii-up` and restart fuzz proxy"
	}
	return false, fmt.Sprintf("PII counters did not grow with detection (scanned %.0f→%.0f with_pii %.0f→%.0f outcome=%s entities=%.0f)", scannedBefore, scannedAfter, withPIIBefore, withPIIAfter, outcome, entityTotal)
}

// piiWireRestoreEmail exercises MASK-tier wire restore: Presidio scrubs the
// email to <EMAIL_ADDRESS_N>, fake upstream echoes the placeholder, and the
// client must see the original address restored — not the placeholder token.
func (r *Runner) piiWireRestoreEmail(ctx context.Context) (bool, string) {
	if _, ok, msg := r.requirePIIReady(ctx); !ok {
		return false, msg
	}
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.createWithPII(ctx, "fuzz-pii-restore", 1000, 5_000_000, true)
	if err != nil {
		return false, err.Error()
	}
	email := fmt.Sprintf("fuzz-restore-%d@example.com", time.Now().UnixNano())
	prompt := fmt.Sprintf("My email is %s. Reply with ONLY that email address and nothing else.", email)
	zero := 0.0
	res := r.proxy.OpenAIChat(ctx, ChatOpts{
		APIKey:               key,
		ChaosRate:            &zero,
		Content:              prompt,
		OutputTok:            32,
		FakeEchoPlaceholders: true,
	})
	if res.Status == http.StatusServiceUnavailable {
		return false, "503 — Presidio unavailable"
	}
	if res.Status != http.StatusOK {
		return false, fmt.Sprintf("want 200 got %d body=%s", res.Status, truncate(res.Body, 120))
	}
	content, err := openAIAssistantContent(res.Body)
	if err != nil {
		return false, err.Error()
	}
	content = normalizeEcho(content)
	if !strings.Contains(content, email) {
		return false, fmt.Sprintf("MASK email not restored: want %q in %q", email, truncate(content, 120))
	}
	if strings.Contains(content, "<EMAIL_ADDRESS") {
		return false, fmt.Sprintf("MASK placeholder leaked to client: %q", truncate(content, 120))
	}
	return true, fmt.Sprintf("MASK email restored (%s)", truncate(content, 60))
}

// piiConcurrentNoBleed fires many concurrent requests, each carrying a UNIQUE
// email, through the scrub→echo→restore pipeline and asserts every response
// restores exactly ITS OWN email — never another concurrent request's. This
// catches cross-request state bleed in the placeholder restore map (e.g. a
// shared/global mapping) that a sequential test cannot surface. Because each
// request must see its own value, an A↔B swap is detected (set-equality would
// not catch it), so we pair sent↔restored per request rather than using Burst.
func (r *Runner) piiConcurrentNoBleed(ctx context.Context) (bool, string) {
	if _, ok, msg := r.requirePIIReady(ctx); !ok {
		return false, msg
	}
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.createWithPII(ctx, "fuzz-pii-nobleed", 100_000, 500_000_000, true)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	const fired = 60
	const workers = 32
	runID := time.Now().UnixNano()

	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	var mu sync.Mutex
	var mismatches []string

	for i := 0; i < fired; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			email := fmt.Sprintf("fuzz-bleed-%d-%d@example.com", runID, i)
			prompt := fmt.Sprintf("My email is %s. Reply with ONLY that email address and nothing else.", email)
			res := r.proxy.OpenAIChat(ctx, ChatOpts{
				APIKey:               key,
				ChaosRate:            &zero,
				Content:              prompt,
				OutputTok:            32,
				FakeEchoPlaceholders: true,
			})
			content, _ := openAIAssistantContent(res.Body)
			content = normalizeEcho(content)
			fail := ""
			switch {
			case res.Status != http.StatusOK:
				fail = fmt.Sprintf("status=%d", res.Status)
			case strings.Contains(content, "<EMAIL_ADDRESS"):
				fail = fmt.Sprintf("placeholder leaked: %q", truncate(content, 60))
			case !strings.Contains(content, email):
				fail = fmt.Sprintf("restored wrong/missing email: sent=%s got=%q", email, truncate(content, 60))
			}
			if fail != "" {
				mu.Lock()
				mismatches = append(mismatches, fmt.Sprintf("req#%d %s", i, fail))
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if len(mismatches) > 0 {
		return false, fmt.Sprintf("PII CROSS-REQUEST BLEED (%d/%d failed): %s", len(mismatches), fired, mismatches[0])
	}
	return true, fmt.Sprintf("no bleed: %d concurrent unique-email requests each restored exactly its own (workers=%d)", fired, workers)
}

// piiWireSealSSN exercises SEAL-tier opacity: even when the fake upstream
// echoes the scrubbed placeholder back, the client must never see the raw SSN.
func (r *Runner) piiWireSealSSN(ctx context.Context) (bool, string) {
	if _, ok, msg := r.requirePIIReady(ctx); !ok {
		return false, msg
	}
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.createWithPII(ctx, "fuzz-pii-seal", 1000, 5_000_000, true)
	if err != nil {
		return false, err.Error()
	}
	const ssn = "222-33-4444"
	prompt := fmt.Sprintf("My SSN is %s. Reply with ONLY that SSN including dashes and nothing else.", ssn)
	zero := 0.0
	res := r.proxy.OpenAIChat(ctx, ChatOpts{
		APIKey:               key,
		ChaosRate:            &zero,
		Content:              prompt,
		OutputTok:            32,
		FakeEchoPlaceholders: true,
	})
	if res.Status == http.StatusServiceUnavailable {
		return false, "503 — Presidio unavailable"
	}
	if res.Status != http.StatusOK {
		return false, fmt.Sprintf("want 200 got %d body=%s", res.Status, truncate(res.Body, 120))
	}
	if strings.Contains(res.Body, ssn) {
		return false, "raw SSN leaked in response body"
	}
	content, err := openAIAssistantContent(res.Body)
	if err != nil {
		return false, err.Error()
	}
	content = normalizeEcho(content)
	if strings.Contains(content, ssn) {
		return false, fmt.Sprintf("raw SSN leaked in assistant content: %q", truncate(content, 80))
	}
	if !strings.Contains(content, "<US_SSN") {
		return false, fmt.Sprintf("expected SEAL placeholder in reply, got %q", truncate(content, 80))
	}
	return true, fmt.Sprintf("SEAL SSN stayed opaque (%s)", truncate(content, 60))
}
