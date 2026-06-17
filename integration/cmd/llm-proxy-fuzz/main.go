package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Instawork/llm-proxy/integration/fuzz"
)

func main() {
	base := flag.String("base", envOr("PROXY_BASE_URL", "http://localhost:9002"), "proxy base URL")
	scenario := flag.String("scenario", "smoke", "scenario name, comma list, smoke, all, chaos, matrix, cost-limit, circuit, or proxy-issues")
	workers := flag.Int("workers", 8, "concurrent workers")
	requests := flag.Int("requests", 4, "requests per worker for burst scenarios")
	timeoutSec := flag.Int("timeout", 60, "HTTP timeout seconds")
	seed := flag.Int64("seed", 0, "chaos seed (logged for repro)")
	chaosRate := flag.Float64("chaos-rate", 0, "per-request X-LLM-Proxy-Fake-Chaos-Rate override")
	costFile := flag.String("cost-file", "../logs/cost-tracking.jsonl", "cost tracking jsonl path (relative to integration/ when run via make fuzz)")
	resetCost := flag.Bool("reset-cost-file", false, "truncate cost file before run")
	reportJSON := flag.String("report-json", "", "write machine-readable report JSON")
	flag.Parse()

	cfg := fuzz.Config{
		BaseURL:       *base,
		Scenario:      *scenario,
		Workers:       *workers,
		Requests:      *requests,
		Timeout:       time.Duration(*timeoutSec) * time.Second,
		Seed:          *seed,
		ChaosRate:     *chaosRate,
		CostFile:      *costFile,
		ResetCostFile: *resetCost,
		ReportJSON:    *reportJSON,
	}

	runner, err := fuzz.NewRunner(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fuzz: %v\n", err)
		os.Exit(1)
	}
	report, err := runner.Run(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "fuzz: %v\n", err)
		os.Exit(1)
	}
	report.Print()
	if err := report.WriteJSON(cfg.ReportJSON); err != nil {
		fmt.Fprintf(os.Stderr, "report json: %v\n", err)
		os.Exit(1)
	}
	if report.Failed() {
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
