package redact

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAnalyzer spins up an httptest.Server that mimics the Presidio
// /analyze endpoint. Each test wires its own response shape — keeps
// assertions tight without standing up the real container.
func fakeAnalyzer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestNew_RequiresAnalyzerURL(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error when AnalyzerURL is empty")
	}
}

func TestNew_AppliesDefaults(t *testing.T) {
	r, err := New(Config{AnalyzerURL: "http://example/"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.cfg.Language != "en" {
		t.Errorf("Language default not applied; got %q", r.cfg.Language)
	}
	if r.cfg.ScoreThreshold != 0.5 {
		t.Errorf("ScoreThreshold default not applied; got %v", r.cfg.ScoreThreshold)
	}
	if r.cfg.Timeout != defaultAnalyzeTimeout {
		t.Errorf("Timeout default not applied; got %v", r.cfg.Timeout)
	}
	if len(r.cfg.EntityTypes) == 0 {
		t.Error("EntityTypes default (DefaultEntityTypes) not applied")
	}
}

func TestRedact_EmptyTextShortCircuits(t *testing.T) {
	called := false
	srv := fakeAnalyzer(t, func(_ http.ResponseWriter, _ *http.Request) { called = true })
	r, _ := New(Config{AnalyzerURL: srv.URL})

	res, err := r.Redact(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Text != "" {
		t.Errorf("expected empty text, got %q", res.Text)
	}
	if called {
		t.Error("Redact called the sidecar for empty input")
	}
}

func TestRedact_NoSpansPassesThrough(t *testing.T) {
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]Span{})
	})
	r, _ := New(Config{AnalyzerURL: srv.URL})

	res, err := r.Redact(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Text != "hello world" {
		t.Errorf("text mutated despite empty span list: %q", res.Text)
	}
	if len(res.EntityCounts) != 0 {
		t.Errorf("expected empty counts, got %v", res.EntityCounts)
	}
}

func TestRedact_SingleSpanReplaced(t *testing.T) {
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]Span{
			{Start: 4, End: 15, EntityType: "US_SSN", Score: 0.95},
		})
	})
	r, _ := New(Config{AnalyzerURL: srv.URL})

	res, err := r.Redact(context.Background(), "ssn 222-33-4444 today")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "ssn [REDACTED:US_SSN] today"
	if res.Text != want {
		t.Errorf("got %q, want %q", res.Text, want)
	}
	if res.EntityCounts["US_SSN"] != 1 {
		t.Errorf("expected one US_SSN hit, got %v", res.EntityCounts)
	}
}

func TestRedact_MultipleSpansSpliceInReverse(t *testing.T) {
	// "Bob lives in 222-33-4444 area and email me bob@x.com"
	//    0   4     12 13         24             45 48
	// Spans are intentionally provided OUT OF ORDER to verify the splice
	// loop sorts them by start descending — replacing earlier indices
	// first would shift later ones and corrupt the output.
	text := "Bob lives in 222-33-4444 area and email me bob@x.com"
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]Span{
			{Start: 0, End: 3, EntityType: "PERSON", Score: 0.9},
			{Start: 43, End: 52, EntityType: "EMAIL_ADDRESS", Score: 0.85},
			{Start: 13, End: 24, EntityType: "US_SSN", Score: 0.95},
		})
	})
	r, _ := New(Config{AnalyzerURL: srv.URL})

	res, err := r.Redact(context.Background(), text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"[REDACTED:PERSON]",
		"[REDACTED:US_SSN]",
		"[REDACTED:EMAIL_ADDRESS]",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("expected %q in result, got %q", want, res.Text)
		}
	}
	for _, leak := range []string{"222-33-4444", "bob@x.com", "Bob "} {
		if strings.Contains(res.Text, leak) {
			t.Errorf("raw value %q leaked into result %q", leak, res.Text)
		}
	}
	if got := res.EntityCounts["PERSON"]; got != 1 {
		t.Errorf("PERSON count = %d, want 1", got)
	}
}

func TestRedact_LowScoreSpansSkipped(t *testing.T) {
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]Span{
			{Start: 0, End: 7, EntityType: "PHONE_NUMBER", Score: 0.4}, // below threshold
		})
	})
	r, _ := New(Config{AnalyzerURL: srv.URL, ScoreThreshold: 0.5})

	res, _ := r.Redact(context.Background(), "555-1234 hello")
	if strings.Contains(res.Text, "[REDACTED") {
		t.Errorf("low-score span should not have been redacted: %q", res.Text)
	}
}

func TestRedact_OutOfBoundsSpansSkipped(t *testing.T) {
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]Span{
			{Start: -1, End: 5, EntityType: "X", Score: 1.0},
			{Start: 100, End: 200, EntityType: "X", Score: 1.0},
			{Start: 5, End: 5, EntityType: "X", Score: 1.0},
		})
	})
	r, _ := New(Config{AnalyzerURL: srv.URL})

	res, err := r.Redact(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Text != "hello world" {
		t.Errorf("invalid spans corrupted output: %q", res.Text)
	}
}

func TestRedact_PayloadIncludesEntityScopeAndContext(t *testing.T) {
	var captured map[string]any
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		_ = json.NewDecoder(req.Body).Decode(&captured)
		_ = json.NewEncoder(w).Encode([]Span{})
	})
	r, _ := New(Config{
		AnalyzerURL:    srv.URL,
		EntityTypes:    []string{"US_SSN", "PERSON"},
		ScoreThreshold: 0.7,
		Language:       "en",
	})

	if _, err := r.Redact(context.Background(), "hi"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured["text"] != "hi" {
		t.Errorf("text not forwarded; got %v", captured["text"])
	}
	if captured["language"] != "en" {
		t.Errorf("language not forwarded; got %v", captured["language"])
	}
	if captured["score_threshold"] != 0.7 {
		t.Errorf("score_threshold not forwarded; got %v", captured["score_threshold"])
	}
	gotEnts, _ := captured["entities"].([]any)
	if len(gotEnts) != 2 || gotEnts[0] != "US_SSN" || gotEnts[1] != "PERSON" {
		t.Errorf("entities scope not forwarded; got %v", captured["entities"])
	}
}

func TestAnalyzeEntities_SendsZeroWireThresholdAndScopedEntities(t *testing.T) {
	var captured map[string]any
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		_ = json.NewDecoder(req.Body).Decode(&captured)
		_ = json.NewEncoder(w).Encode([]Span{})
	})
	// pii_redact-style threshold of 0.5 must NOT leak onto the gate's
	// analyze call — the gate applies its own threshold in Go, so the wire
	// threshold has to be 0 or low-confidence passport hits get dropped.
	r, _ := New(Config{
		AnalyzerURL:    srv.URL,
		ScoreThreshold: 0.5,
		Language:       "en",
	})

	if _, err := r.AnalyzeEntities(
		context.Background(),
		"passport 123456789",
		[]string{"US_PASSPORT", "US_DRIVER_LICENSE"},
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured["score_threshold"] != float64(0) {
		t.Errorf("AnalyzeEntities must send wire score_threshold=0; got %v", captured["score_threshold"])
	}
	gotEnts, _ := captured["entities"].([]any)
	if len(gotEnts) != 2 || gotEnts[0] != "US_PASSPORT" || gotEnts[1] != "US_DRIVER_LICENSE" {
		t.Errorf("entities scope not forwarded; got %v", captured["entities"])
	}
}

func TestRedact_TimeoutPropagates(t *testing.T) {
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, _ *http.Request) {
		// Stall longer than the configured timeout so the request must
		// abort with a deadline error rather than blocking indefinitely.
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode([]Span{})
	})
	r, _ := New(Config{AnalyzerURL: srv.URL, Timeout: 10 * time.Millisecond})

	_, err := r.Redact(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "analyze call failed") {
		t.Errorf("unexpected error wrapping: %v", err)
	}
}

func TestRedact_NonTwoXXReturnsError(t *testing.T) {
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	r, _ := New(Config{AnalyzerURL: srv.URL})

	_, err := r.Redact(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected non-2xx error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("status code missing from error: %v", err)
	}
}

func TestRedact_ContextCancelAborts(t *testing.T) {
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		<-req.Context().Done()
	})
	r, _ := New(Config{AnalyzerURL: srv.URL, Timeout: time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Redact(ctx, "hi")
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestNew_FiltersUnknownEntitiesToAllowlist proves the in-code
// allowlist is enforced at construction. Even a YAML config that
// asks for UK_NHS / MEDICAL_LICENSE / DATE_TIME (the noisy default
// recognizers we never want to fire) cannot make it past New —
// the resulting Redactor's EntityTypes only contain entries from
// DefaultEntityTypes.
func TestNew_FiltersUnknownEntitiesToAllowlist(t *testing.T) {
	r, err := New(Config{
		AnalyzerURL: "http://example/",
		EntityTypes: []string{"US_SSN", "UK_NHS", "EMAIL_ADDRESS", "MEDICAL_LICENSE", "DATE_TIME"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, e := range r.cfg.EntityTypes {
		if _, ok := defaultEntityTypesSet[e]; !ok {
			t.Errorf("entity %q leaked past allowlist filter; final scope=%v", e, r.cfg.EntityTypes)
		}
	}
	want := map[string]bool{"US_SSN": true, "EMAIL_ADDRESS": true}
	if len(r.cfg.EntityTypes) != len(want) {
		t.Errorf("expected %d kept entities, got %d (%v)",
			len(want), len(r.cfg.EntityTypes), r.cfg.EntityTypes)
	}
	for _, e := range r.cfg.EntityTypes {
		if !want[e] {
			t.Errorf("unexpected kept entity %q", e)
		}
	}
}

// TestNew_FullyDisallowedScopeFallsBackToDefaults: when filtering
// removes EVERY requested entity, falling back to "all default
// recognizers" by sending an empty “entities“ to /analyze would be
// strictly worse than the documented DefaultEntityTypes scope.
// New() must instead substitute the canonical default.
func TestNew_FullyDisallowedScopeFallsBackToDefaults(t *testing.T) {
	r, err := New(Config{
		AnalyzerURL: "http://example/",
		EntityTypes: []string{"UK_NHS", "MEDICAL_LICENSE", "DATE_TIME"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(r.cfg.EntityTypes) != len(DefaultEntityTypes) {
		t.Errorf("expected fallback to DefaultEntityTypes (%d entries), got %d (%v)",
			len(DefaultEntityTypes), len(r.cfg.EntityTypes), r.cfg.EntityTypes)
	}
}

// TestRedact_AllowlistFilterPreventsUKNHSWirePayload is the proof-by-
// captured-payload that even when YAML widens to “[UK_NHS, US_SSN]“,
// the JSON sent to /analyze never contains UK_NHS. The in-code
// allowlist is the only sanctioned way to enable a recognizer; a
// YAML edit cannot extend the wire payload past the audited scope.
func TestRedact_AllowlistFilterPreventsUKNHSWirePayload(t *testing.T) {
	var captured map[string]any
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		_ = json.NewDecoder(req.Body).Decode(&captured)
		_ = json.NewEncoder(w).Encode([]Span{})
	})
	r, err := New(Config{
		AnalyzerURL: srv.URL,
		EntityTypes: []string{"UK_NHS", "US_SSN", "MEDICAL_LICENSE"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := r.Redact(context.Background(), "ssn 222-33-4444"); err != nil {
		t.Fatalf("Redact: %v", err)
	}
	ents, _ := captured["entities"].([]any)
	if len(ents) == 0 {
		t.Fatal("entities field missing or empty in /analyze payload")
	}
	for _, e := range ents {
		s, _ := e.(string)
		if s == "UK_NHS" || s == "MEDICAL_LICENSE" || s == "DATE_TIME" {
			t.Errorf("forbidden entity type %q reached /analyze payload: %v", s, ents)
		}
	}
	if !containsString(ents, "US_SSN") {
		t.Errorf("US_SSN missing from /analyze payload: %v", ents)
	}
}

func containsString(slice []any, want string) bool {
	for _, v := range slice {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}

// TestDefaultEntityTypes_ExcludesNoisyDefaultRecognizers is a guard
// against a future code change that "helpfully" adds UK_NHS or
// DATE_TIME to DefaultEntityTypes. If you really need them, you'll
// need to update this test too — that friction is the point.
func TestDefaultEntityTypes_ExcludesNoisyDefaultRecognizers(t *testing.T) {
	forbidden := []string{
		"UK_NHS",          // Mod-11 checksum recognizer; flags 10-digit numeric IDs.
		"DATE_TIME",       // Flags any ISO timestamp.
		"MAC_ADDRESS",     // Flags AA:BB:CC:DD:EE:FF look-alikes in tool args.
		"CRYPTO",          // Flags base58 strings.
		"MEDICAL_LICENSE", // Out of scope.
		"URL",             // Catches links in user prompts; not PII.
		"NRP",             // Nationality / religious / political affiliation.
	}
	for _, e := range forbidden {
		if _, ok := defaultEntityTypesSet[e]; ok {
			t.Errorf("DefaultEntityTypes must exclude %q; if you really need it, "+
				"update this test and document the rationale in the policy doc", e)
		}
	}
}

func TestSpliceMarkers_Idempotent(t *testing.T) {
	// Running twice must not corrupt or double-redact already-redacted
	// markers — useful when middleware is wrapped in a retry loop.
	first := spliceSpans("ssn 222-33-4444 today",
		[]Span{{Start: 4, End: 15, EntityType: "US_SSN", Score: 1}}, 0.5, nil, true)
	second := spliceSpans(first.Text, nil, 0.5, nil, true)
	if first.Text != second.Text {
		t.Errorf("idempotency broke: %q -> %q", first.Text, second.Text)
	}
}
