// Command endpoint-coverage aggregates 14 days of llm.instawork.com ELB
// request paths from Datadog and classifies each provider path with the
// same registry the proxy bills from (providers.ClassifyEndpoint), so the
// llm-price-update skill's step 4b is one command instead of a hand-rolled
// query plus a re-implementation of the classifier.
//
//	DD_API_KEY=... DD_APPLICATION_KEY=... go run ./cmd/endpoint-coverage/
//	go run ./cmd/endpoint-coverage/ -input paths.tsv   # offline: "<count>\t<path>" per line
//
// Output is a table of (class, endpoint template, calls); "unknown" rows are
// forwarded unbilled and are the findings step 4b is looking for.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/circuit"
	"github.com/Instawork/llm-proxy/internal/providers"
)

type pathCount struct {
	Path  string
	Count int64
}

type row struct {
	Class    string
	Template string
	Count    int64
}

func main() {
	days := flag.Int("days", 14, "Lookback window in days")
	host := flag.String("host", "llm.instawork.com", "ELB host to aggregate")
	site := flag.String("site", "api.datadoghq.com", "Datadog API host")
	input := flag.String("input", "", "Read '<count>\\t<path>' lines from this file instead of Datadog ('-' for stdin)")
	format := flag.String("format", "table", "Output format: table or markdown")
	minCalls := flag.Int64("min-calls", 3, "Hide (class, template) buckets with fewer calls; one-off scanner probes dominate below this")
	flag.Parse()

	var counts []pathCount
	var err error
	if *input != "" {
		counts, err = readTSV(*input)
	} else {
		counts, err = fetchDatadog(*site, *host, *days)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	rows, noise := summarize(counts)
	hidden := 0
	shown := rows[:0]
	for _, r := range rows {
		if r.Count < *minCalls {
			hidden++
			continue
		}
		shown = append(shown, r)
	}
	if *format == "markdown" {
		printMarkdown(os.Stdout, shown)
	} else {
		printTable(os.Stdout, shown)
	}
	fmt.Fprintf(os.Stdout, "\n%d provider path(s) skipped as scanner noise; %d bucket(s) below -min-calls=%d hidden\n", noise, hidden, *minCalls)
}

// isProviderPath mirrors middleware.isProviderRoute plus the /meta/{name}/
// prefix that attributes a request to a named caller.
func isProviderPath(path string) bool {
	if strings.HasPrefix(path, "/meta/") {
		rest := strings.TrimPrefix(path, "/meta/")
		if idx := strings.Index(rest, "/"); idx > 0 {
			path = rest[idx:]
		}
	}
	switch circuit.ProviderFromPath(path) {
	case "openai", "anthropic", "gemini", "bedrock", "bedrock-mantle":
		return true
	}
	return false
}

// isNoise drops the obvious scanner traffic (percent-encoded segments, path
// traversal, embedded schemes) that the ELB facet is full of.
func isNoise(path string) bool {
	return len(path) > 200 ||
		strings.Contains(path, "%") ||
		strings.Contains(path, "..") ||
		strings.Contains(path, "//") ||
		strings.Contains(path, "://") ||
		strings.Contains(path, "@") ||
		strings.Contains(path, ";") ||
		strings.Contains(path, "\\")
}

// summarize folds raw paths into (class, template) buckets, unknown first.
func summarize(counts []pathCount) ([]row, int) {
	agg := map[[2]string]int64{}
	noise := 0
	for _, pc := range counts {
		if !isProviderPath(pc.Path) {
			continue
		}
		if isNoise(pc.Path) {
			noise++
			continue
		}
		key := [2]string{
			providers.ClassifyEndpoint(pc.Path).String(),
			providers.EndpointTemplate(pc.Path),
		}
		agg[key] += pc.Count
	}
	rows := make([]row, 0, len(agg))
	for k, c := range agg {
		rows = append(rows, row{Class: k[0], Template: k[1], Count: c})
	}
	classOrder := map[string]int{"unknown": 0, "passthrough": 1, "metered": 2}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Class != rows[j].Class {
			return classOrder[rows[i].Class] < classOrder[rows[j].Class]
		}
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Template < rows[j].Template
	})
	return rows, noise
}

func printTable(w io.Writer, rows []row) {
	fmt.Fprintf(w, "%-12s %8s  %s\n", "CLASS", "CALLS", "ENDPOINT TEMPLATE")
	for _, r := range rows {
		fmt.Fprintf(w, "%-12s %8d  %s\n", r.Class, r.Count, r.Template)
	}
}

func printMarkdown(w io.Writer, rows []row) {
	fmt.Fprintln(w, "| Class | Calls | Endpoint template |")
	fmt.Fprintln(w, "|---|---:|---|")
	for _, r := range rows {
		fmt.Fprintf(w, "| %s | %d | `%s` |\n", r.Class, r.Count, r.Template)
	}
}

func readTSV(name string) ([]pathCount, error) {
	var f io.Reader = os.Stdin
	if name != "-" {
		fh, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		defer fh.Close()
		f = fh
	}
	var out []pathCount
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed line (want '<count>\\t<path>'): %q", line)
		}
		n, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad count in %q: %w", line, err)
		}
		out = append(out, pathCount{Path: parts[1], Count: n})
	}
	return out, sc.Err()
}

// fetchDatadog runs the logs analytics aggregate that step 4b documents.
// Health, admin and root paths are excluded server-side so the 1000-bucket
// cap is spent on provider traffic.
func fetchDatadog(site, host string, days int) ([]pathCount, error) {
	apiKey, appKey := os.Getenv("DD_API_KEY"), os.Getenv("DD_APPLICATION_KEY")
	if apiKey == "" || appKey == "" {
		return nil, fmt.Errorf("DD_API_KEY and DD_APPLICATION_KEY must be set (or pass -input)")
	}
	query := fmt.Sprintf(`service:elb "%s" -@http.url_details.path:(\/health OR \/admin* OR \/ OR \/lander)`, host)
	body := map[string]any{
		"filter": map[string]any{
			"query": query,
			"from":  fmt.Sprintf("now-%dd", days),
			"to":    "now",
		},
		"compute": []map[string]any{{"aggregation": "count", "type": "total"}},
		"group_by": []map[string]any{{
			"facet": "@http.url_details.path",
			"limit": 1000,
			"sort":  map[string]any{"type": "measure", "aggregation": "count", "order": "desc"},
		}},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, "https://"+site+"/api/v2/logs/analytics/aggregate", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("DD-API-KEY", apiKey)
	req.Header.Set("DD-APPLICATION-KEY", appKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("datadog returned HTTP %d", resp.StatusCode)
	}

	var parsed struct {
		Data struct {
			Buckets []struct {
				By       map[string]string `json:"by"`
				Computes map[string]any    `json:"computes"`
			} `json:"buckets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decoding datadog response: %w", err)
	}
	out := make([]pathCount, 0, len(parsed.Data.Buckets))
	for _, b := range parsed.Data.Buckets {
		path := b.By["@http.url_details.path"]
		var n int64
		if v, ok := b.Computes["c0"].(float64); ok {
			n = int64(v)
		}
		out = append(out, pathCount{Path: path, Count: n})
	}
	return out, nil
}
