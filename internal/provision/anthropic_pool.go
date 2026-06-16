package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// AnthropicPool assigns pre-created Anthropic API keys from a Redis list.
type AnthropicPool struct {
	rdb      *redis.Client
	listKey  string
	adminKey string
	client   *http.Client
}

// NewAnthropicPool returns a pool provisioner. adminKey is optional for revoke.
func NewAnthropicPool(rdb *redis.Client, listKey, adminKey string) *AnthropicPool {
	if listKey == "" {
		listKey = "llm:provision:anthropic:available"
	}
	return &AnthropicPool{
		rdb:      rdb,
		listKey:  listKey,
		adminKey: adminKey,
		client:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *AnthropicPool) Provision(ctx context.Context, name string) (Result, error) {
	_ = name
	key, err := p.rdb.LPop(ctx, p.listKey).Result()
	if err == redis.Nil || key == "" {
		return Result{}, fmt.Errorf("%w — refill via llm-proxy-keys pool add", errEmptyPool)
	}
	if err != nil {
		return Result{}, err
	}
	return Result{
		ActualKey:    key,
		UpstreamKind: UpstreamKindAnthropicPooled,
	}, nil
}

func (p *AnthropicPool) Revoke(ctx context.Context, upstreamID, upstreamKind string) error {
	if upstreamKind != UpstreamKindAnthropicPooled || p.adminKey == "" || upstreamID == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"status": "archived"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.anthropic.com/v1/organizations/api_keys/"+upstreamID,
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", p.adminKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("anthropic revoke: status %d: %s", resp.StatusCode, truncate(raw, 300))
	}
	return nil
}

func (p *AnthropicPool) PoolStatus(ctx context.Context) (int, bool) {
	n, err := p.rdb.LLen(ctx, p.listKey).Result()
	if err != nil {
		return 0, true
	}
	return int(n), true
}

// PoolAdd pushes a key onto the Anthropic pool (ops / CLI).
func PoolAdd(ctx context.Context, rdb *redis.Client, listKey, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("empty key")
	}
	if listKey == "" {
		listKey = "llm:provision:anthropic:available"
	}
	return rdb.RPush(ctx, listKey, key).Err()
}

// PoolLen returns the number of keys available in the pool.
func PoolLen(ctx context.Context, rdb *redis.Client, listKey string) (int64, error) {
	if listKey == "" {
		listKey = "llm:provision:anthropic:available"
	}
	return rdb.LLen(ctx, listKey).Result()
}
