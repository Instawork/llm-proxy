package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSingleTransport_AllBranches(t *testing.T) {
	c := &YAMLConfig{}
	cases := []struct {
		name string
		t    TransportConfig
		ok   bool
	}{
		{"empty type", TransportConfig{}, false},
		{"file missing struct", TransportConfig{Type: "file"}, false},
		{"file missing path", TransportConfig{Type: "file", File: &FileTransportConfig{}}, false},
		{"file ok", TransportConfig{Type: "file", File: &FileTransportConfig{Path: "/tmp/x"}}, true},
		{"ddb missing struct", TransportConfig{Type: "dynamodb"}, false},
		{"ddb missing tablename", TransportConfig{Type: "dynamodb", DynamoDB: &DynamoDBTransportConfig{Region: "us-west-2"}}, false},
		{"ddb missing region", TransportConfig{Type: "dynamodb", DynamoDB: &DynamoDBTransportConfig{TableName: "t"}}, false},
		{"ddb ok", TransportConfig{Type: "dynamodb", DynamoDB: &DynamoDBTransportConfig{TableName: "t", Region: "us-west-2"}}, true},
		{"datadog missing struct", TransportConfig{Type: "datadog"}, false},
		{"datadog ok", TransportConfig{Type: "datadog", Datadog: &DatadogTransportConfig{}}, true},
		{"unsupported", TransportConfig{Type: "weird"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.validateSingleTransport(tc.t)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestValidate_DisabledFeatures_ShortCircuit(t *testing.T) {
	c := &YAMLConfig{Providers: map[string]ProviderConfig{}}
	require.NoError(t, c.Validate())

	c.Features.CostTracking.Enabled = true
	require.Error(t, c.Validate())
	c.Features.CostTracking.Enabled = false

	c.Features.RateLimiting.Enabled = true
	c.Features.RateLimiting.Backend = "invalid"
	require.Error(t, c.Validate())
	c.Features.RateLimiting = RateLimitingConfig{}

	c.Features.CircuitBreaker.Enabled = true
	c.Features.CircuitBreaker.Mode = "weird"
	require.Error(t, c.Validate())
}

func TestValidate_NoProvidersErrors(t *testing.T) {
	c := &YAMLConfig{}
	assert.Error(t, c.Validate())
}

func TestValidateRateLimiting_AllBranches(t *testing.T) {
	cases := []struct {
		name string
		rl   RateLimitingConfig
		ok   bool
	}{
		{"defaults", RateLimitingConfig{}, true},
		{"memory backend explicit", RateLimitingConfig{Backend: "memory"}, true},
		{"redis missing", RateLimitingConfig{Backend: "redis"}, false},
		{"redis with url", RateLimitingConfig{Backend: "redis", Redis: &RedisConfig{URL: "redis://h"}}, true},
		{"redis with addr", RateLimitingConfig{Backend: "redis", Redis: &RedisConfig{Address: "h:6379"}}, true},
		{"unsupported backend", RateLimitingConfig{Backend: "etcd"}, false},
		{"neg bytes per token", RateLimitingConfig{Estimation: EstimationConfig{BytesPerToken: -1}}, false},
		{"neg chars per token", RateLimitingConfig{Estimation: EstimationConfig{CharsPerToken: -1}}, false},
		{"max sample bytes too small", RateLimitingConfig{Estimation: EstimationConfig{MaxSampleBytes: -2}}, false},
		{"neg provider override", RateLimitingConfig{Estimation: EstimationConfig{ProviderCharsPerToken: map[string]int{"openai": -1}}}, false},
		{"valid provider override", RateLimitingConfig{Estimation: EstimationConfig{ProviderCharsPerToken: map[string]int{"openai": 5}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &YAMLConfig{Features: FeaturesConfig{RateLimiting: tc.rl}}
			err := c.validateRateLimitingConfig()
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestValidateCircuitBreaker_AllBranches(t *testing.T) {
	good := CircuitBreakerConfig{
		FailureThreshold: 5, WindowSeconds: 60, CooldownSeconds: 60,
	}
	cases := []struct {
		name string
		mut  func(*CircuitBreakerConfig)
		ok   bool
	}{
		{"defaults", func(c *CircuitBreakerConfig) {}, true},
		{"mode log", func(c *CircuitBreakerConfig) { c.Mode = "log" }, true},
		{"mode enforce", func(c *CircuitBreakerConfig) { c.Mode = "enforce" }, true},
		{"mode invalid", func(c *CircuitBreakerConfig) { c.Mode = "weird" }, false},
		{"backend memory", func(c *CircuitBreakerConfig) { c.Backend = "memory" }, true},
		{"backend invalid", func(c *CircuitBreakerConfig) { c.Backend = "weird" }, false},
		{"redis backend missing redis", func(c *CircuitBreakerConfig) { c.Backend = "redis" }, false},
		{"redis backend missing url+addr", func(c *CircuitBreakerConfig) { c.Backend = "redis"; c.Redis = &RedisConfig{} }, false},
		{"redis backend ok", func(c *CircuitBreakerConfig) { c.Backend = "redis"; c.Redis = &RedisConfig{URL: "redis://h"} }, true},
		{"failure threshold 0", func(c *CircuitBreakerConfig) { c.FailureThreshold = 0 }, false},
		{"failure threshold neg", func(c *CircuitBreakerConfig) { c.FailureThreshold = -1 }, false},
		{"window seconds 0", func(c *CircuitBreakerConfig) { c.WindowSeconds = 0 }, false},
		{"window seconds neg", func(c *CircuitBreakerConfig) { c.WindowSeconds = -1 }, false},
		{"cooldown 0", func(c *CircuitBreakerConfig) { c.CooldownSeconds = 0 }, false},
		{"cooldown neg", func(c *CircuitBreakerConfig) { c.CooldownSeconds = -1 }, false},
		{"max transient retries neg", func(c *CircuitBreakerConfig) { c.MaxTransientRetries = -1 }, false},
		{"max rate limit retries neg", func(c *CircuitBreakerConfig) { c.MaxRateLimitRetries = -1 }, false},
		{"global rate limit escalation neg", func(c *CircuitBreakerConfig) { c.GlobalRateLimitEscalationWindow = -1 }, false},
		{"retry contribution invalid", func(c *CircuitBreakerConfig) { c.RetryContributionMode = "weird" }, false},
		{"retry contribution log", func(c *CircuitBreakerConfig) { c.RetryContributionMode = "log" }, true},
		{"per-provider rollup neg", func(c *CircuitBreakerConfig) { c.PerProviderRollupThreshold = -1 }, false},
		{"per-provider rollup window neg", func(c *CircuitBreakerConfig) { c.PerProviderRollupWindowSeconds = -1 }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cb := good
			tc.mut(&cb)
			c := &YAMLConfig{Features: FeaturesConfig{CircuitBreaker: cb}}
			err := c.validateCircuitBreakerConfig()
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
