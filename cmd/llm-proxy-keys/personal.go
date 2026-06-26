package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"

	"github.com/Instawork/llm-proxy/internal/apikeys"
)

const (
	oldPersonalMonthlyLimitCents = 1000
	newPersonalMonthlyLimitCents = 2000
)

func runPersonalCommand(args []string) {
	fs := flag.NewFlagSet("personal", flag.ExitOnError)
	configDir := fs.String("config-dir", "configs", "Path to configuration directory")
	environment := fs.String("env", "dev", "Environment (dev, staging, production)")
	fromCents := fs.Int64("from", oldPersonalMonthlyLimitCents, "Only update personal keys at this monthly limit (cents)")
	toCents := fs.Int64("to", newPersonalMonthlyLimitCents, "New monthly limit (cents)")
	apply := fs.Bool("apply", false, "Persist updates (default is dry-run)")

	if len(args) == 0 {
		printPersonalUsage()
		os.Exit(1)
	}
	subcmd := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		os.Exit(1)
	}

	switch subcmd {
	case "bump-limit":
		runPersonalBumpLimit(*configDir, *environment, *fromCents, *toCents, *apply)
	default:
		printPersonalUsage()
		os.Exit(1)
	}
}

func runPersonalBumpLimit(configDir, environment string, fromCents, toCents int64, apply bool) {
	if fromCents <= 0 {
		fmt.Fprintf(os.Stderr, "--from must be positive\n")
		os.Exit(1)
	}
	if toCents <= 0 {
		fmt.Fprintf(os.Stderr, "--to must be positive\n")
		os.Exit(1)
	}
	if fromCents == toCents {
		fmt.Fprintf(os.Stderr, "--from and --to must differ\n")
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	yamlConfig, err := loadConfig(configDir, environment)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	if !yamlConfig.Features.APIKeyManagement.Enabled {
		fmt.Fprintf(os.Stderr, "api_key_management is disabled in %s config\n", environment)
		os.Exit(1)
	}

	if environment == "production" {
		if err := requireProdAcknowledgement(); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}

	keyPrefixBase := os.Getenv("LLM_PROXY_API_KEY_PREFIX")
	if keyPrefixBase == "" {
		keyPrefixBase = yamlConfig.Features.APIKeyManagement.KeyPrefix
	}
	apikeys.SetKeyPrefixBase(keyPrefixBase)

	store, err := apikeys.NewStore(apikeys.StoreConfig{
		TableName:       yamlConfig.Features.APIKeyManagement.TableName,
		Region:          yamlConfig.Features.APIKeyManagement.Region,
		Logger:          logger,
		AutoCreateTable: false,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "create store: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	keys, err := store.ListKeys(ctx, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "list keys: %v\n", err)
		os.Exit(1)
	}

	var candidates []*apikeys.APIKey
	for _, key := range keys {
		if !apikeys.IsPersonalKey(key) {
			continue
		}
		if key.MonthlyCostLimit != fromCents {
			continue
		}
		candidates = append(candidates, key)
	}

	if len(candidates) == 0 {
		fmt.Printf("No personal keys with monthly_cost_limit=%d in %s (%s)\n",
			fromCents, yamlConfig.Features.APIKeyManagement.TableName, environment)
		return
	}

	mode := "DRY RUN"
	if apply {
		mode = "APPLY"
	}
	fmt.Printf(
		"%s: %d personal key(s) $%.2f/month -> $%.2f/month (table=%s region=%s)\n\n",
		mode,
		len(candidates),
		float64(fromCents)/100,
		float64(toCents)/100,
		yamlConfig.Features.APIKeyManagement.TableName,
		yamlConfig.Features.APIKeyManagement.Region,
	)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tOWNER\tPROVIDER\tCURRENT\tNEW")
	fmt.Fprintln(w, "---\t-----\t--------\t-------\t---")
	for _, key := range candidates {
		fmt.Fprintf(
			w, "%s\t%s\t%s\t$%.2f/mo\t$%.2f/mo\n",
			apikeys.RedactKey(key.PK),
			key.OwnerEmail,
			key.Provider,
			float64(key.MonthlyCostLimit)/100,
			float64(toCents)/100,
		)
	}
	w.Flush()

	if !apply {
		fmt.Printf("\nRe-run with --apply to persist (env=%s)\n", environment)
		return
	}

	var updated, failed int
	for _, key := range candidates {
		if err := store.UpdateKey(ctx, key.PK, map[string]interface{}{
			"monthly_cost_limit": toCents,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "update %s: %v\n", apikeys.RedactKey(key.PK), err)
			failed++
			continue
		}
		updated++
	}

	fmt.Printf("\nUpdated %d key(s)", updated)
	if failed > 0 {
		fmt.Printf(", %d failed", failed)
		os.Exit(1)
	}
	fmt.Println()
}

func printPersonalUsage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  %s personal bump-limit -env=production [--from=1000] [--to=2000] [--apply]\n", os.Args[0])
}
