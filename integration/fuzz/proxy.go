package fuzz

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/integration/live"
)

type Proxy struct {
	base    string
	timeout time.Duration
	client  *http.Client
	report  *Report
}

func NewProxy(base string, timeout time.Duration, report *Report) *Proxy {
	return &Proxy{
		base:    strings.TrimRight(base, "/"),
		timeout: timeout,
		client:  &http.Client{Timeout: timeout},
		report:  report,
	}
}

type ChatOpts struct {
	APIKey                     string
	Model                      string
	Content                    string
	ChaosRate                  *float64
	FakeOutcome                string
	OutputTok                  int
	CachedTokens               int
	FakeEchoPlaceholders       bool
	FakeEchoPlaceholdersFormat string
	LatencyMS                  int
	TestMode                   string
	MaxTokens                  int
}

type ChatResult struct {
	Status  int
	Headers http.Header
	Trailer http.Header
	Body    string
	Err     error
}

func (p *Proxy) OpenAIChat(ctx context.Context, opts ChatOpts) ChatResult {
	if opts.Model == "" {
		opts.Model = "gpt-4o-mini"
	}
	if opts.Content == "" {
		opts.Content = "fuzz"
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 16
	}
	payload := map[string]any{
		"model": opts.Model,
		"messages": []map[string]string{
			{"role": "user", "content": opts.Content},
		},
		"max_tokens": opts.MaxTokens,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/openai/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResult{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+opts.APIKey)
	p.applyFakeHeaders(req, opts)
	return p.do(req)
}

func (p *Proxy) applyFakeHeaders(req *http.Request, opts ChatOpts) {
	if opts.ChaosRate != nil {
		req.Header.Set("X-LLM-Proxy-Fake-Chaos-Rate", strconv.FormatFloat(*opts.ChaosRate, 'f', -1, 64))
	}
	if opts.OutputTok > 0 {
		req.Header.Set("X-LLM-Proxy-Fake-Output-Tokens", strconv.Itoa(opts.OutputTok))
	}
	if opts.CachedTokens > 0 {
		req.Header.Set("X-LLM-Proxy-Fake-Cached-Tokens", strconv.Itoa(opts.CachedTokens))
	}
	if opts.FakeEchoPlaceholders {
		req.Header.Set("X-LLM-Proxy-Fake-Echo-Placeholders", "1")
	}
	if opts.FakeEchoPlaceholdersFormat != "" {
		req.Header.Set("X-LLM-Proxy-Fake-Echo-Placeholders-Format", opts.FakeEchoPlaceholdersFormat)
	}
	if opts.LatencyMS > 0 {
		req.Header.Set("X-LLM-Proxy-Fake-Latency-Ms", strconv.Itoa(opts.LatencyMS))
	}
	if opts.FakeOutcome != "" {
		req.Header.Set("X-LLM-Proxy-Fake-Outcome", opts.FakeOutcome)
	}
	if opts.TestMode != "" {
		req.Header.Set("X-LLM-Proxy-Test-Mode", opts.TestMode)
	}
}

func (p *Proxy) do(req *http.Request) ChatResult {
	resp, err := p.client.Do(req)
	if err != nil {
		if p.report != nil {
			if strings.Contains(err.Error(), "context deadline") || strings.Contains(err.Error(), "Client.Timeout") {
				p.report.RecordError("timeout")
			} else {
				p.report.RecordError("conn-error")
			}
		}
		return ChatResult{Err: err}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if p.report != nil {
		p.report.RecordStatus(resp.StatusCode)
		if strings.Contains(string(data), degradedSignal) {
			p.report.RecordDegraded()
		}
	}
	return ChatResult{Status: resp.StatusCode, Headers: resp.Header.Clone(), Trailer: resp.Trailer.Clone(), Body: string(data)}
}

func (p *Proxy) Burst(ctx context.Context, n int, workers int, fn func(context.Context) ChatResult) []ChatResult {
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int)
	results := make([]ChatResult, 0, n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				res := fn(ctx)
				mu.Lock()
				results = append(results, res)
				mu.Unlock()
			}
		}()
	}
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}

type keyHelper struct {
	admin *live.AdminClient
	keys  []string
}

func newKeyHelper(admin *live.AdminClient) *keyHelper {
	return &keyHelper{admin: admin}
}

func (k *keyHelper) create(ctx context.Context, desc string, rpm, tpm int) (string, error) {
	rec, err := k.admin.CreateKey(ctx, live.FuzzCreateKeyRequest(desc, rpm, tpm))
	if err != nil {
		return "", err
	}
	k.keys = append(k.keys, rec.Key)
	return rec.Key, nil
}

func (k *keyHelper) createWithCost(ctx context.Context, desc string, rpm, tpm int, dailyCostLimitCents int64) (string, error) {
	rec, err := k.admin.CreateKey(ctx, live.FuzzCreateKeyRequestWithCost(desc, rpm, tpm, dailyCostLimitCents))
	if err != nil {
		return "", err
	}
	k.keys = append(k.keys, rec.Key)
	return rec.Key, nil
}

func (k *keyHelper) createWithPII(ctx context.Context, desc string, rpm, tpm int, redactPII bool) (string, error) {
	rec, err := k.admin.CreateKey(ctx, live.FuzzCreateKeyRequestWithPII(desc, rpm, tpm, redactPII))
	if err != nil {
		return "", err
	}
	k.keys = append(k.keys, rec.Key)
	return rec.Key, nil
}

func (k *keyHelper) createWithDaily(ctx context.Context, desc string, rpm, tpm, rpd, tpd int) (string, error) {
	rec, err := k.admin.CreateKey(ctx, live.FuzzCreateKeyRequestWithDaily(desc, rpm, tpm, rpd, tpd))
	if err != nil {
		return "", err
	}
	k.keys = append(k.keys, rec.Key)
	return rec.Key, nil
}

func (k *keyHelper) cleanup(ctx context.Context) {
	for _, key := range k.keys {
		_ = k.admin.DeleteKey(ctx, key)
	}
}

func waitCostFlush(ctx context.Context, path string, before, want int) ([]CostRecord, error) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		after, err := CountLines(path)
		if err != nil {
			return nil, err
		}
		if after-before >= want {
			return ReadNewRecords(path, before)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("timeout waiting for %d new cost lines in %s", want, path)
}
