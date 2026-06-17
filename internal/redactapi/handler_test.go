package redactapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/redact"
)

type stubRedactor struct {
	out redact.Result
	err error
}

func (s *stubRedactor) Redact(_ context.Context, text string) (redact.Result, error) {
	if s.err != nil {
		return redact.Result{}, s.err
	}
	if s.out.Text != "" || len(s.out.EntityCounts) > 0 {
		return s.out, nil
	}
	return redact.Result{Text: "[redacted] " + text, EntityCounts: map[string]int{"US_SSN": 1}}, nil
}

type stubKeyStore struct {
	record *apikeys.APIKey
	err    error
}

func (s *stubKeyStore) LookupProxyKey(_ context.Context, bearer string) (*apikeys.APIKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.record == nil {
		return nil, errors.New("not found")
	}
	return s.record, nil
}

func TestHandler_TextMode_Redacts(t *testing.T) {
	h := NewHandler(&stubRedactor{}, &stubKeyStore{record: &apikeys.APIKey{PK: apikeys.KeyPrefix + "abc"}}, Config{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/redact?mode=text", strings.NewReader("SSN 123-45-6789"))
	req.Header.Set("Authorization", "Bearer "+apikeys.KeyPrefix+"abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("content-type=%q", ct)
	}
	if !strings.Contains(rec.Body.String(), "[redacted]") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestHandler_JSONMode(t *testing.T) {
	h := NewHandler(&stubRedactor{
		out: redact.Result{Text: "clean", EntityCounts: map[string]int{"US_SSN": 1}},
	}, &stubKeyStore{record: &apikeys.APIKey{PK: apikeys.KeyPrefix + "abc"}}, Config{}, nil)
	body := `{"text":"SSN 123-45-6789"}`
	req := httptest.NewRequest(http.MethodPost, "/redact?mode=json", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+apikeys.KeyPrefix+"abc")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"text":"clean"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"US_SSN":1`) {
		t.Fatalf("entities missing: %s", rec.Body.String())
	}
}

func TestHandler_Unauthorized(t *testing.T) {
	h := NewHandler(&stubRedactor{}, &stubKeyStore{}, Config{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/redact", strings.NewReader("hello"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestHandler_InvalidKey(t *testing.T) {
	h := NewHandler(&stubRedactor{}, &stubKeyStore{err: errors.New("disabled")}, Config{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/redact", strings.NewReader("hello"))
	req.Header.Set("x-api-key", apikeys.KeyPrefix+"bad")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestHandler_DevAllowUnauthenticated(t *testing.T) {
	h := NewHandler(&stubRedactor{}, nil, Config{AllowUnauthenticated: true}, nil)
	req := httptest.NewRequest(http.MethodPost, "/redact?mode=text", strings.NewReader("hello"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_FailClosed(t *testing.T) {
	h := NewHandler(&stubRedactor{err: errors.New("redact: analyze call failed: timeout")}, &stubKeyStore{record: &apikeys.APIKey{PK: apikeys.KeyPrefix + "abc"}}, Config{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/redact", strings.NewReader("hello"))
	req.Header.Set("Authorization", "Bearer "+apikeys.KeyPrefix+"abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_RedactFailureReason(t *testing.T) {
	cases := []struct {
		err    error
		reason string
	}{
		{errors.New("redact: analyze call failed: dial tcp"), "analyze_call_failed"},
		{errors.New("redact: analyze returned 503: oops"), "analyze_http_503"},
		{errors.New("redact: decode response: invalid"), "analyze_decode_failed"},
	}
	for _, tc := range cases {
		if got := redactFailureReason(tc.err); got != tc.reason {
			t.Fatalf("err=%v got %q want %q", tc.err, got, tc.reason)
		}
	}
}

func TestHandler_BodyTooLarge(t *testing.T) {
	h := NewHandler(&stubRedactor{}, &stubKeyStore{record: &apikeys.APIKey{PK: apikeys.KeyPrefix + "abc"}}, Config{MaxBodyBytes: 4}, nil)
	req := httptest.NewRequest(http.MethodPost, "/redact", strings.NewReader("123456"))
	req.Header.Set("Authorization", "Bearer "+apikeys.KeyPrefix+"abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestHandler_InvalidMode(t *testing.T) {
	h := NewHandler(&stubRedactor{}, &stubKeyStore{record: &apikeys.APIKey{PK: apikeys.KeyPrefix + "abc"}}, Config{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/redact?mode=xml", strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer "+apikeys.KeyPrefix+"abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}
