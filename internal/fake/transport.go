package fake

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
)

const defaultOutputTokens = 16

// Config carries fake-upstream settings from YAML plus runtime gate state.
type Config struct {
	Enabled         bool
	ChaosFailureRate float64
	ChaosSeed       int64
	LatencyMS       int
	JitterMS        int
	Estimation      providers.YAMLConfigEstimationAdapter
}

// Transport synthesizes provider responses without calling the inner RoundTripper.
type Transport struct {
	inner    http.RoundTripper
	provider string
	cfg      Config
	chaos    *Chaos
}

func NewTransport(inner http.RoundTripper, provider string, cfg Config) *Transport {
	if inner == nil {
		inner = http.DefaultTransport
	}
	enabled := cfg.Enabled
	return &Transport{
		inner:    inner,
		provider: provider,
		cfg:      cfg,
		chaos:    NewChaos(enabled, cfg.ChaosFailureRate, cfg.ChaosSeed),
	}
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.cfg.Enabled {
		return t.inner.RoundTrip(req)
	}

	if err := t.sleep(req); err != nil {
		return nil, err
	}

	outcome := t.chaos.Pick(parseChaosRate(req))
	switch outcome {
	case OutcomeConnError:
		return nil, errFakeConn
	case Outcome503:
		return t.jsonResponse(req, http.StatusServiceUnavailable, failureBody(t.provider, 503)), nil
	case Outcome429:
		resp := t.jsonResponse(req, http.StatusTooManyRequests, failureBody(t.provider, 429))
		resp.Header.Set("Retry-After", "1")
		return resp, nil
	case Outcome500:
		return t.jsonResponse(req, http.StatusInternalServerError, failureBody(t.provider, 500)), nil
	default:
		inTok, model := t.estimateTokens(req)
		outTok := parseOutputTokens(req, defaultOutputTokens)
		if model == "" {
			model = defaultModel(t.provider)
		}
		body := successBody(t.provider, model, inTok, outTok)
		return t.jsonResponse(req, http.StatusOK, body), nil
	}
}

func (t *Transport) sleep(req *http.Request) error {
	ms := t.cfg.LatencyMS
	if hdr := req.Header.Get(HeaderLatencyMs); hdr != "" {
		if v, err := strconv.Atoi(hdr); err == nil && v >= 0 {
			ms = v
		}
	}
	if t.cfg.JitterMS > 0 {
		t.chaos.mu.Lock()
		jitter := t.chaos.rng.Intn(t.cfg.JitterMS + 1)
		t.chaos.mu.Unlock()
		ms += jitter
	}
	if ms <= 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-req.Context().Done():
		return req.Context().Err()
	case <-timer.C:
		return nil
	}
}

func (t *Transport) estimateTokens(req *http.Request) (int, string) {
	if req.Body == nil {
		return 1, ""
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return 1, ""
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(bytes.NewReader(body))
	est, model := providers.EstimateRequestTokens(clone, t.cfg.Estimation, nil)
	if est < 1 {
		est = 1
	}
	if model == "" {
		model = parseModelFromBody(body)
	}
	return est, model
}

func (t *Transport) jsonResponse(req *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}
}

func parseChaosRate(req *http.Request) float64 {
	hdr := req.Header.Get(HeaderChaosRate)
	if hdr == "" {
		return -1
	}
	v, err := strconv.ParseFloat(hdr, 64)
	if err != nil {
		return -1
	}
	return v
}

func parseOutputTokens(req *http.Request, fallback int) int {
	hdr := req.Header.Get(HeaderOutputTokens)
	if hdr == "" {
		return fallback
	}
	v, err := strconv.Atoi(hdr)
	if err != nil || v < 0 {
		return fallback
	}
	return v
}

func parseModelFromBody(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &m) == nil && m.Model != "" {
		return m.Model
	}
	return ""
}

func defaultModel(provider string) string {
	switch provider {
	case "anthropic":
		return "claude-3-5-haiku-20241022"
	case "gemini":
		return "gemini-2.5-flash"
	default:
		return "gpt-4o-mini"
	}
}
