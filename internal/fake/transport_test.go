package fake_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/fake"
	"github.com/Instawork/llm-proxy/internal/providers"
)

type countingRT struct {
	calls atomic.Int32
}

func (c *countingRT) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusTeapot,
		Body:       io.NopCloser(strings.NewReader("inner")),
	}, nil
}

func TestTransport_Success_NoInnerCall(t *testing.T) {
	inner := &countingRT{}
	tr := fake.NewTransport(inner, "openai", fake.Config{Enabled: true})
	body := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest(http.MethodPost, "http://proxy/openai/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if inner.calls.Load() != 0 {
		t.Fatal("inner transport must not be called")
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q; strict SDKs (openrouter) reject non-JSON success responses", ct)
	}
	data, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(data, []byte(`"system_fingerprint":null`)) {
		t.Fatalf("OpenAI-shaped body must carry system_fingerprint (required by the OpenRouter SDK): %s", data)
	}
	p := providers.NewOpenAIProxy()
	meta, err := p.ParseResponseMetadata(bytes.NewReader(data), false)
	if err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	if meta.InputTokens <= 0 || meta.OutputTokens <= 0 {
		t.Fatalf("tokens: in=%d out=%d", meta.InputTokens, meta.OutputTokens)
	}
}

func TestTransport_Disabled_CallsInner(t *testing.T) {
	inner := &countingRT{}
	tr := fake.NewTransport(inner, "openai", fake.Config{Enabled: false})
	req, _ := http.NewRequest(http.MethodGet, "http://proxy/", nil)
	_, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if inner.calls.Load() != 1 {
		t.Fatal("inner must be called when fake disabled")
	}
}

func TestTransport_Chaos503(t *testing.T) {
	tr := fake.NewTransport(&countingRT{}, "openai", fake.Config{
		Enabled:          true,
		ChaosFailureRate: 1,
		ChaosSeed:        1,
	})
	body := []byte(`{"model":"gpt-4o-mini","messages":[]}`)
	req, _ := http.NewRequest(http.MethodPost, "http://proxy/openai/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	for range 20 {
		resp, err := tr.RoundTrip(req)
		req.Body = io.NopCloser(bytes.NewReader(body))
		if err != nil {
			if err == io.ErrUnexpectedEOF {
				continue
			}
			t.Fatalf("round trip: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Fatalf("failure Content-Type = %q, want application/json", ct)
			}
			return
		}
	}
	t.Fatal("expected chaos failure status")
}

func TestTransport_LatencyRespectsContext(t *testing.T) {
	tr := fake.NewTransport(&countingRT{}, "openai", fake.Config{Enabled: true})
	body := []byte(`{"model":"gpt-4o-mini","messages":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://proxy/openai/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(fake.HeaderLatencyMs, "500")

	_, err := tr.RoundTrip(req)
	if err == nil {
		t.Fatal("expected context deadline")
	}
}

func TestChaos_PickDeterministicWithSeed(t *testing.T) {
	c1 := fake.NewChaos(true, 0.5, 42)
	c2 := fake.NewChaos(true, 0.5, 42)
	var a, b []fake.Outcome
	for range 10 {
		a = append(a, c1.Pick(-1))
		b = append(b, c2.Pick(-1))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("outcomes diverged at %d: %v vs %v", i, a[i], b[i])
		}
	}
}
