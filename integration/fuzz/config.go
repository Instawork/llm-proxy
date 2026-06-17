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
		"latency-timeout",
		"circuit-transient-retry",
		"circuit-random-trip",
		"circuit-recovery",
		"circuit-mixed",
	}
}

func DefaultSmokeScenarios() []string {
	return []string{"ratelimit-key-rpm", "cost-jsonl-count", "circuit-random-trip"}
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
