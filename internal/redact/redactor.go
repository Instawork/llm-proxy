// Package redact provides PII redaction for the llm-proxy by calling a
// Presidio analyzer sidecar over HTTP and replacing detected spans with
// policy-aware tokens:
//
//   - MASK  → numbered placeholders (<PII_PERSON_1>) restored to the client
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
	"strings"
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

type DetectedEntity struct {
	EntityType string
	Text       string
	Policy     string
	Score      float64
	Start      int
	End        int
}

// AllowedEntity is a Presidio hit that matched middle-ground policy and
// was intentionally left in the outbound body.
type AllowedEntity struct {
	EntityType string
	Text       string
	Score      float64
	Reason     string
	Start      int
	End        int
}

// Result is the return value of Redact: the redacted text plus the
// distinct entity types observed (useful for log/metric tags without
// leaking the raw values).
type Result struct {
	Text             string
	EntityCounts     map[string]int
	DetectedEntities []DetectedEntity
	AllowedEntities  []AllowedEntity
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

	// AllowTestEmails, when nil or true, lets obvious fixture emails
	// (example.com, test@*, dev@*) pass through middle-ground filtering.
	AllowTestEmails *bool

	// AnalyzeConcurrency caps parallel /analyze calls when scrubbing JSON
	// with multiple user-content strings. Zero defaults to 4.
	AnalyzeConcurrency int

	// AnalyzeCache optionally caches Presidio span lists per content block.
	AnalyzeCache AnalyzeCache
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
	cfg             Config
	client          *http.Client
	allowTestEmails bool
	analyzeCache    AnalyzeCache
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
	allowTestEmails := true
	if cfg.AllowTestEmails != nil {
		allowTestEmails = *cfg.AllowTestEmails
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
		// Per-request context deadlines in analyze() govern /analyze latency;
		// a non-zero Client.Timeout would cap scaled body-size budgets.
		client = &http.Client{}
	}
	return &Redactor{
		cfg:             cfg,
		client:          client,
		allowTestEmails: allowTestEmails,
		analyzeCache:    cfg.AnalyzeCache,
	}, nil
}

// Redact returns “text“ with every detected span replaced by
// “[REDACTED:ENTITY_TYPE]“ regardless of policy tier. Use this for
// one-way observability (log previews) where placeholders must not leak.
func (r *Redactor) Redact(ctx context.Context, text string) (Result, error) {
	if json.Valid([]byte(text)) {
		return r.scrubJSON(ctx, text, nil, true)
	}
	return r.scrub(ctx, text, nil, true)
}

// Scrub analyzes text and replaces spans using policy-aware placeholders.
// When reg is non-nil, MASK/SEAL entries are recorded for response restore.
func (r *Redactor) Scrub(ctx context.Context, text string, reg *Registry) (Result, error) {
	if json.Valid([]byte(text)) {
		return r.scrubJSON(ctx, text, reg, false)
	}
	return r.scrub(ctx, text, reg, false)
}

func (r *Redactor) scrub(ctx context.Context, text string, reg *Registry, forceRedactMarkers bool) (Result, error) {
	if text == "" {
		return Result{Text: text, EntityCounts: map[string]int{}}, nil
	}
	analysisText := prepareJSONForAnalysis(text, AdapterForContext(ctx))
	spans, err := r.analyzeSpans(ctx, analysisText)
	if err != nil {
		return Result{}, err
	}
	return spliceSpans(text, spans, r.cfg.ScoreThreshold, reg, forceRedactMarkers, r.allowTestEmails), nil
}

func (r *Redactor) analyzeSpans(ctx context.Context, analysisText string) ([]Span, error) {
	if prefetch := analyzeCachePrefetchFromContext(ctx); prefetch != nil {
		if spans, ok := prefetch[analysisText]; ok {
			return spans, nil
		}
	}
	if r.analyzeCache != nil {
		if spans, ok := r.analyzeCache.Get(ctx, analysisText); ok {
			return spans, nil
		}
	}
	spans, err := r.analyze(ctx, analysisText, nil, r.cfg.ScoreThreshold)
	if err != nil {
		return nil, err
	}
	if r.analyzeCache != nil {
		r.analyzeCache.Set(ctx, analysisText, spans)
	}
	return spans, nil
}

// Analyze posts text to Presidio /analyze using the redactor's configured
// entity scope and returns raw spans without redacting the input. The
// redactor's ScoreThreshold is applied server-side.
func (r *Redactor) Analyze(ctx context.Context, text string) ([]Span, error) {
	return r.analyze(ctx, text, nil, r.cfg.ScoreThreshold)
}

// AnalyzeEntities posts text to Presidio /analyze scoped to entityTypes
// (filtered to DefaultEntityTypes) and returns ALL matching spans.
//
// Unlike Scrub/Analyze, this deliberately sends a wire score_threshold of 0
// so Presidio does NOT filter spans server-side. Callers (the ID gate) apply
// their own threshold in Go. This decouples the gate's blocking sensitivity
// from pii_redact.score_threshold: stock Presidio scores US_PASSPORT at only
// ~0.40, so reusing the 0.5 redaction threshold here would drop real passport
// hits before the gate ever evaluated them.
func (r *Redactor) AnalyzeEntities(ctx context.Context, text string, entityTypes []string) ([]Span, error) {
	return r.analyze(ctx, text, entityTypes, 0)
}

// analyze posts to the sidecar's /analyze endpoint and decodes the span
// list. When entityTypesOverride is non-nil it replaces the configured
// EntityTypes for this call (after allowlist filtering). wireThreshold is
// forwarded to Presidio as score_threshold (server-side span filtering).
func (r *Redactor) analyze(ctx context.Context, text string, entityTypesOverride []string, wireThreshold float64) ([]Span, error) {
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
		"score_threshold": wireThreshold,
	}
	if len(entityTypes) > 0 {
		payload["entities"] = entityTypes
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("redact: marshal payload: %w", err)
	}

	timeout := AnalyzeTimeoutFromContext(ctx, r.cfg.Timeout)
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
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

type byteSpan struct {
	Span
	byteStart int
	byteEnd   int
}

// spliceSpans walks the spans in reverse-start order and replaces each
// in-place. Presidio reports character offsets, so spans are translated
// to byte offsets before slicing the Go string. Reverse order keeps the
// byte indices valid because earlier spans aren't shifted by later
// replacements. We also drop spans below the score threshold and
// silently skip ranges that fall outside the input — defensive, since a
// buggy sidecar could return a stale offset.
func spliceSpans(text string, spans []Span, threshold float64, reg *Registry, forceRedactMarkers bool, allowTestEmails bool) Result {
	counts := map[string]int{}
	if len(spans) == 0 {
		return Result{Text: text, EntityCounts: counts}
	}

	filtered := make([]byteSpan, 0, len(spans))
	allowed := make([]AllowedEntity, 0)
	for _, s := range spans {
		if s.Score < threshold {
			continue
		}
		byteStart, byteEnd, ok := spanCharacterOffsetsToBytes(text, s.Start, s.End)
		if !ok {
			continue
		}
		original := text[byteStart:byteEnd]
		if reason, allow := middleGroundAllowReason(original, s.EntityType, allowTestEmails); allow {
			allowed = append(allowed, AllowedEntity{
				EntityType: s.EntityType,
				Text:       original,
				Score:      s.Score,
				Reason:     reason,
				Start:      s.Start,
				End:        s.End,
			})
			continue
		}
		filtered = append(filtered, byteSpan{Span: s, byteStart: byteStart, byteEnd: byteEnd})
	}
	filtered = nonOverlappingByteSpans(filtered)
	if len(filtered) == 0 {
		return Result{Text: text, EntityCounts: counts, AllowedEntities: allowed}
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].byteStart > filtered[j].byteStart
	})

	out := []byte(text)
	entities := make([]DetectedEntity, 0, len(filtered))
	for _, s := range filtered {
		original := text[s.byteStart:s.byteEnd]
		policy := PolicyFor(s.EntityType)
		var marker string
		switch {
		case forceRedactMarkers:
			marker = fmt.Sprintf("[REDACTED:%s]", s.EntityType)
		case reg != nil:
			marker = reg.Placeholder(s.EntityType, original)
		default:
			marker = fmt.Sprintf("[REDACTED:%s]", s.EntityType)
		}
		out = append(out[:s.byteStart], append([]byte(marker), out[s.byteEnd:]...)...)
		counts[s.EntityType]++
		entities = append(entities, DetectedEntity{
			EntityType: s.EntityType,
			Text:       original,
			Policy:     policy.String(),
			Score:      s.Score,
			Start:      s.Start,
			End:        s.End,
		})
	}
	return Result{Text: string(out), EntityCounts: counts, DetectedEntities: entities, AllowedEntities: allowed}
}

func middleGroundAllowReason(value, entityType string, allowTestEmails bool) (string, bool) {
	switch entityType {
	case "PERSON":
		if len(strings.Fields(value)) < 2 {
			return "single_token_person", true
		}
	case "LOCATION":
		if !looksLikeStreetAddress(value) {
			return "non_street_location", true
		}
	case "IP_ADDRESS":
		if isPrivateOrLoopbackIP(value) {
			return "private_ip", true
		}
	case "EMAIL_ADDRESS":
		if allowTestEmails && isTestEmail(value) {
			return "test_email", true
		}
	}
	return "", false
}

func AllowedEntityCounts(allowed []AllowedEntity) map[string]int {
	if len(allowed) == 0 {
		return nil
	}
	counts := make(map[string]int, len(allowed))
	for _, e := range allowed {
		counts[e.EntityType]++
	}
	return counts
}

func looksLikeStreetAddress(value string) bool {
	lower := strings.ToLower(value)
	hasDigit := false
	for _, ch := range lower {
		if ch >= '0' && ch <= '9' {
			hasDigit = true
			break
		}
	}
	if !hasDigit {
		return false
	}
	for _, token := range []string{
		" street", " st", " avenue", " ave", " road", " rd", " boulevard", " blvd",
		" lane", " ln", " drive", " dr", " court", " ct", " place", " pl", " way",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func nonOverlappingByteSpans(spans []byteSpan) []byteSpan {
	sort.SliceStable(spans, func(i, j int) bool {
		if spanPolicyPriority(spans[i]) != spanPolicyPriority(spans[j]) {
			return spanPolicyPriority(spans[i]) > spanPolicyPriority(spans[j])
		}
		if spans[i].Score != spans[j].Score {
			return spans[i].Score > spans[j].Score
		}
		if spanByteLen(spans[i]) != spanByteLen(spans[j]) {
			return spanByteLen(spans[i]) > spanByteLen(spans[j])
		}
		return spans[i].byteStart < spans[j].byteStart
	})

	out := make([]byteSpan, 0, len(spans))
	for _, s := range spans {
		if overlapsAnyByteSpan(s, out) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func spanPolicyPriority(s byteSpan) int {
	switch PolicyFor(s.EntityType) {
	case PolicyRedact:
		return 3
	case PolicySeal:
		return 2
	case PolicyMask:
		return 1
	default:
		return 0
	}
}

func spanByteLen(s byteSpan) int {
	return s.byteEnd - s.byteStart
}

func overlapsAnyByteSpan(s byteSpan, spans []byteSpan) bool {
	for _, existing := range spans {
		if s.byteStart < existing.byteEnd && existing.byteStart < s.byteEnd {
			return true
		}
	}
	return false
}

type jsonContainerState int

const (
	jsonObjectKey jsonContainerState = iota
	jsonObjectColon
	jsonObjectValue
	jsonObjectCommaOrEnd
	jsonArrayValue
	jsonArrayCommaOrEnd
)

func copyOrMaskJSONString(in, out []rune, start int, mask bool) int {
	i := start + 1
	for i < len(in) {
		switch in[i] {
		case '\\':
			if mask {
				out[i] = ' '
			}
			i++
			if i < len(in) {
				if mask {
					out[i] = ' '
				}
				i++
			}
		case '"':
			return i + 1
		default:
			if mask {
				out[i] = ' '
			}
			i++
		}
	}
	return i
}

func markJSONValueSeen(stack *[]jsonContainerState) {
	if len(*stack) == 0 {
		return
	}
	switch (*stack)[len(*stack)-1] {
	case jsonObjectValue:
		(*stack)[len(*stack)-1] = jsonObjectCommaOrEnd
	case jsonArrayValue:
		(*stack)[len(*stack)-1] = jsonArrayCommaOrEnd
	}
}

func isJSONWhitespace(ch rune) bool {
	return ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t'
}

func isJSONDelimiter(ch rune) bool {
	return isJSONWhitespace(ch) || ch == ',' || ch == ']' || ch == '}'
}

func spanCharacterOffsetsToBytes(text string, start, end int) (int, int, bool) {
	if start < 0 || start >= end {
		return 0, 0, false
	}

	runeIndex := 0
	byteStart := -1
	byteEnd := -1
	for byteIndex := range text {
		if runeIndex == start {
			byteStart = byteIndex
		}
		if runeIndex == end {
			byteEnd = byteIndex
			break
		}
		runeIndex++
	}
	if byteStart < 0 && start == runeIndex {
		byteStart = len(text)
	}
	if byteEnd < 0 && end == runeIndex {
		byteEnd = len(text)
	}
	if byteStart < 0 || byteEnd < 0 || byteStart >= byteEnd {
		return 0, 0, false
	}
	return byteStart, byteEnd, true
}
