package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Instawork/llm-proxy-live/live"
)

func main() {
	baseURL := flag.String("base-url", "http://localhost:9002", "Running llm-proxy base URL")
	presidioURL := flag.String("presidio-url", "http://localhost:5004", "Presidio analyzer URL (host port)")
	costFile := flag.String("cost-file", "", "Path to cost-tracking.jsonl (default ../logs/cost-tracking.jsonl)")
	snippetsDir := flag.String("snippets-dir", "snippets", "Path to share-box snippet tests (default snippets/)")
	suites := flag.String("suite", "all", "Comma-separated suites: health,admin,openai,anthropic,gemini,ratelimit,cost,pii,presidio,snippets,all")
	timeoutSec := flag.Int("timeout", 90, "HTTP timeout per request in seconds")
	verbose := flag.Bool("verbose", true, "Log progress to stderr while tests run")
	flag.Parse()

	cfg := live.LoadConfig(*baseURL, *presidioURL, *costFile, *snippetsDir, *suites, *timeoutSec, *verbose)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner, err := live.NewRunner(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		os.Exit(2)
	}

	fmt.Fprintf(os.Stdout, "llm-proxy live checks → %s\n\n", cfg.BaseURL)
	results := runner.Run(ctx)
	code := live.PrintResults(results, cfg.Verbose)
	os.Exit(code)
}
