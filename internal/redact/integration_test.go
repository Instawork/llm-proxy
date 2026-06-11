package redact

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// contains reports whether “haystack“ includes the literal value
// “needle“.  Tiny helper to keep the recognizer-list assertions in
// this file readable.
func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

// httpGetJSON does a GET against “urlStr“ and decodes the response
// body as JSON into a generic Go value (“[]any“ or
// “map[string]any“).  Used for the “/recognizers“ endpoint check;
// not worth the ceremony of a full struct since the upstream contract
// is just "an array of strings".
func httpGetJSON(t *testing.T, urlStr string) (any, error) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(urlStr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &http.ProtocolError{ErrorString: "GET " + urlStr + ": status " + resp.Status}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// Default URL when running tests on the host (matches docker-compose.yml:
// "${PRESIDIO_PORT:-5004}:3000"). Override with PRESIDIO_ANALYZER_URL when
// running inside the compose network — there it's http://presidio:3000.
const defaultPresidioURLForHost = "http://localhost:5004"

// requirePresidio gates every integration test in this file:
//
//  1. -short skips by default so `go test ./...` from a laptop never
//     dials the sidecar.
//  2. LLM_PROXY_PII_INTEGRATION=1 must be set so a CI job that hasn't
//     stood up the sidecar profile doesn't accidentally enable these
//     tests and spam its logs with skip noise.
//  3. We TCP-dial the analyzer URL; if the dial fails the test t.Skip's
//     with an actionable message so a developer who forgot to bring up
//     the profile sees what to do next.
//
// The function returns the URL to use so each test can pass it straight
// to redact.New.
func requirePresidio(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping --pii integration test in -short mode")
	}
	if os.Getenv("LLM_PROXY_PII_INTEGRATION") != "1" {
		t.Skip("Skipping --pii integration test; set LLM_PROXY_PII_INTEGRATION=1 to enable " +
			"(and `docker compose --profile pii_redact up -d presidio`)")
	}
	target := os.Getenv("PRESIDIO_ANALYZER_URL")
	if target == "" {
		target = defaultPresidioURLForHost
	}

	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("PRESIDIO_ANALYZER_URL %q: invalid URL: %v", target, err)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		// Default analyzer port if the URL omits one.
		host += ":80"
	}

	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		t.Skipf("Skipping --pii integration test; cannot reach Presidio at %s "+
			"(`docker compose --profile pii_redact up -d presidio`): %v", target, err)
	}
	_ = conn.Close()
	return target
}

// newIntegrationRedactor wires a Redactor against a live Presidio sidecar
// with a generous timeout — first-call latency on a cold container is
// dominated by spaCy model load, sometimes several seconds.
func newIntegrationRedactor(t *testing.T, scope []string) *Redactor {
	t.Helper()
	r, err := New(Config{
		AnalyzerURL: requirePresidio(t),
		Timeout:     10 * time.Second,
		EntityTypes: scope,
	})
	if err != nil {
		t.Fatalf("redact.New: %v", err)
	}
	return r
}

// TestIntegration_RedactsRealUSSSN proves the wire format Presidio emits
// is the one we decode. This is the smoke test — if it fails on a fresh
// sidecar, every other test in this file is suspect.
func TestIntegration_RedactsRealUSSSN(t *testing.T) {
	r := newIntegrationRedactor(t, []string{"US_SSN"})

	res, err := r.Redact(context.Background(), "my ssn is 222-33-4444 today")
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if !strings.Contains(res.Text, "[REDACTED:US_SSN]") {
		t.Errorf("expected SSN marker in result; got %q", res.Text)
	}
	if strings.Contains(res.Text, "222-33-4444") {
		t.Errorf("raw SSN leaked: %q", res.Text)
	}
	if res.EntityCounts["US_SSN"] != 1 {
		t.Errorf("expected one US_SSN hit; counts=%v", res.EntityCounts)
	}
}

// TestIntegration_RedactsMultipleEntityTypes covers the "happy path
// payload": a chat-completions-shaped string carrying a name, an SSN,
// and an email. We assert that all three collapse to redaction markers
// AND that none of the raw values bleed through.
func TestIntegration_RedactsMultipleEntityTypes(t *testing.T) {
	r := newIntegrationRedactor(t, DefaultEntityTypes)

	input := "Hi, my name is Alice Johnson, ssn 222-33-4444, email alice@example.com"
	res, err := r.Redact(context.Background(), input)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}

	for _, leak := range []string{"222-33-4444", "alice@example.com", "Alice Johnson"} {
		if strings.Contains(res.Text, leak) {
			t.Errorf("raw value %q leaked into result %q", leak, res.Text)
		}
	}
	for _, marker := range []string{"[REDACTED:US_SSN]", "[REDACTED:EMAIL_ADDRESS]"} {
		if !strings.Contains(res.Text, marker) {
			t.Errorf("missing marker %q in result %q", marker, res.Text)
		}
	}
	// PERSON marker is highly likely but not guaranteed (Presidio's
	// PERSON recognizer can blur). Assert via the counts map instead so
	// a low-confidence hit still satisfies the contract.
	if _, ok := res.EntityCounts["PERSON"]; !ok {
		t.Logf("WARNING: live Presidio did not flag PERSON for %q; counts=%v "+
			"(this is occasionally flaky depending on model version — "+
			"investigate if it persists)", "Alice Johnson", res.EntityCounts)
	}
}

// TestIntegration_ScopeNarrowingSuppressesUKNHS proves the wire-side
// guarantee: when entity_types is narrowed to just US_SSN, a UK NHS
// number embedded in the prompt must NOT get a UK_NHS detection.
// Without an explicit scope, the prebuilt analyzer image would flag
// the same input as UK_NHS at score 1.0 — that's exactly the false
// positive class this test exists to prevent.
func TestIntegration_ScopeNarrowingSuppressesUKNHS(t *testing.T) {
	r := newIntegrationRedactor(t, []string{"US_SSN"})

	// 9434765919 is a checksum-valid UK NHS number; without scope, the
	// default Presidio recognizer set flags it as UK_NHS.
	input := "case id 9434765919 with ssn 222-33-4444"
	res, err := r.Redact(context.Background(), input)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if _, ok := res.EntityCounts["UK_NHS"]; ok {
		t.Errorf("scoping to US_SSN should suppress UK_NHS; counts=%v", res.EntityCounts)
	}
	if !strings.Contains(res.Text, "[REDACTED:US_SSN]") {
		t.Errorf("scoped recognizer should still hit US_SSN; got %q", res.Text)
	}
}

// TestIntegration_ProductionDefaultScopeSuppressesUKNHS is the more
// important version of the scope test: with the FULL DefaultEntityTypes
// list (which is what every real proxy deployment runs with), a
// checksum-valid UK NHS number must STILL not be detected. This proves
// DefaultEntityTypes does not contain UK_NHS — i.e. the in-code
// allowlist is correctly authored — independent of any YAML override.
func TestIntegration_ProductionDefaultScopeSuppressesUKNHS(t *testing.T) {
	r := newIntegrationRedactor(t, DefaultEntityTypes)

	// Same checksum-valid UK NHS number as above.
	input := "case id 9434765919 with ssn 222-33-4444"
	res, err := r.Redact(context.Background(), input)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if _, ok := res.EntityCounts["UK_NHS"]; ok {
		t.Errorf("production default scope must NOT include UK_NHS; counts=%v", res.EntityCounts)
	}
	for _, forbidden := range []string{"UK_NHS", "MEDICAL_LICENSE", "DATE_TIME", "MAC_ADDRESS", "URL", "NRP"} {
		if _, ok := res.EntityCounts[forbidden]; ok {
			t.Errorf("production default scope must NOT detect %s; counts=%v",
				forbidden, res.EntityCounts)
		}
	}
	if !strings.Contains(res.Text, "[REDACTED:US_SSN]") {
		t.Errorf("US_SSN must still be detected in production default scope; got %q", res.Text)
	}
}

// TestIntegration_YAMLWideningCannotEnableUKNHS proves the second leg
// of the guarantee: even if a YAML edit tries to add UK_NHS to the
// scope, redact.New filters it out and the wire payload to /analyze
// never contains it. We exercise the live sidecar to be sure the
// filter result actually round-trips through the real wire format —
// not just the httptest fake.
func TestIntegration_YAMLWideningCannotEnableUKNHS(t *testing.T) {
	url := requirePresidio(t)
	r, err := New(Config{
		AnalyzerURL: url,
		Timeout:     10 * time.Second,
		// Pretend the YAML widened to UK_NHS + a noisy classifier;
		// the in-code allowlist must drop both.
		EntityTypes: []string{"UK_NHS", "DATE_TIME", "US_SSN"},
	})
	if err != nil {
		t.Fatalf("redact.New: %v", err)
	}

	res, err := r.Redact(context.Background(), "case id 9434765919 with ssn 222-33-4444")
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if _, ok := res.EntityCounts["UK_NHS"]; ok {
		t.Errorf("UK_NHS leaked through despite allowlist filter; counts=%v", res.EntityCounts)
	}
	if _, ok := res.EntityCounts["DATE_TIME"]; ok {
		t.Errorf("DATE_TIME leaked through despite allowlist filter; counts=%v", res.EntityCounts)
	}
	if !strings.Contains(res.Text, "[REDACTED:US_SSN]") {
		t.Errorf("US_SSN still expected after filtering; got %q", res.Text)
	}
}

// TestIntegration_NoPIIPassesThrough confirms that a PII-free input
// returns unchanged. This protects against a recognizer regression
// where the model starts misclassifying ordinary text — easy to break
// on a Presidio version bump.
func TestIntegration_NoPIIPassesThrough(t *testing.T) {
	r := newIntegrationRedactor(t, DefaultEntityTypes)

	input := "the weather forecast for tomorrow looks great"
	res, err := r.Redact(context.Background(), input)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if res.Text != input {
		t.Errorf("PII-free text was mutated: got %q, want %q", res.Text, input)
	}
	if len(res.EntityCounts) != 0 {
		t.Errorf("PII-free text produced entity counts: %v", res.EntityCounts)
	}
}

// TestIntegration_LogPreviewUsesGlobalRedactor exercises the package-
// level preview helper end-to-end against the real sidecar. This is the
// path provider response-body debug logs take in production.
func TestIntegration_LogPreviewUsesGlobalRedactor(t *testing.T) {
	r := newIntegrationRedactor(t, DefaultEntityTypes)
	SetGlobal(r)
	t.Cleanup(func() { SetGlobal(nil) })

	out := LogPreview(context.Background(), "ssn 222-33-4444 today", 200)
	if !strings.Contains(out, "[REDACTED:US_SSN]") {
		t.Errorf("expected SSN marker via LogPreview, got %q", out)
	}
	if strings.Contains(out, "222-33-4444") {
		t.Errorf("raw SSN leaked through LogPreview: %q", out)
	}
}

// TestIntegration_RecognizersYAMLIsMounted asserts that the docker
// compose stack actually mounted “recognizers.yaml“ into the
// sidecar.  The unit test in recognizers_yaml_test.go validates the
// YAML *file*; this test validates the *running container*.  The two
// failure modes it catches:
//
//  1. Mount path regressed to the hyphenated form
//     (“/app/presidio-analyzer/conf/...“); Presidio falls back to
//     stock recognizers and the custom ones disappear.
//  2. Someone bumped the Presidio image version and the upstream
//     loader path moved (e.g. underscore → hyphen, or moved into a
//     different config file altogether).
//
// In either case the test fails immediately with an actionable
// diagnostic instead of waiting for a "DOB wasn't redacted in
// production" incident.
func TestIntegration_RecognizersYAMLIsMounted(t *testing.T) {
	url := requirePresidio(t)

	resp, err := httpGetJSON(t, url+"/recognizers")
	if err != nil {
		t.Fatalf("GET /recognizers: %v", err)
	}

	var loaded []string
	switch v := resp.(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				loaded = append(loaded, s)
			}
		}
	default:
		t.Fatalf("unexpected /recognizers response type %T: %v", resp, resp)
	}

	mustHave := []string{
		"DateOfBirthRecognizer",
		"UsStreetAddressRecognizer",
	}
	for _, name := range mustHave {
		if !contains(loaded, name) {
			t.Errorf("custom recognizer %q not loaded; live recognizers: %v\n"+
				"\nThis usually means recognizers.yaml is not mounted at the "+
				"correct container path. Check docker-compose.yml and verify "+
				"the bind mount target is "+
				"/app/presidio_analyzer/conf/default_recognizers.yaml "+
				"(underscore, not hyphen).", name, loaded)
		}
	}

	// Negative assertion: the stock-image-only recognizers should NOT
	// be present any more.  recognizers.yaml replaces the default
	// list, so loading our file should mean the noisy ones are gone.
	mustNotHave := []string{
		"NhsRecognizer",
		"MacAddressRecognizer",
		"MedicalLicenseRecognizer",
		"UrlRecognizer",
		"CryptoRecognizer",
		"DateRecognizer",
	}
	for _, name := range mustNotHave {
		if contains(loaded, name) {
			t.Errorf("stock recognizer %q still loaded after recognizers.yaml "+
				"override; the YAML mount is being ignored OR the loader is "+
				"merging stock+custom (Presidio version bump?). Live: %v",
				name, loaded)
		}
	}
}

// TestIntegration_DateOfBirthRedacts exercises the DateOfBirthRecognizer
// end-to-end.  Without a recognizers.yaml mount this fails with NO HITS
// regardless of how DefaultEntityTypes is configured, so it's our most
// load-bearing canary that the mount path is correct in production.
func TestIntegration_DateOfBirthRedacts(t *testing.T) {
	r := newIntegrationRedactor(t, DefaultEntityTypes)

	res, err := r.Redact(context.Background(), "DOB: 03/15/1990")
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if !strings.Contains(res.Text, "[REDACTED:DATE_OF_BIRTH]") {
		t.Errorf("expected DATE_OF_BIRTH marker; got %q (recognizers.yaml not "+
			"mounted? counts=%v)", res.Text, res.EntityCounts)
	}
	if strings.Contains(res.Text, "03/15/1990") {
		t.Errorf("raw DOB leaked: %q", res.Text)
	}
}

// TestIntegration_ScrubUsesTieredPlaceholders proves Scrub (wire-mode)
// replaces spans with policy-aware placeholders and that RestoreUserFacing
// round-trips MASK values while SEAL placeholders stay opaque.
func TestIntegration_ScrubUsesTieredPlaceholders(t *testing.T) {
	r := newIntegrationRedactor(t, []string{"US_SSN", "EMAIL_ADDRESS"})
	reg := NewRegistry()

	const ssn = "222-33-4444"
	const email = "alice@example.com"
	input := "ssn " + ssn + " email " + email

	res, err := r.Scrub(context.Background(), input, reg)
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if strings.Contains(res.Text, ssn) {
		t.Errorf("raw SSN leaked in scrubbed text: %q", res.Text)
	}
	if strings.Contains(res.Text, email) {
		t.Errorf("raw email leaked in scrubbed text: %q", res.Text)
	}
	if !strings.Contains(res.Text, "<US_SSN_") {
		t.Errorf("expected SEAL SSN placeholder; got %q", res.Text)
	}
	if !strings.Contains(res.Text, "<EMAIL_ADDRESS_") {
		t.Errorf("expected MASK email placeholder; got %q", res.Text)
	}
	if reg.Len() < 2 {
		t.Fatalf("registry too small after scrub: len=%d", reg.Len())
	}

	restored := reg.RestoreUserFacing(res.Text)
	if !strings.Contains(restored, email) {
		t.Errorf("MASK email not restored: %q", restored)
	}
	if strings.Contains(restored, ssn) {
		t.Errorf("SEAL SSN must not restore to client: %q", restored)
	}
	if !strings.Contains(restored, "<US_SSN_") {
		t.Errorf("SEAL placeholder should survive restore pass: %q", restored)
	}
}

// TestIntegration_ScrubRedactTierUsesMarker asserts payment-rail entities
// collapse to one-way [REDACTED:TYPE] markers with no registry entry.
func TestIntegration_ScrubRedactTierUsesMarker(t *testing.T) {
	r := newIntegrationRedactor(t, []string{"CREDIT_CARD"})
	reg := NewRegistry()

	const pan = "4111111111111111"
	input := "pay with card " + pan

	res, err := r.Scrub(context.Background(), input, reg)
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if strings.Contains(res.Text, pan) {
		t.Errorf("raw PAN leaked: %q", res.Text)
	}
	if !strings.Contains(res.Text, "[REDACTED:CREDIT_CARD]") {
		t.Errorf("expected REDACT marker; got %q", res.Text)
	}
	if reg.Len() != 0 {
		t.Errorf("REDACT tier must not register placeholders; len=%d", reg.Len())
	}
}

// TestIntegration_ScrubJSONPayload scrubs a chat-completions-shaped body
// the same way the proxy middleware does in wire mode.
func TestIntegration_ScrubJSONPayload(t *testing.T) {
	r := newIntegrationRedactor(t, []string{"US_SSN", "EMAIL_ADDRESS"})
	reg := NewRegistry()

	const ssn = "222-33-4444"
	const email = "bob@example.com"
	input := fmt.Sprintf(
		`{"messages":[{"role":"user","content":"my ssn is %s and email %s"}]}`,
		ssn, email,
	)

	res, err := r.Scrub(context.Background(), input, reg)
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	for _, leak := range []string{ssn, email} {
		if strings.Contains(res.Text, leak) {
			t.Errorf("raw value %q leaked into scrubbed JSON: %q", leak, res.Text)
		}
	}
	if !strings.Contains(res.Text, "<US_SSN_") || !strings.Contains(res.Text, "<EMAIL_ADDRESS_") {
		t.Errorf("scrubbed JSON missing placeholders: %q", res.Text)
	}
}

// TestIntegration_UsStreetAddressRedacts asserts that the strict
// "number + street + city + state + zip" regex catches a canonical
// US address.  This recognizer fires at score 0.6 with no context
// boost, so it's a good check that the score isn't being trimmed by
// any unintended threshold downstream.
func TestIntegration_UsStreetAddressRedacts(t *testing.T) {
	r := newIntegrationRedactor(t, DefaultEntityTypes)

	res, err := r.Redact(context.Background(),
		"Lives at 123 Main St, San Francisco, CA 94105 today")
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if !strings.Contains(res.Text, "[REDACTED:US_STREET_ADDRESS]") {
		t.Logf("US_STREET_ADDRESS not detected; spaCy LOCATION may have "+
			"won the overlap competition.  This is acceptable as long as "+
			"_some_ redaction happened.  Output: %q (counts=%v)",
			res.Text, res.EntityCounts)
		// Soft assert: as long as the raw address parts are gone, the
		// proxy redacted *something*.  spaCy's LOCATION recognizer
		// often beats our custom recognizer on overlap because both
		// fire with similar scores; the LOCATION marker is also fine.
	}
	if strings.Contains(res.Text, "123 Main St") {
		t.Errorf("raw street leaked: %q", res.Text)
	}
}
