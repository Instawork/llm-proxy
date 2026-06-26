package live

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
)

type Runner struct {
	cfg   Config
	admin *AdminClient
	proxy *ProxyClient
}

func NewRunner(cfg Config) (*Runner, error) {
	admin, err := NewAdminClient(cfg.BaseURL, cfg.Timeout)
	if err != nil {
		return nil, err
	}
	proxy, err := NewProxyClient(cfg.BaseURL, cfg.Timeout)
	if err != nil {
		return nil, err
	}
	return &Runner{cfg: cfg, admin: admin, proxy: proxy}, nil
}

func (r *Runner) emitAll(results []Result) []Result {
	if r.cfg.Verbose {
		for _, res := range results {
			PrintResult(res)
		}
	}
	return results
}

func (r *Runner) Run(ctx context.Context) []Result {
	var out []Result
	if r.cfg.Suite("health") {
		r.logf("suite health: GET %s/health", r.cfg.BaseURL)
		out = append(out, r.emitAll(r.runHealth(ctx))...)
	}
	if r.cfg.Suite("admin") {
		r.logf("suite admin: dev login + config + keys CRUD")
		out = append(out, r.emitAll(r.runAdmin(ctx))...)
	}
	if r.cfg.Suite("openai") {
		r.logf("suite openai: passthrough + iw: key via openai-go")
		out = append(out, r.emitAll(r.runOpenAI(ctx))...)
	}
	if r.cfg.Suite("anthropic") {
		r.logf("suite anthropic: passthrough + iw: key via anthropic-sdk-go")
		out = append(out, r.emitAll(r.runAnthropic(ctx))...)
	}
	if r.cfg.Suite("gemini") {
		r.logf("suite gemini: passthrough + iw: key via google.golang.org/genai")
		out = append(out, r.emitAll(r.runGemini(ctx))...)
	}
	if r.cfg.Suite("ratelimit") {
		r.logf("suite ratelimit")
		out = append(out, r.emitAll(r.runRateLimit(ctx))...)
	}
	if r.cfg.Suite("cost") {
		r.logf("suite cost: tail %s", r.cfg.CostFile)
		out = append(out, r.emitAll(r.runCost(ctx))...)
	}
	if r.cfg.Suite("presidio") {
		r.logf("suite presidio: %s", r.cfg.PresidioURL)
		out = append(out, r.emitAll(r.runPresidio(ctx))...)
	}
	if r.cfg.Suite("pii") {
		r.logf("suite pii: redaction through proxy")
		out = append(out, r.emitAll(r.runPII(ctx))...)
	}
	if r.cfg.Suite("redact") {
		r.logf("suite redact: POST /redact")
		out = append(out, r.emitAll(r.runRedactAPI(ctx))...)
	}
	if r.cfg.Suite("snippets") {
		r.logf("suite snippets: share-box tabs under %s", r.cfg.SnippetsDir)
		out = append(out, r.runSnippets(ctx)...)
	}
	return out
}

func (r *Runner) runHealth(ctx context.Context) []Result {
	start := time.Now()
	pr, err := r.proxy.Health(ctx)
	if err != nil {
		return []Result{failResult("health", "check", err.Error())}
	}
	if pr.Status != http.StatusOK {
		return []Result{failResult("health", "check", fmt.Sprintf("status %d", pr.Status))}
	}
	return []Result{passResult("health", "check", "proxy reachable", elapsed(start))}
}

func (r *Runner) runAdmin(ctx context.Context) []Result {
	var out []Result

	start := time.Now()
	me, err := r.admin.Me(ctx)
	if err != nil {
		out = append(out, failResult("admin", "dev-login", err.Error()))
		return out
	}
	email, _ := me["email"].(string)
	out = append(out, passResult("admin", "dev-login", "session as "+email, elapsed(start)))

	start = time.Now()
	cfg, err := r.admin.Config(ctx)
	if err != nil {
		out = append(out, failResult("admin", "config", err.Error()))
	} else {
		features, _ := cfg["features"].(map[string]any)
		out = append(out, passResult("admin", "config", fmt.Sprintf("features=%v", featureKeys(features)), elapsed(start)))
	}

	start = time.Now()
	_, err = r.admin.RateLimits(ctx)
	if err != nil {
		out = append(out, failResult("admin", "rate-limits", err.Error()))
	} else {
		out = append(out, passResult("admin", "rate-limits", "snapshot ok", elapsed(start)))
	}

	start = time.Now()
	costCfg, err := r.admin.CostConfig(ctx)
	if err != nil {
		out = append(out, failResult("admin", "cost-config", err.Error()))
	} else {
		enabled, _ := costCfg["enabled"].(bool)
		stats, _ := costCfg["stats"].(map[string]any)
		if enabled && !costStatsAvailable(stats) {
			out = append(out, failResult("admin", "cost-config", "stats unavailable — restart proxy after enabling cost_tracking"))
		} else {
			msg := fmt.Sprintf("enabled=%v", enabled)
			if enabled {
				msg += " stats=live"
			}
			out = append(out, passResult("admin", "cost-config", msg, elapsed(start)))
		}
	}

	start = time.Now()
	piiResp, err := r.admin.PII(ctx)
	if err != nil {
		out = append(out, failResult("admin", "pii-endpoint", err.Error()))
	} else {
		enabled, _ := piiResp["enabled"].(bool)
		stats, _ := piiResp["stats"].(map[string]any)
		if !enabled {
			out = append(out, skipResult("admin", "pii-endpoint", "pii_redact disabled in config"))
		} else if !piiStatsAvailable(stats) {
			out = append(out, failResult("admin", "pii-endpoint", "stats unavailable — restart proxy after enabling pii_redact"))
		} else {
			failMode, _ := piiResp["fail_mode"].(string)
			out = append(out, passResult("admin", "pii-endpoint", fmt.Sprintf("enabled fail_mode=%s stats=live", failMode), elapsed(start)))
		}
	}

	start = time.Now()
	tag := fmt.Sprintf("live-admin-%d", time.Now().UnixNano())
	if r.cfg.OpenAIKey == "" {
		out = append(out, skipResult("admin", "keys-crud", "OPENAI_API_KEY not set"))
		return out
	}
	key, err := r.admin.CreateKey(ctx, createKeyRequest{
		Provider:    "openai",
		ActualKey:   r.cfg.OpenAIKey,
		Description: tag,
		Tags:        map[string]string{"suite": "live"},
	})
	if err != nil {
		out = append(out, failResult("admin", "keys-crud", "create: "+err.Error()))
		return out
	}
	if !apikeys.HasKeyPrefix(key.Key) {
		out = append(out, failResult("admin", "keys-crud", "expected proxy key prefix, got "+key.Key))
	} else if err := r.admin.DeleteKey(ctx, key.Key); err != nil {
		out = append(out, failResult("admin", "keys-crud", "delete: "+err.Error()))
	} else {
		out = append(out, passResult("admin", "keys-crud", "create+delete "+key.Key, elapsed(start)))
	}
	return out
}

func featureKeys(m map[string]any) []string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func (r *Runner) runOpenAI(ctx context.Context) []Result {
	if r.cfg.OpenAIKey == "" {
		return []Result{skipResult("openai", "all", "OPENAI_API_KEY not set")}
	}
	var out []Result

	start := time.Now()
	pr, err := r.proxy.OpenAIChat(ctx, r.cfg.OpenAIKey, "gpt-4o-mini", 5)
	if err != nil {
		out = append(out, failResult("openai", "passthrough", err.Error()))
	} else if err := proxyOK(pr); err != nil {
		out = append(out, failResult("openai", "passthrough", err.Error()))
	} else {
		detail := "upstream ok"
		if hasTokenHeaders(pr) {
			detail = fmt.Sprintf("tokens in=%s out=%s", pr.InputTok, pr.OutputTok)
		}
		out = append(out, passResult("openai", "passthrough", detail+" via openai-go", elapsed(start)))
	}

	start = time.Now()
	key, err := r.admin.CreateKey(ctx, createKeyRequest{
		Provider:    "openai",
		ActualKey:   r.cfg.OpenAIKey,
		Description: "live-openai-iw",
	})
	if err != nil {
		out = append(out, failResult("openai", "iw-key", "create key: "+err.Error()))
		return out
	}
	defer func() { _ = r.admin.DeleteKey(ctx, key.Key) }()

	pr, err = r.proxy.OpenAIChat(ctx, key.Key, "gpt-4o-mini", 5)
	if err != nil {
		out = append(out, failResult("openai", "iw-key", err.Error()))
	} else if err := proxyOK(pr); err != nil {
		out = append(out, failResult("openai", "iw-key", err.Error()))
	} else {
		out = append(out, passResult("openai", "iw-key", "openai-go via iw: key", elapsed(start)))
	}
	return out
}

func (r *Runner) runAnthropic(ctx context.Context) []Result {
	if r.cfg.AnthropicKey == "" {
		return []Result{skipResult("anthropic", "all", "ANTHROPIC_API_KEY not set")}
	}
	var out []Result

	start := time.Now()
	pr, err := r.proxy.AnthropicMessage(ctx, r.cfg.AnthropicKey, "claude-haiku-4-5", 5)
	if err != nil {
		out = append(out, failResult("anthropic", "passthrough", err.Error()))
	} else if err := proxyOK(pr); err != nil {
		out = append(out, failResult("anthropic", "passthrough", err.Error()))
	} else {
		out = append(out, passResult("anthropic", "passthrough", fmt.Sprintf("anthropic-sdk-go provider=%s model=%s", pr.Provider, pr.Model), elapsed(start)))
	}

	start = time.Now()
	key, err := r.admin.CreateKey(ctx, createKeyRequest{
		Provider:    "anthropic",
		ActualKey:   r.cfg.AnthropicKey,
		Description: "live-anthropic-iw",
	})
	if err != nil {
		out = append(out, failResult("anthropic", "iw-key", "create key: "+err.Error()))
		return out
	}
	defer func() { _ = r.admin.DeleteKey(ctx, key.Key) }()

	pr, err = r.proxy.AnthropicMessage(ctx, key.Key, "claude-haiku-4-5", 5)
	if err != nil {
		out = append(out, failResult("anthropic", "iw-key", err.Error()))
	} else if err := proxyOK(pr); err != nil {
		out = append(out, failResult("anthropic", "iw-key", err.Error()))
	} else {
		out = append(out, passResult("anthropic", "iw-key", "anthropic-sdk-go via iw: key", elapsed(start)))
	}
	return out
}

func (r *Runner) runGemini(ctx context.Context) []Result {
	if r.cfg.GeminiKey == "" {
		return []Result{skipResult("gemini", "all", "GEMINI_API_KEY not set")}
	}
	var out []Result

	start := time.Now()
	pr, err := r.proxy.GeminiGenerate(ctx, r.cfg.GeminiKey, "gemini-2.5-flash")
	if err != nil {
		out = append(out, failResult("gemini", "passthrough", err.Error()))
	} else if err := proxyOK(pr); err != nil {
		out = append(out, failResult("gemini", "passthrough", err.Error()))
	} else {
		out = append(out, passResult("gemini", "passthrough", fmt.Sprintf("google.golang.org/genai provider=%s", pr.Provider), elapsed(start)))
	}

	start = time.Now()
	key, err := r.admin.CreateKey(ctx, createKeyRequest{
		Provider:    "gemini",
		ActualKey:   r.cfg.GeminiKey,
		Description: "live-gemini-iw",
	})
	if err != nil {
		out = append(out, failResult("gemini", "iw-key", "create key: "+err.Error()))
		return out
	}
	defer func() { _ = r.admin.DeleteKey(ctx, key.Key) }()

	pr, err = r.proxy.GeminiGenerate(ctx, key.Key, "gemini-2.5-flash")
	if err != nil {
		out = append(out, failResult("gemini", "iw-key", err.Error()))
	} else if err := proxyOK(pr); err != nil {
		out = append(out, failResult("gemini", "iw-key", err.Error()))
	} else {
		out = append(out, passResult("gemini", "iw-key", "google.golang.org/genai via iw: key", elapsed(start)))
	}
	return out
}

func (r *Runner) runRateLimit(ctx context.Context) []Result {
	if r.cfg.OpenAIKey == "" {
		return []Result{skipResult("ratelimit", "per-key-rpm", "OPENAI_API_KEY not set")}
	}

	start := time.Now()
	key, err := r.admin.CreateKey(ctx, createKeyRequest{
		Provider:     "openai",
		ActualKey:    r.cfg.OpenAIKey,
		Description:  "live-ratelimit",
		RateLimitRPM: 1,
		RateLimitTPM: 50000,
	})
	if err != nil {
		return []Result{failResult("ratelimit", "per-key-rpm", "create key: "+err.Error())}
	}
	defer func() { _ = r.admin.DeleteKey(ctx, key.Key) }()

	first, err := r.proxy.OpenAIChat(ctx, key.Key, "gpt-4o-mini", 1)
	if err != nil && (first == nil || first.Status != http.StatusTooManyRequests) {
		return []Result{failResult("ratelimit", "per-key-rpm", "first request: "+err.Error())}
	}
	if first != nil && first.Status == http.StatusTooManyRequests {
		return []Result{skipResult("ratelimit", "per-key-rpm", "first request already 429 — key may have stale limits; retry in 60s")}
	}
	if err := proxyOK(first); err != nil {
		return []Result{failResult("ratelimit", "per-key-rpm", "first request: "+err.Error())}
	}

	second, err := r.proxy.OpenAIChat(ctx, key.Key, "gpt-4o-mini", 1)
	if second == nil || second.Status != http.StatusTooManyRequests {
		msg := fmt.Sprintf("expected 429, got %d", 0)
		if second != nil {
			msg = fmt.Sprintf("expected 429, got %d", second.Status)
		}
		if err != nil {
			msg += " (" + err.Error() + ")"
		}
		return []Result{failResult("ratelimit", "per-key-rpm", msg)}
	}
	scope := second.Headers.Get("X-RateLimit-Scope")
	reason := second.Headers.Get("X-RateLimit-Reason")
	if !strings.Contains(scope, "key:") {
		return []Result{failResult("ratelimit", "per-key-rpm", "X-RateLimit-Scope missing key scope: "+scope)}
	}
	return []Result{passResult("ratelimit", "per-key-rpm", fmt.Sprintf("429 scope=%s reason=%s", scope, reason), elapsed(start))}
}

type costRecord struct {
	Provider    string  `json:"provider"`
	InputTokens int     `json:"input_tokens"`
	TotalCost   float64 `json:"total_cost"`
}

func (r *Runner) runCost(ctx context.Context) []Result {
	if r.cfg.OpenAIKey == "" {
		return []Result{skipResult("cost", "jsonl-record", "OPENAI_API_KEY not set")}
	}
	before, err := countLines(r.cfg.CostFile)
	if err != nil {
		return []Result{skipResult("cost", "jsonl-record", "cannot read cost file: "+err.Error())}
	}

	start := time.Now()
	pr, err := r.proxy.OpenAIChat(ctx, r.cfg.OpenAIKey, "gpt-4o-mini", 3)
	if err != nil {
		return []Result{failResult("cost", "jsonl-record", err.Error())}
	}
	if err := proxyOK(pr); err != nil {
		return []Result{failResult("cost", "jsonl-record", err.Error())}
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		after, err := countLines(r.cfg.CostFile)
		if err != nil {
			break
		}
		if after > before {
			line, err := readLastLine(r.cfg.CostFile)
			if err != nil {
				return []Result{failResult("cost", "jsonl-record", err.Error())}
			}
			var rec costRecord
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				return []Result{failResult("cost", "jsonl-record", "parse: "+err.Error())}
			}
			if rec.Provider != "openai" || rec.InputTokens <= 0 {
				return []Result{failResult("cost", "jsonl-record", fmt.Sprintf("unexpected record: %+v", rec))}
			}
			results := []Result{
				passResult("cost", "jsonl-record", fmt.Sprintf("openai input_tokens=%d total_cost=%.6f", rec.InputTokens, rec.TotalCost), elapsed(start)),
			}
			costCfg, err := r.admin.CostConfig(ctx)
			if err != nil {
				results = append(results, failResult("cost", "live-stats", err.Error()))
			} else {
				stats, _ := costCfg["stats"].(map[string]any)
				if !costStatsAvailable(stats) {
					results = append(results, failResult("cost", "live-stats", "stats unavailable"))
				} else if costStatsSpendToday(stats) <= 0 {
					results = append(results, failResult("cost", "live-stats", "spend_today_usd still zero after tracked request"))
				} else {
					results = append(results, passResult("cost", "live-stats", fmt.Sprintf("spend_today_usd=%.6f", costStatsSpendToday(stats)), elapsed(start)))
				}
			}
			return results
		}
		time.Sleep(500 * time.Millisecond)
	}
	return []Result{skipResult("cost", "jsonl-record", "no new line in "+r.cfg.CostFile+" within 15s (async flush or file transport disabled?)")}
}

func (r *Runner) runPresidio(ctx context.Context) []Result {
	start := time.Now()
	if !presidioReachable(r.cfg.PresidioURL) {
		return []Result{skipResult("presidio", "health", "sidecar unreachable at "+r.cfg.PresidioURL+" (run: docker compose --profile pii_redact up -d presidio)")}
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.PresidioURL+"/health", nil)
	if err != nil {
		return []Result{failResult("presidio", "health", err.Error())}
	}
	resp, err := client.Do(req)
	if err != nil {
		return []Result{failResult("presidio", "health", err.Error())}
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return []Result{failResult("presidio", "health", fmt.Sprintf("status %d", resp.StatusCode))}
	}
	return []Result{passResult("presidio", "health", r.cfg.PresidioURL, elapsed(start))}
}

func (r *Runner) runPII(ctx context.Context) []Result {
	piiCfg, err := r.admin.PII(ctx)
	if err != nil {
		return []Result{failResult("pii", "admin-api", err.Error())}
	}
	enabled, _ := piiCfg["enabled"].(bool)
	allowPerKey, _ := piiCfg["allow_per_key_override"].(bool)
	if !enabled && !allowPerKey {
		return []Result{skipResult("pii", "all", "pii_redact disabled (set features.pii_redact.enabled in dev.yml and restart proxy)")}
	}

	var out []Result
	out = append(out, r.runPIIAdminStats(ctx, piiCfg)...)

	if !presidioReachable(r.cfg.PresidioURL) {
		out = append(out, skipResult("pii", "redaction", "Presidio sidecar not reachable (docker compose --profile pii_redact up -d presidio)"))
		out = append(out, skipResult("pii", "entities", "Presidio sidecar not reachable"))
		out = append(out, skipResult("pii", "recent-events", "Presidio sidecar not reachable"))
		out = append(out, skipResult("pii", "wire-restore", "Presidio sidecar not reachable"))
		out = append(out, skipResult("pii", "wire-seal", "Presidio sidecar not reachable"))
		return out
	}
	if r.cfg.OpenAIKey == "" {
		out = append(out, skipResult("pii", "redaction", "OPENAI_API_KEY not set"))
		out = append(out, skipResult("pii", "entities", "OPENAI_API_KEY not set"))
		out = append(out, skipResult("pii", "recent-events", "OPENAI_API_KEY not set"))
		out = append(out, skipResult("pii", "wire-restore", "OPENAI_API_KEY not set"))
		out = append(out, skipResult("pii", "wire-seal", "OPENAI_API_KEY not set"))
		return out
	}

	out = append(out, r.runPIIRedaction(ctx, piiCfg)...)
	out = append(out, r.runPIIWireRestore(ctx)...)
	if r.cfg.AnthropicKey != "" {
		out = append(out, r.runPIIWireRestoreAnthropic(ctx)...)
	} else {
		out = append(out, skipResult("pii", "wire-restore-anthropic", "ANTHROPIC_API_KEY not set"))
		out = append(out, skipResult("pii", "wire-restore-anthropic-stream", "ANTHROPIC_API_KEY not set"))
	}
	if r.cfg.GeminiKey != "" {
		out = append(out, r.runPIIWireRestoreGemini(ctx)...)
	} else {
		out = append(out, skipResult("pii", "wire-restore-gemini", "GEMINI_API_KEY not set"))
		out = append(out, skipResult("pii", "wire-restore-gemini-stream", "GEMINI_API_KEY not set"))
	}
	return out
}

func (r *Runner) runPIIAdminStats(_ context.Context, piiCfg map[string]any) []Result {
	start := time.Now()
	stats, _ := piiCfg["stats"].(map[string]any)
	if !piiStatsAvailable(stats) {
		return []Result{failResult("pii", "admin-stats", "recorder inactive — restart proxy after enabling pii_redact in dev.yml")}
	}
	failMode, _ := piiCfg["fail_mode"].(string)
	return []Result{passResult("pii", "admin-stats", fmt.Sprintf("live recorder fail_mode=%s scanned=%.0f", failMode, piiStatsScanned(stats)), elapsed(start))}
}

func (r *Runner) runPIIRedaction(ctx context.Context, piiCfg map[string]any) []Result {
	statsBefore, _ := piiCfg["stats"].(map[string]any)
	scannedBefore := piiStatsScanned(statsBefore)
	withPIIBefore := piiStatsWithPII(statsBefore)
	entitiesBefore := piiStatsEntitiesTotal(statsBefore)
	recentBefore := piiStatsRecentCount(statsBefore)

	start := time.Now()
	redact := true
	key, err := r.admin.CreateKey(ctx, createKeyRequest{
		Provider:    "openai",
		ActualKey:   r.cfg.OpenAIKey,
		Description: "live-pii",
		RedactPII:   &redact,
	})
	if err != nil {
		return []Result{failResult("pii", "redaction", "create key: "+err.Error())}
	}
	defer func() { _ = r.admin.DeleteKey(ctx, key.Key) }()

	pr, err := r.proxy.OpenAIChatWithPII(ctx, key.Key)
	if err != nil {
		return []Result{failResult("pii", "redaction", err.Error())}
	}
	if pr.Status == http.StatusServiceUnavailable {
		return []Result{failResult("pii", "redaction", "503 — Presidio fail-closed or sidecar error")}
	}
	if err := proxyOK(pr); err != nil {
		return []Result{failResult("pii", "redaction", err.Error())}
	}

	piiAfter, err := r.admin.PII(ctx)
	if err != nil {
		return []Result{
			failResult("pii", "redaction", "admin pii: "+err.Error()),
			failResult("pii", "entities", "admin pii: "+err.Error()),
			failResult("pii", "recent-events", "admin pii: "+err.Error()),
		}
	}
	statsAfter, _ := piiAfter["stats"].(map[string]any)
	scannedAfter := piiStatsScanned(statsAfter)
	withPIIAfter := piiStatsWithPII(statsAfter)
	entitiesAfter := piiStatsEntitiesTotal(statsAfter)
	recentAfter := piiStatsRecentCount(statsAfter)
	latestOutcome := piiLatestRecentOutcome(statsAfter)
	latestEntityTotal := piiLatestRecentEntityTotal(statsAfter)

	var out []Result
	if scannedAfter <= scannedBefore {
		out = append(out, failResult("pii", "redaction", fmt.Sprintf("requests_scanned did not increase (before=%.0f after=%.0f)", scannedBefore, scannedAfter)))
	} else if latestOutcome == "fail_open" {
		out = append(out, failResult("pii", "redaction", "Presidio analyze failed (fail_open) — run `make test-pii-up` (network + warm-up) and ensure dev.yml timeout_ms is high enough; restart proxy after YAML changes"))
	} else if withPIIAfter <= withPIIBefore {
		out = append(out, failResult("pii", "redaction", fmt.Sprintf("requests_with_pii did not increase (before=%.0f after=%.0f outcome=%s entity_total=%.0f)", withPIIBefore, withPIIAfter, latestOutcome, latestEntityTotal)))
	} else {
		out = append(out, passResult("pii", "redaction", fmt.Sprintf("scanned %.0f→%.0f with_pii %.0f→%.0f", scannedBefore, scannedAfter, withPIIBefore, withPIIAfter), elapsed(start)))
	}

	if latestOutcome == "fail_open" {
		out = append(out, skipResult("pii", "entities", "skipped — Presidio fail_open"))
	} else if entitiesAfter <= entitiesBefore {
		out = append(out, failResult("pii", "entities", fmt.Sprintf("entities_total did not increase (before=%.0f after=%.0f outcome=%s)", entitiesBefore, entitiesAfter, latestOutcome)))
	} else {
		out = append(out, passResult("pii", "entities", fmt.Sprintf("entities_total %.0f→%.0f", entitiesBefore, entitiesAfter), elapsed(start)))
	}

	if recentAfter <= recentBefore {
		out = append(out, failResult("pii", "recent-events", fmt.Sprintf("recent events did not grow (before=%d after=%d)", recentBefore, recentAfter)))
	} else {
		out = append(out, passResult("pii", "recent-events", fmt.Sprintf("recent %d→%d events", recentBefore, recentAfter), elapsed(start)))
	}
	return out
}

func (r *Runner) runPIIWireRestore(ctx context.Context) []Result {
	start := time.Now()
	redactPII := true
	key, err := r.admin.CreateKey(ctx, createKeyRequest{
		Provider:    "openai",
		ActualKey:   r.cfg.OpenAIKey,
		Description: "live-pii-wire",
		RedactPII:   &redactPII,
	})
	if err != nil {
		return []Result{
			failResult("pii", "wire-restore", "create key: "+err.Error()),
			failResult("pii", "wire-seal", "create key: "+err.Error()),
		}
	}
	defer func() { _ = r.admin.DeleteKey(ctx, key.Key) }()

	email := fmt.Sprintf("pii-restore-%d@example.com", time.Now().UnixNano())
	pr, content, err := r.proxy.OpenAIChatRepeatEmail(ctx, key.Key, email)
	if err != nil {
		return []Result{failResult("pii", "wire-restore", err.Error())}
	}
	if err := proxyOK(pr); err != nil {
		return []Result{failResult("pii", "wire-restore", err.Error())}
	}
	content = normalizeEcho(content)

	var out []Result
	if !strings.Contains(content, email) {
		out = append(out, failResult("pii", "wire-restore",
			fmt.Sprintf("expected restored email %q in assistant reply %q", email, content)))
	} else if leaked, ok := PIIMaskLeaked(pr.Headers, pr.Trailer); ok && leaked > 0 {
		out = append(out, failResult("pii", "wire-restore",
			fmt.Sprintf("MASK placeholder leaked to client (X-LLM-PII-Leaked=%d)", leaked)))
	} else {
		out = append(out, passResult("pii", "wire-restore",
			fmt.Sprintf("MASK email restored in client reply (%s)", truncate(content, 80)), elapsed(start)))
	}

	start = time.Now()
	const ssn = "222-33-4444"
	pr, ssnReply, err := r.proxy.OpenAIChatRepeatSSN(ctx, key.Key, ssn)
	if err != nil {
		out = append(out, failResult("pii", "wire-seal", err.Error()))
		return out
	}
	if err := proxyOK(pr); err != nil {
		out = append(out, failResult("pii", "wire-seal", err.Error()))
		return out
	}
	ssnReply = normalizeEcho(ssnReply)
	if strings.Contains(ssnReply, ssn) {
		out = append(out, failResult("pii", "wire-seal",
			fmt.Sprintf("raw SSN leaked to client: %q", ssnReply)))
	} else if !strings.Contains(ssnReply, "<PII_US_SSN") && !strings.Contains(ssnReply, `[REDACTED:US_SSN]`) {
		out = append(out, failResult("pii", "wire-seal",
			fmt.Sprintf("expected SEAL placeholder or redact marker in reply, got %q", truncate(ssnReply, 80))))
	} else {
		out = append(out, passResult("pii", "wire-seal",
			fmt.Sprintf("SEAL SSN stayed opaque (%s)", truncate(ssnReply, 80)), elapsed(start)))
	}
	return out
}

func (r *Runner) runPIIWireRestoreAnthropic(ctx context.Context) []Result {
	return r.runPIIWireRestoreProvider(ctx, piiWireRestoreProviderSpec{
		provider:    "anthropic",
		actualKey:   r.cfg.AnthropicKey,
		keyDesc:     "live-pii-wire-anthropic",
		suitePrefix: "wire-restore-anthropic",
		repeatEmail: r.proxy.AnthropicChatRepeatEmail,
	})
}

func (r *Runner) runPIIWireRestoreGemini(ctx context.Context) []Result {
	return r.runPIIWireRestoreProvider(ctx, piiWireRestoreProviderSpec{
		provider:    "gemini",
		actualKey:   r.cfg.GeminiKey,
		keyDesc:     "live-pii-wire-gemini",
		suitePrefix: "wire-restore-gemini",
		repeatEmail: r.proxy.GeminiChatRepeatEmail,
	})
}

type piiWireRestoreProviderSpec struct {
	provider    string
	actualKey   string
	keyDesc     string
	suitePrefix string
	repeatEmail func(context.Context, string, string, bool) (*ProxyResponse, string, error)
}

func (r *Runner) runPIIWireRestoreProvider(ctx context.Context, spec piiWireRestoreProviderSpec) []Result {
	start := time.Now()
	redactPII := true
	key, err := r.admin.CreateKey(ctx, createKeyRequest{
		Provider:    spec.provider,
		ActualKey:   spec.actualKey,
		Description: spec.keyDesc,
		RedactPII:   &redactPII,
	})
	if err != nil {
		return []Result{
			failResult("pii", spec.suitePrefix, "create key: "+err.Error()),
			failResult("pii", spec.suitePrefix+"-stream", "create key: "+err.Error()),
		}
	}
	defer func() { _ = r.admin.DeleteKey(ctx, key.Key) }()

	email := fmt.Sprintf("%s-%d@example.com", spec.suitePrefix, time.Now().UnixNano())
	pr, content, err := spec.repeatEmail(ctx, key.Key, email, false)
	if err != nil {
		return []Result{failResult("pii", spec.suitePrefix, err.Error())}
	}
	if err := proxyOK(pr); err != nil {
		return []Result{failResult("pii", spec.suitePrefix, err.Error())}
	}
	content = normalizeEcho(content)

	var out []Result
	if !strings.Contains(content, email) {
		out = append(out, failResult("pii", spec.suitePrefix,
			fmt.Sprintf("expected restored email %q in assistant reply %q", email, content)))
	} else if leaked, ok := PIIMaskLeaked(pr.Headers, pr.Trailer); ok && leaked > 0 {
		out = append(out, failResult("pii", spec.suitePrefix,
			fmt.Sprintf("MASK placeholder leaked to client on %s non-streaming path (X-LLM-PII-Leaked=%d)", spec.provider, leaked)))
	} else {
		out = append(out, passResult("pii", spec.suitePrefix,
			fmt.Sprintf("MASK email restored (%s)", truncate(content, 80)), elapsed(start)))
	}

	start = time.Now()
	streamEmail := fmt.Sprintf("%s-stream-%d@example.com", spec.suitePrefix, time.Now().UnixNano())
	pr, streamBody, err := spec.repeatEmail(ctx, key.Key, streamEmail, true)
	if err != nil {
		out = append(out, failResult("pii", spec.suitePrefix+"-stream", err.Error()))
		return out
	}
	if err := proxyOK(pr); err != nil {
		out = append(out, failResult("pii", spec.suitePrefix+"-stream", err.Error()))
		return out
	}
	streamBody = normalizeEcho(streamBody)
	if !strings.Contains(streamBody, streamEmail) {
		out = append(out, failResult("pii", spec.suitePrefix+"-stream",
			fmt.Sprintf("expected restored email %q in stream body %q", streamEmail, truncate(streamBody, 120))))
	} else if leaked, ok := PIIMaskLeaked(pr.Headers, pr.Trailer); ok && leaked > 0 {
		out = append(out, failResult("pii", spec.suitePrefix+"-stream",
			fmt.Sprintf("MASK placeholder leaked to client on %s streaming path (X-LLM-PII-Leaked=%d)", spec.provider, leaked)))
	} else {
		out = append(out, passResult("pii", spec.suitePrefix+"-stream",
			fmt.Sprintf("MASK email restored in stream (%s)", truncate(streamBody, 80)), elapsed(start)))
	}
	return out
}

func presidioReachable(base string) bool {
	u := strings.TrimRight(base, "/")
	host := strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		n++
	}
	return n, sc.Err()
}

func readLastLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var last string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			last = line
		}
	}
	return last, sc.Err()
}
