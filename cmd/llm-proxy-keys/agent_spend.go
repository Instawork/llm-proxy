package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/cost"
)

const (
	defaultAgentSpendPrefix    = "finch-agent-"
	defaultAgentSoftLimitCents = 10000
	defaultAgentHardLimitCents = 20000

	// agentSeedMarkTTL outlives the 35-day monthly counter TTL so a seed
	// cannot be replayed into a month whose counter still exists.
	agentSeedMarkTTL = 62 * 24 * time.Hour
)

// agentMappingHeader is the header row of the Finch export.
var agentMappingHeader = []string{"id", "slug", "label_as_sent"}

// costSummer sums a user label's cost rows; *cost.DynamoDBTransport implements it.
type costSummer interface {
	SumUserCost(ctx context.Context, userID string, start, end time.Time) (cost.UserCostTotal, error)
}

// agentMapping is one row of the Finch export.
type agentMapping struct {
	ID          string
	Slug        string
	LabelAsSent string
}

// seedSource is one old label as the proxy recorded it (URL-decoded), with
// every agent ID the mapping assigns to it.
type seedSource struct {
	Label      string
	SentLabels []string
	IDs        []string
	slugByID   map[string]string
}

// candidates names every agent the source maps to, as "label (slug)".
func (s seedSource) candidates(prefix string) string {
	out := make([]string, len(s.IDs))
	for i, id := range s.IDs {
		out[i] = fmt.Sprintf("%s (%s)", prefix+id, s.slugByID[id])
	}
	return strings.Join(out, ", ")
}

func (s seedSource) ambiguous() bool { return len(s.IDs) > 1 }

type seedOutcome string

const (
	seedWouldWrite  seedOutcome = "would seed"
	seedWritten     seedOutcome = "seeded"
	seedAlreadyDone seedOutcome = "already seeded, skipped"
	seedNoSpend     seedOutcome = "no spend"
	seedFailed      seedOutcome = "FAILED"
	seedAmbiguous   seedOutcome = "ambiguous, not seeded"
)

type seedRow struct {
	Source  seedSource
	Total   cost.UserCostTotal
	Outcome seedOutcome
	Err     error
}

func (r seedRow) result() string {
	if r.Err != nil {
		return fmt.Sprintf("%s: %v", r.Outcome, r.Err)
	}
	return string(r.Outcome)
}

type seedOptions struct {
	Month  string
	Prefix string
	DryRun bool
	Now    time.Time
}

type seedResult struct {
	Rows   []seedRow
	Failed int
}

// runAgentSpendCommand implements `agent-spend seed|report`.
func runAgentSpendCommand(args []string) {
	if len(args) == 0 || (args[0] != "seed" && args[0] != "report") {
		printAgentSpendUsage()
		os.Exit(1)
	}
	action := args[0]

	fs := flag.NewFlagSet("agent-spend "+action, flag.ExitOnError)
	configDir := fs.String("config-dir", "configs", "Path to configuration directory")
	environment := fs.String("env", "dev", "Environment (dev, staging, production)")
	month := fs.String("month", "", "UTC calendar month, YYYY-MM (required)")
	prefix := fs.String("prefix", defaultAgentSpendPrefix, "Governed agent label prefix")
	mappingPath := fs.String("mapping", "", "seed: Finch export CSV with columns id,slug,label_as_sent")
	dryRun := fs.Bool("dry-run", false, "seed: print what would be written without writing")
	softCents := fs.Int64("soft-cents", defaultAgentSoftLimitCents, "report: soft threshold in cents")
	hardCents := fs.Int64("hard-cents", defaultAgentHardLimitCents, "report: hard threshold in cents")
	if err := fs.Parse(args[1:]); err != nil {
		os.Exit(1)
	}

	now := time.Now().UTC()
	if err := validateAgentSpendMonth(*month, now); err != nil {
		fatalf("agent-spend %s: %v", action, err)
	}
	if err := validateAgentSpendPrefix(*prefix); err != nil {
		fatalf("agent-spend %s: %v", action, err)
	}

	yamlConfig, err := loadConfig(*configDir, *environment)
	if err != nil {
		fatalf("load config: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := context.Background()

	switch action {
	case "report":
		if *softCents <= 0 || *hardCents < *softCents {
			fatalf("agent-spend report: need 0 < -soft-cents <= -hard-cents")
		}
		rollup := openAgentSpendRollup(yamlConfig, logger)
		defer rollup.Close() //nolint:errcheck
		if err := executeAgentSpendReport(ctx, os.Stdout, rollup, *month, *prefix, *softCents, *hardCents); err != nil {
			fatalf("agent-spend report: %v", err)
		}
	case "seed":
		if strings.TrimSpace(*mappingPath) == "" {
			fatalf("agent-spend seed requires -mapping")
		}
		f, err := os.Open(*mappingPath)
		if err != nil {
			fatalf("agent-spend seed: %v", err)
		}
		mappings, err := parseAgentMapping(f)
		_ = f.Close()
		if err != nil {
			fatalf("agent-spend seed: %v", err)
		}
		if agentSeedNeedsProdGuard(*environment, *dryRun) {
			if err := requireProdAcknowledgement(); err != nil {
				fatalf("%v", err)
			}
		}
		costs := openAgentSpendCostTable(yamlConfig, logger)
		rollup := openAgentSpendRollup(yamlConfig, logger)
		res, err := executeAgentSpendSeed(ctx, os.Stdout, costs, rollup, mappings, seedOptions{
			Month: *month, Prefix: *prefix, DryRun: *dryRun, Now: now,
		})
		_ = rollup.Close()
		if err != nil {
			fatalf("agent-spend seed: %v", err)
		}
		if res.Failed > 0 {
			os.Exit(1)
		}
	}
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// agentSeedNeedsProdGuard reports whether seed must pass the production
// guard: only writes against production do.
func agentSeedNeedsProdGuard(environment string, dryRun bool) bool {
	return environment == "production" && !dryRun
}

// openAgentSpendRollup opens the shared rollup Redis. The memory backend is
// refused because it lives only inside this process.
func openAgentSpendRollup(cfg *config.YAMLConfig, logger *slog.Logger) *adminrollup.Store {
	rc := adminrollup.ConfigFromYAML(cfg.Features.AdminDashboard)
	rc.Logger = logger
	if !rc.Enabled {
		fatalf("admin_dashboard.rollups is disabled in this environment; the monthly counter lives there")
	}
	store, err := adminrollup.NewStore(rc)
	if err != nil {
		fatalf("open rollup store: %v", err)
	}
	if store.Backend() != adminrollup.BackendRedis {
		_ = store.Close()
		fatalf("admin_dashboard.rollups uses the %q backend; seed and report need the shared Redis", store.Backend())
	}
	return store
}

// openAgentSpendCostTable opens the configured DynamoDB cost table read-only;
// it never creates the table.
func openAgentSpendCostTable(cfg *config.YAMLConfig, logger *slog.Logger) *cost.DynamoDBTransport {
	for _, t := range cfg.GetAllTransports() {
		if t.Type != "dynamodb" || t.DynamoDB == nil {
			continue
		}
		tp, err := cost.NewDynamoDBTransport(cost.DynamoDBTransportConfig{
			TableName: t.DynamoDB.TableName,
			Region:    t.DynamoDB.Region,
			Logger:    logger,
		})
		if err != nil {
			fatalf("open cost table: %v", err)
		}
		return tp
	}
	fatalf("no dynamodb cost tracking transport is configured in this environment")
	return nil
}

// validateAgentSpendMonth requires a YYYY-MM month that is not in the future.
func validateAgentSpendMonth(month string, now time.Time) error {
	start, err := time.Parse("2006-01", month)
	if err != nil || start.Format("2006-01") != month {
		return fmt.Errorf("-month must be YYYY-MM, got %q", month)
	}
	if start.After(now) {
		return fmt.Errorf("-month %s is in the future", month)
	}
	return nil
}

func validateAgentSpendPrefix(prefix string) error {
	if prefix == "" || strings.ContainsAny(prefix, "|/ ") {
		return fmt.Errorf("-prefix must be non-empty and contain no '|', '/' or spaces")
	}
	return nil
}

// isGovernedAgentLabel matches coststats' rule for which labels get the
// monthly per-user counter.
func isGovernedAgentLabel(prefix, label string) bool {
	return len(label) > len(prefix) && strings.HasPrefix(label, prefix) && !strings.Contains(label, "|")
}

// parseAgentMapping reads the Finch export: a header row "id,slug,label_as_sent"
// ("label-as-sent" is also accepted) then one row per (agent, label). Exact
// duplicate rows are dropped; an ID with two different slugs is rejected as an
// inconsistent export.
func parseAgentMapping(r io.Reader) ([]agentMapping, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = len(agentMappingHeader)
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("mapping: read header: %w", err)
	}
	for i, want := range agentMappingHeader {
		got := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(header[i], "\ufeff")))
		if strings.ReplaceAll(got, "-", "_") != want {
			return nil, fmt.Errorf("mapping: header must be %q, got %q", strings.Join(agentMappingHeader, ","), strings.Join(header, ","))
		}
	}

	var out []agentMapping
	seen := map[agentMapping]bool{}
	slugByID := map[string]string{}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("mapping: %w", err)
		}
		line, _ := cr.FieldPos(0)
		m := agentMapping{
			ID:          strings.TrimSpace(rec[0]),
			Slug:        strings.TrimSpace(rec[1]),
			LabelAsSent: strings.TrimSpace(rec[2]),
		}
		n, err := strconv.ParseUint(m.ID, 10, 64)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != m.ID {
			return nil, fmt.Errorf("mapping line %d: id must be a positive integer, got %q", line, m.ID)
		}
		if m.LabelAsSent == "" {
			return nil, fmt.Errorf("mapping line %d: label_as_sent is empty", line)
		}
		if prev, ok := slugByID[m.ID]; ok && prev != m.Slug {
			return nil, fmt.Errorf("mapping line %d: id %s has two slugs (%q and %q)", line, m.ID, prev, m.Slug)
		}
		slugByID[m.ID] = m.Slug
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("mapping: no rows")
	}
	return out, nil
}

// buildSeedSources groups mapping rows by the label the proxy recorded. The
// proxy takes the label from the decoded request path, so label_as_sent is
// URL-unescaped first; two sent forms that decode alike are one source.
// Labels already under the governed prefix are rejected: their spend is
// counted live and seeding them would double it.
func buildSeedSources(mappings []agentMapping, prefix string) ([]seedSource, error) {
	byLabel := map[string]*seedSource{}
	slugByID := map[string]string{}
	for _, m := range mappings {
		slugByID[m.ID] = m.Slug
	}
	for _, m := range mappings {
		label, err := url.PathUnescape(m.LabelAsSent)
		if err != nil {
			return nil, fmt.Errorf("mapping: label_as_sent %q is not valid URL encoding: %w", m.LabelAsSent, err)
		}
		if strings.HasPrefix(label, prefix) {
			return nil, fmt.Errorf("mapping: label %q already has the governed prefix %q", label, prefix)
		}
		src := byLabel[label]
		if src == nil {
			src = &seedSource{Label: label, slugByID: slugByID}
			byLabel[label] = src
		}
		src.SentLabels = appendUnique(src.SentLabels, m.LabelAsSent)
		src.IDs = appendUnique(src.IDs, m.ID)
	}
	out := make([]seedSource, 0, len(byLabel))
	for _, src := range byLabel {
		sort.Strings(src.SentLabels)
		sort.Slice(src.IDs, func(i, j int) bool { return idLess(src.IDs[i], src.IDs[j]) })
		out = append(out, *src)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func agentSeedMarkName(month, sourceLabel string) string {
	return "agent_spend_seed:" + month + ":" + sourceLabel
}

// executeAgentSpendSeed sums each old label's cost rows for the month and adds
// them to the monthly counter under prefix+id. Each old label is seeded at
// most once per month, guarded by a TryMarkOnce marker; labels that map to
// more than one ID are reported and never written.
func executeAgentSpendSeed(ctx context.Context, w io.Writer, costs costSummer, rollup *adminrollup.Store, mappings []agentMapping, opts seedOptions) (seedResult, error) {
	var res seedResult
	sources, err := buildSeedSources(mappings, opts.Prefix)
	if err != nil {
		return res, err
	}
	start, err := time.Parse("2006-01", opts.Month)
	if err != nil {
		return res, err
	}
	end := start.AddDate(0, 1, 0)

	for _, src := range sources {
		total, err := costs.SumUserCost(ctx, src.Label, start, end)
		if err != nil {
			return res, fmt.Errorf("sum cost rows for label %q: %w", src.Label, err)
		}
		row := seedRow{Source: src, Total: total}
		switch {
		case src.ambiguous():
			row.Outcome = seedAmbiguous
		case total.TotalUSD <= 0:
			row.Outcome = seedNoSpend
		default:
			row.Outcome, row.Err = seedOne(ctx, rollup, opts, src, total.TotalUSD)
		}
		if row.Outcome == seedFailed {
			res.Failed++
		}
		res.Rows = append(res.Rows, row)
	}
	printSeedResult(w, res, opts, sources)
	return res, nil
}

// seedOne claims the label's marker before writing and releases it if the
// write fails, so a crash between the two under-seeds rather than doubles.
func seedOne(ctx context.Context, rollup *adminrollup.Store, opts seedOptions, src seedSource, spendUSD float64) (seedOutcome, error) {
	mark := agentSeedMarkName(opts.Month, src.Label)
	target := opts.Prefix + src.IDs[0]
	if opts.DryRun {
		marked, err := rollup.IsMarked(ctx, mark)
		if err != nil {
			return seedFailed, fmt.Errorf("check marker: %w", err)
		}
		if marked {
			return seedAlreadyDone, nil
		}
		return seedWouldWrite, nil
	}
	claimed, err := rollup.TryMarkOnce(ctx, mark, agentSeedMarkTTL)
	if err != nil {
		return seedFailed, fmt.Errorf("claim marker: %w", err)
	}
	if !claimed {
		return seedAlreadyDone, nil
	}
	if err := rollup.ApplyMonthlyUserSpend(ctx, adminrollup.MetricCost, opts.Month, target, spendUSD); err != nil {
		if relErr := rollup.ReleaseMark(ctx, mark); relErr != nil {
			return seedFailed, fmt.Errorf("write counter: %w; release marker: %v", err, relErr)
		}
		return seedFailed, fmt.Errorf("write counter: %w", err)
	}
	return seedWritten, nil
}

func printSeedResult(w io.Writer, res seedResult, opts seedOptions, sources []seedSource) {
	mode := "APPLY"
	if opts.DryRun {
		mode = "DRY RUN"
	}
	fmt.Fprintf(w, "agent-spend seed (%s) month=%s prefix=%s\n", mode, opts.Month, opts.Prefix)
	fmt.Fprintf(w, "Cost rows read through UserProviderIndex at %s.\n\n", opts.Now.Format(time.RFC3339))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "OLD LABEL\tTARGET\tROWS\tSPEND\tRESULT")
	var seeded, wouldSeed, ambiguousUSD float64
	var nAmbiguous, nAlready, nFailed int
	for _, r := range res.Rows {
		if r.Source.ambiguous() {
			ambiguousUSD += r.Total.TotalUSD
			nAmbiguous++
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", displayLabel(r.Source.Label), opts.Prefix+r.Source.IDs[0], r.Total.Rows, formatUSD(r.Total.TotalUSD), r.result())
		switch r.Outcome {
		case seedWritten:
			seeded += r.Total.TotalUSD
		case seedWouldWrite:
			wouldSeed += r.Total.TotalUSD
		case seedAlreadyDone:
			nAlready++
		case seedFailed:
			nFailed++
		}
	}
	_ = tw.Flush()

	if nAmbiguous > 0 {
		fmt.Fprintf(w, "\nAMBIGUOUS, not seeded: each label below maps to more than one agent ID, so its spend cannot be attributed.\n")
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "OLD LABEL\tCANDIDATES\tROWS\tSPEND")
		for _, r := range res.Rows {
			if !r.Source.ambiguous() {
				continue
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", displayLabel(r.Source.Label), r.Source.candidates(opts.Prefix), r.Total.Rows, formatUSD(r.Total.TotalUSD))
		}
		_ = tw.Flush()
	}

	merged, unseeded := seedGroupings(sources, opts.Prefix)
	if len(merged) > 0 {
		fmt.Fprintf(w, "\nMERGED: these agents have several old labels; each label's total is added to the agent.\n")
		for _, line := range merged {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	if len(unseeded) > 0 {
		fmt.Fprintf(w, "\nUNSEEDED AGENTS: every old label of these agents is ambiguous: %s\n", strings.Join(unseeded, ", "))
	}

	fmt.Fprintf(w, "\nSummary: %d old label(s); ", len(res.Rows))
	if opts.DryRun {
		fmt.Fprintf(w, "would seed %s; ", formatUSD(wouldSeed))
	} else {
		fmt.Fprintf(w, "seeded %s; ", formatUSD(seeded))
	}
	fmt.Fprintf(w, "%d already seeded; %d ambiguous (%s not seeded); %d failed.\n", nAlready, nAmbiguous, formatUSD(ambiguousUSD), nFailed)
	if opts.DryRun {
		fmt.Fprintf(w, "Nothing was written. Re-run without -dry-run to seed.\n")
	}
}

// seedGroupings lists agents that receive several old labels, and agents whose
// every old label is ambiguous and so receive nothing.
func seedGroupings(sources []seedSource, prefix string) (merged, unseeded []string) {
	labelsByID := map[string][]string{}
	allIDs := map[string]bool{}
	for _, src := range sources {
		for _, id := range src.IDs {
			allIDs[id] = true
		}
		if !src.ambiguous() {
			labelsByID[src.IDs[0]] = append(labelsByID[src.IDs[0]], displayLabel(src.Label))
		}
	}
	ids := make([]string, 0, len(allIDs))
	for id := range allIDs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return idLess(ids[i], ids[j]) })
	for _, id := range ids {
		labels := labelsByID[id]
		switch {
		case len(labels) == 0:
			unseeded = append(unseeded, prefix+id)
		case len(labels) > 1:
			merged = append(merged, fmt.Sprintf("%s <- %s", prefix+id, strings.Join(labels, ", ")))
		}
	}
	return merged, unseeded
}

type agentSpendRow struct {
	Label    string
	SpendUSD float64
}

// executeAgentSpendReport lists every governed label's recorded month-to-date
// spend, highest first, flagging labels at or over each threshold. In-flight
// reservations are not included.
func executeAgentSpendReport(ctx context.Context, w io.Writer, rollup *adminrollup.Store, month, prefix string, softCents, hardCents int64) error {
	all, err := rollup.UserMonthlySpendAll(ctx, adminrollup.MetricCost, month)
	if err != nil {
		return fmt.Errorf("read monthly counter: %w", err)
	}
	var rows []agentSpendRow
	for label, spend := range all {
		if isGovernedAgentLabel(prefix, label) {
			rows = append(rows, agentSpendRow{Label: label, SpendUSD: spend})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].SpendUSD != rows[j].SpendUSD {
			return rows[i].SpendUSD > rows[j].SpendUSD
		}
		return rows[i].Label < rows[j].Label
	})

	soft, hard := float64(softCents)/100, float64(hardCents)/100
	fmt.Fprintf(w, "agent-spend report month=%s prefix=%s soft=%s hard=%s\n\n", month, prefix, formatUSD(soft), formatUSD(hard))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "LABEL\tSPEND\tSTATUS")
	var nSoft, nHard int
	for _, r := range rows {
		status := "ok"
		switch {
		case r.SpendUSD >= hard:
			status = "OVER HARD"
			nHard++
		case r.SpendUSD >= soft:
			status = "OVER SOFT"
			nSoft++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Label, formatUSD(r.SpendUSD), status)
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "\nSummary: %d agent label(s); %d at or over hard; %d at or over soft but under hard.\n", len(rows), nHard, nSoft)
	return nil
}

// displayLabel quotes labels with surrounding whitespace, which Finch exports
// as a separate row from the stripped name and which would otherwise print
// identically.
func displayLabel(label string) string {
	if label != strings.TrimSpace(label) {
		return strconv.Quote(label)
	}
	return label
}

func formatUSD(v float64) string {
	return fmt.Sprintf("$%.2f", v)
}

func printAgentSpendUsage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  %s agent-spend seed   -month YYYY-MM -mapping finch-agents.csv [-dry-run] [-prefix finch-agent-] [-env=dev]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s agent-spend report -month YYYY-MM [-soft-cents 10000] [-hard-cents 20000] [-prefix finch-agent-] [-env=dev]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\nThe mapping CSV has the header id,slug,label_as_sent.\n")
}
