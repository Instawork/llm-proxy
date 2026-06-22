// Package redact provides PII redaction for the llm-proxy by calling a
// Presidio analyzer sidecar over HTTP and replacing detected spans with
// policy-aware tokens:
//
//   - MASK  → numbered placeholders (<PERSON_1>) restored to the client
//   - SEAL  → numbered placeholders that stay opaque to the client
//   - REDACT → fixed [REDACTED:TYPE] markers (no restore)
//
// When wire_placeholders is enabled, scrubbed text (with placeholders)
// is sent to the upstream LLM and MASK-tier values are restored in
// responses. Redact() remains available for one-way observability paths
// (debug log previews) that always emit [REDACTED:TYPE] markers.
//
// Why analyzer-only (no anonymizer service)
// -----------------------------------------
// One container, /analyze HTTP only. The placeholder/replacement step
// is twelve lines of Go (see spliceMarkers) and does not need a second
// container. Skipping the anonymizer service halves the ECS sidecar
// surface area and keeps the fail-mode guard rails localised here.
//
// All API surface is best-effort: every method returns an error or falls
// back to the original input. A flaky sidecar must never break a proxy
// request — that decision is made at the middleware layer via
// “Config.FailMode“.
package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"
)

// defaultAnalyzeTimeout is the Presidio /analyze deadline when YAML leaves
// timeout_ms at zero. Chosen to tolerate cold starts; tune down only after
// baselining a warm sidecar p99.
const defaultAnalyzeTimeout = 3 * time.Second

// DefaultEntityTypes is the audited recognizer scope this package
// passes to Presidio's /analyze endpoint. Without an explicit scope,
// the prebuilt analyzer image runs every default recognizer it ships
// with — UK_NHS, DATE_TIME, MAC_ADDRESS, CRYPTO, MEDICAL_LICENSE, URL,
// NRP, and so on — most of which produce false positives on routine
// application data (10-digit numeric IDs flag as UK_NHS at score 1.0,
// ISO timestamps flag as DATE_TIME at 0.85, etc.).
//
// The list is the union of:
//   - Microsoft built-in recognizers we want enabled (US_SSN, PERSON, ...)
//   - Custom recognizers loaded from recognizers.yaml
//     (DATE_OF_BIRTH, US_STREET_ADDRESS).
//
// It intentionally lives in code, not YAML, so a config override can
// only narrow it; widening requires a code review (see filterToAllowlist).
var DefaultEntityTypes = []string{
	"US_SSN",
	"US_ITIN",
	"US_DRIVER_LICENSE",
	"US_PASSPORT",
	"CREDIT_CARD",
	"US_BANK_NUMBER",
	"IBAN_CODE",
	"PERSON",
	"EMAIL_ADDRESS",
	"PHONE_NUMBER",
	"LOCATION",
	"IP_ADDRESS",
	"DATE_OF_BIRTH",
	"US_STREET_ADDRESS",
}

// DefaultGovIDEntityTypes are the Presidio entity types the ID gate scans
// for in OCR'd image text. Both are on the DefaultEntityTypes allowlist.
var DefaultGovIDEntityTypes = []string{
	"US_PASSPORT",
	"US_DRIVER_LICENSE",
}

// defaultEntityTypesSet is the allowlist as a map for O(1) lookup.
// Built once from DefaultEntityTypes so the two stay in sync.
var defaultEntityTypesSet = func() map[string]struct{} {
	s := make(map[string]struct{}, len(DefaultEntityTypes))
	for _, e := range DefaultEntityTypes {
		s[e] = struct{}{}
	}
	return s
}()

// filterToAllowlist drops any entity type not in DefaultEntityTypes.
// Returns (kept, dropped). Used by New() to make the YAML's
// entity_types field narrowing-only — a config edit that adds UK_NHS
// gets silently filtered out at construction (with a slog warning) so
// the wire payload to Presidio cannot end up wider than the audited
// scope. The kept list preserves the caller's order so any
// already-narrowed config (e.g. “["US_SSN"]“) is unchanged.
func filterToAllowlist(input []string) (kept, dropped []string) {
	for _, e := range input {
		if _, ok := defaultEntityTypesSet[e]; ok {
			kept = append(kept, e)
		} else {
			dropped = append(dropped, e)
		}
	}
	return kept, dropped
}

// Span is one PII hit returned by /analyze.
type Span struct {
	Start      int     `json:"start"`
	End        int     `json:"end"`
	EntityType string  `json:"entity_type"`
	Score      float64 `json:"score"`
}

// Result is the return value of Redact: the redacted text plus the
// distinct entity types observed (useful for log/metric tags without
// leaking the raw values).
type Result struct {
	Text         string
	EntityCounts map[string]int
}

// Config controls the redactor wire behaviour.
//
// Most callers should set AnalyzerURL + Timeout and accept defaults for
// everything else. The middleware layer is responsible for translating
// FailMode into "log and pass through" vs. "abort the request" — the
// redactor itself never makes that decision.
type Config struct {
	// AnalyzerURL is the base URL of the Presidio analyzer sidecar.
	// Required when redaction is enabled. Production: localhost in the
	// same ECS task. Local dev: http://presidio:3000 over the
	// docker-compose network.
	AnalyzerURL string

	// Timeout caps each /analyze round trip. Keep this tight (50–250 ms)
	// — the proxy serves user traffic and a slow sidecar must not stall
	// requests. Default: 200 ms.
	Timeout time.Duration

	// EntityTypes scopes which recognizers run. Empty means "use
	// DefaultEntityTypes" (recommended). Pass a subset to shave latency
	// for known-safe payload shapes.
	EntityTypes []string

	// ScoreThreshold is the minimum confidence Presidio must report
	// before we redact a span. Default: 0.5. Lower values catch more PII
	// at the cost of false positives.
	ScoreThreshold float64

	// Language is forwarded as the /analyze ``language`` parameter.
	// Default: "en".
	Language string

	// HTTPClient overrides the default http.Client. Tests inject an
	// httptest.Server-backed client here.
	HTTPClient *http.Client
}

// Defaults returns a Config populated with the documented defaults.
// Callers must still supply AnalyzerURL.
func Defaults() Config {
	return Config{
		Timeout:        defaultAnalyzeTimeout,
		EntityTypes:    DefaultEntityTypes,
		ScoreThreshold: 0.5,
		Language:       "en",
	}
}

// Redactor wraps a Config + http.Client. Cheap to construct and safe to
// share — the underlying http.Client is concurrent-safe.
type Redactor struct {
	cfg    Config
	client *http.Client
}

// New constructs a Redactor. AnalyzerURL is required.
//
// EntityTypes are filtered to DefaultEntityTypes at construction. This
// is the wire-side guarantee that Presidio never runs a recognizer the
// project hasn't audited: even a YAML config that tries to widen the
// scope with “entity_types: [UK_NHS]“ cannot get UK_NHS detection,
// because UK_NHS isn't on the in-code allowlist. A slog warning fires
// when filtering drops anything so operators see what was rejected.
//
// If filtering produces an empty list (e.g. someone wrote
// “entity_types: [UK_NHS, MEDICAL_LICENSE]“), we fall back to the
// full DefaultEntityTypes set rather than calling /analyze with empty
// “entities“ (which Presidio interprets as "run ALL default
// recognizers" — a strictly worse outcome than the documented default).
func New(cfg Config) (*Redactor, error) {
	if cfg.AnalyzerURL == "" {
		return nil, errors.New("redact: AnalyzerURL is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultAnalyzeTimeout
	}
	if cfg.ScoreThreshold <= 0 {
		cfg.ScoreThreshold = 0.5
	}
	if cfg.Language == "" {
		cfg.Language = "en"
	}
	if len(cfg.EntityTypes) == 0 {
		cfg.EntityTypes = DefaultEntityTypes
	} else {
		kept, dropped := filterToAllowlist(cfg.EntityTypes)
		if len(dropped) > 0 {
			slog.Warn(
				"redact: dropped entity types not in DefaultEntityTypes allowlist; "+
					"widen the in-code allowlist to enable them",
				slog.Any("dropped", dropped),
				slog.Any("kept", kept),
			)
		}
		if len(kept) == 0 {
			// Filtering removed everything — fall back to the audited
			// default rather than letting Presidio run all default
			// recognizers (which is what an empty ``entities`` field
			// means at the wire).
			slog.Warn("redact: every requested entity type was filtered out; " +
				"falling back to DefaultEntityTypes default scope")
			kept = DefaultEntityTypes
		}
		cfg.EntityTypes = kept
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &Redactor{cfg: cfg, client: client}, nil
}

// Redact returns “text“ with every detected span replaced by
// “[REDACTED:ENTITY_TYPE]“ regardless of policy tier. Use this for
// one-way observability (log previews) where placeholders must not leak.
func (r *Redactor) Redact(ctx context.Context, text string) (Result, error) {
	return r.scrub(ctx, text, nil, true)
}

// Scrub analyzes text and replaces spans using policy-aware placeholders.
// When reg is non-nil, MASK/SEAL entries are recorded for response restore.
func (r *Redactor) Scrub(ctx context.Context, text string, reg *Registry) (Result, error) {
	return r.scrub(ctx, text, reg, false)
}

func (r *Redactor) scrub(ctx context.Context, text string, reg *Registry, forceRedactMarkers bool) (Result, error) {
	if text == "" {
		return Result{Text: text, EntityCounts: map[string]int{}}, nil
	}
	spans, err := r.analyze(ctx, text, nil)
	if err != nil {
		return Result{}, err
	}
	return spliceSpans(text, spans, r.cfg.ScoreThreshold, reg, forceRedactMarkers), nil
}

// Analyze posts text to Presidio /analyze using the redactor's configured
// entity scope and returns raw spans without redacting the input.
func (r *Redactor) Analyze(ctx context.Context, text string) ([]Span, error) {
	return r.analyze(ctx, text, nil)
}

// AnalyzeEntities posts text to Presidio /analyze scoped to entityTypes
// (filtered to DefaultEntityTypes) and returns raw spans.
func (r *Redactor) AnalyzeEntities(ctx context.Context, text string, entityTypes []string) ([]Span, error) {
	return r.analyze(ctx, text, entityTypes)
}

// analyze posts to the sidecar's /analyze endpoint and decodes the span
// list. When entityTypesOverride is non-nil it replaces the configured
// EntityTypes for this call (after allowlist filtering).
func (r *Redactor) analyze(ctx context.Context, text string, entityTypesOverride []string) ([]Span, error) {
	entityTypes := r.cfg.EntityTypes
	if entityTypesOverride != nil {
		kept, dropped := filterToAllowlist(entityTypesOverride)
		if len(dropped) > 0 {
			slog.Warn(
				"redact: dropped entity types not in DefaultEntityTypes allowlist",
				slog.Any("dropped", dropped),
				slog.Any("kept", kept),
			)
		}
		if len(kept) > 0 {
			entityTypes = kept
		}
	}

	payload := map[string]any{
		"text":            text,
		"language":        r.cfg.Language,
		"score_threshold": r.cfg.ScoreThreshold,
	}
	if len(entityTypes) > 0 {
		payload["entities"] = entityTypes
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("redact: marshal payload: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(
		reqCtx, http.MethodPost,
		r.cfg.AnalyzerURL+"/analyze",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("redact: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("redact: analyze call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Read a small excerpt of the error body so the operator can
		// diagnose without flooding logs from a misbehaving sidecar.
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf(
			"redact: analyze returned %d: %s",
			resp.StatusCode, string(excerpt),
		)
	}

	var spans []Span
	if err := json.NewDecoder(resp.Body).Decode(&spans); err != nil {
		return nil, fmt.Errorf("redact: decode response: %w", err)
	}
	return spans, nil
}

// spliceSpans walks the spans in reverse-start order and replaces each
// in-place. Reverse order keeps the byte indices valid because earlier
// spans aren't shifted by later replacements. We also drop spans below
// the score threshold and silently skip ranges that fall outside the
// input — defensive, since a buggy sidecar could return a stale offset.
func spliceSpans(text string, spans []Span, threshold float64, reg *Registry, forceRedactMarkers bool) Result {
	counts := map[string]int{}
	if len(spans) == 0 {
		return Result{Text: text, EntityCounts: counts}
	}

	filtered := make([]Span, 0, len(spans))
	for _, s := range spans {
		if s.Score < threshold {
			continue
		}
		if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
			continue
		}
		filtered = append(filtered, s)
	}
	if len(filtered) == 0 {
		return Result{Text: text, EntityCounts: counts}
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Start > filtered[j].Start
	})

	out := []byte(text)
	for _, s := range filtered {
		original := text[s.Start:s.End]
		var marker string
		switch {
		case forceRedactMarkers:
			marker = fmt.Sprintf("[REDACTED:%s]", s.EntityType)
		case reg != nil:
			marker = reg.Placeholder(s.EntityType, original)
		default:
			marker = fmt.Sprintf("[REDACTED:%s]", s.EntityType)
		}
		out = append(out[:s.Start], append([]byte(marker), out[s.End:]...)...)
		counts[s.EntityType]++
	}
	return Result{Text: string(out), EntityCounts: counts}
}
