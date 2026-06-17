package fuzz

import (
	"strings"
	"time"
)

type Config struct {
	BaseURL       string
	Scenario      string
	Workers       int
	Requests      int
	Timeout       time.Duration
	Seed          int64
	ChaosRate     float64
	CostFile      string
	ResetCostFile bool
	ReportJSON    string
	Verbose       bool
}

func AllScenarios() []string {
	return []string{
		"ratelimit-key-rpm",
		"ratelimit-key-tpm",
		"ratelimit-race",
		"ratelimit-atomicity-stress",
		"ratelimit-reconcile",
		"ratelimit-cancel-5xx",
		"cost-jsonl-count",
		"cost-token-math",
		"cost-fuzzy-model",
		"cost-unknown-model",
		"cost-no-charge-429",
		"cost-no-charge-degraded",
		"cost-concurrent-async",
		"cost-admin-stats",
		"cost-limit-zero-unlimited",
		"cost-limit-blocks-second",
		"cost-limit-allows-under",
		"cost-limit-no-charge-blocked",
		"cost-limit-isolated-keys",
		"cost-limit-admin-by-key",
		"cost-limit-update-raises",
		"cost-limit-update-removes",
		"cost-limit-concurrent",
		"cost-limit-atomicity-stress",
		"cost-limit-create-persists",
		"latency-timeout",
		"circuit-transient-retry",
		"circuit-random-trip",
		"circuit-recovery",
		"circuit-mixed",
		"circuit-per-model-isolation",
		"circuit-half-open-recover",
		"circuit-half-open-reopen",
		"cost-cache-token-no-inflation",
		"ratelimit-key-rpd",
		"pii-presidio-redaction",
		"pii-wire-restore-email",
		"pii-wire-seal-ssn",
		"ratelimit-tpm-atomicity-stress",
		"ratelimit-daily-atomicity-stress",
		"circuit-half-open-probe-stress",
		"cost-rollup-aggregation",
		"cost-failed-release-stress",
		"pii-concurrent-no-bleed",
	}
}

// StressScenarios are the high-concurrency atomicity/race stress tests that
// hammer the Redis-backed reservation, counter, probe-slot, and rollup paths
// plus PII cross-request isolation. The PII case requires the Presidio sidecar
// (make test-pii-up) and pii_redact enabled in configs/fuzz.yml.
func StressScenarios() []string {
	return []string{
		"cost-limit-atomicity-stress",
		"ratelimit-atomicity-stress",
		"ratelimit-tpm-atomicity-stress",
		"ratelimit-daily-atomicity-stress",
		"circuit-half-open-probe-stress",
		"cost-rollup-aggregation",
		"cost-failed-release-stress",
		"pii-concurrent-no-bleed",
	}
}

// ProxyIssuesScenarios covers failure modes commonly reported against LLM
// proxies in the wild: prompt-cache token double-counting, per-day rate
// limits with client-backoff header hygiene, and Presidio wire redaction
// through the real proxy API (fake upstream only).
func ProxyIssuesScenarios() []string {
	return []string{
		"cost-cache-token-no-inflation",
		"ratelimit-key-rpd",
		"pii-presidio-redaction",
		"pii-wire-restore-email",
		"pii-wire-seal-ssn",
	}
}

func CircuitScenarios() []string {
	return []string{
		"circuit-transient-retry",
		"circuit-random-trip",
		"circuit-recovery",
		"circuit-mixed",
		"circuit-per-model-isolation",
		"circuit-half-open-recover",
		"circuit-half-open-reopen",
	}
}

func CostLimitScenarios() []string {
	return []string{
		"cost-limit-zero-unlimited",
		"cost-limit-blocks-second",
		"cost-limit-allows-under",
		"cost-limit-no-charge-blocked",
		"cost-limit-isolated-keys",
		"cost-limit-admin-by-key",
		"cost-limit-update-raises",
		"cost-limit-update-removes",
		"cost-limit-concurrent",
		"cost-limit-atomicity-stress",
		"cost-limit-create-persists",
	}
}

func DefaultSmokeScenarios() []string {
	return []string{"ratelimit-key-rpm", "cost-jsonl-count", "cost-limit-blocks-second", "circuit-random-trip"}
}

func ChaosScenarios() []string {
	return []string{"circuit-random-trip", "circuit-mixed", "latency-timeout"}
}

func MatrixScenarios() []string {
	return []string{"ratelimit-key-rpm", "circuit-random-trip"}
}

func ParseScenarioList(raw string) []string {
	if raw == "" || raw == "all" {
		return AllScenarios()
	}
	if raw == "cost-limit" {
		return CostLimitScenarios()
	}
	if raw == "circuit" {
		return CircuitScenarios()
	}
	if raw == "proxy-issues" {
		return ProxyIssuesScenarios()
	}
	if raw == "stress" {
		return StressScenarios()
	}
	if raw == "smoke" {
		return DefaultSmokeScenarios()
	}
	if raw == "chaos" {
		return ChaosScenarios()
	}
	if raw == "matrix" {
		return MatrixScenarios()
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
