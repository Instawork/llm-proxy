package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
)

const (
	version = "1.0.0"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "pool" {
		runPoolCommand(os.Args[2:])
		return
	}

	// Command-line flags
	var (
		configDir     = flag.String("config-dir", "configs", "Path to configuration directory")
		environment   = flag.String("env", "dev", "Environment (dev, staging, production)")
		provider      = flag.String("provider", "", "Provider name (openai, anthropic, gemini)")
		actualKey     = flag.String("key", "", "Actual provider API key")
		description   = flag.String("desc", "", "Description for the key")
		costLimit     = flag.Int64("cost-limit", 10000, "Daily cost limit in cents (default: $100)")
		listKeys      = flag.Bool("list", false, "List all API keys")
		deleteKey     = flag.String("delete", "", "Delete an API key")
		disableKey    = flag.String("disable", "", "Disable an API key")
		enableKey     = flag.String("enable", "", "Enable an API key")
		showKey       = flag.String("show", "", "Show details of a specific API key")
		tags          = flag.String("tags", "", "Comma-separated key=value tags")
		redactPIIFlag = flag.String("redact-pii", "", "PII redaction override: true, false, or omit to inherit global")
	)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "LLM Proxy API Key Manager v%s\n\n", version)
		fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  Create a key:    -provider=openai -key=sk-xxx -desc=\"Production key\" -cost-limit=50000\n")
		fmt.Fprintf(os.Stderr, "  List keys:       -list\n")
		fmt.Fprintf(os.Stderr, "  Show key:        -show=sk-iw-xxx\n")
		fmt.Fprintf(os.Stderr, "  Delete key:      -delete=sk-iw-xxx\n")
		fmt.Fprintf(os.Stderr, "  Disable key:     -disable=sk-iw-xxx\n")
		fmt.Fprintf(os.Stderr, "  Enable key:      -enable=sk-iw-xxx\n\n")
		fmt.Fprintf(os.Stderr, "Pool commands:\n")
		fmt.Fprintf(os.Stderr, "  pool add:        pool add --provider anthropic --key sk-ant-api03-...\n")
		fmt.Fprintf(os.Stderr, "  pool status:     pool status --provider anthropic\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	// Set up logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Load configuration
	yamlConfig, err := loadConfig(*configDir, *environment)
	if err != nil {
		logger.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	// Check if API key management is enabled
	if !yamlConfig.Features.APIKeyManagement.Enabled {
		logger.Error("API key management is not enabled in configuration")
		os.Exit(1)
	}

	keyPrefixBase := os.Getenv("LLM_PROXY_API_KEY_PREFIX")
	if keyPrefixBase == "" {
		keyPrefixBase = yamlConfig.Features.APIKeyManagement.KeyPrefix
	}
	apikeys.SetKeyPrefixBase(keyPrefixBase)

	// Create API key store. The CLI is normally used in local dev where
	// AutoCreateTable=true is desirable; defer to YAML config so the same
	// guard applies as the proxy.
	store, err := apikeys.NewStore(apikeys.StoreConfig{
		TableName:       yamlConfig.Features.APIKeyManagement.TableName,
		Region:          yamlConfig.Features.APIKeyManagement.Region,
		Logger:          logger,
		AutoCreateTable: yamlConfig.Features.APIKeyManagement.AutoCreateTable,
	})
	if err != nil {
		logger.Error("Failed to create API key store", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	// Handle commands
	switch {
	case *listKeys:
		handleList(ctx, store, *provider, logger)
	case *showKey != "":
		handleShow(ctx, store, *showKey, logger)
	case *deleteKey != "":
		handleDelete(ctx, store, *deleteKey, logger)
	case *disableKey != "":
		handleDisable(ctx, store, *disableKey, logger)
	case *enableKey != "":
		handleEnable(ctx, store, *enableKey, logger)
	case *provider != "" && *actualKey != "":
		handleCreate(ctx, store, *provider, *actualKey, *description, *costLimit, *tags, *redactPIIFlag, logger)
	default:
		flag.Usage()
		os.Exit(1)
	}
}

// loadConfig loads the configuration from YAML files
func loadConfig(configDir, environment string) (*config.YAMLConfig, error) {
	// Build list of config files to load
	configFiles := []string{
		filepath.Join(configDir, "base.yml"),
	}

	// Add environment-specific config if not base
	if environment != "base" {
		envFile := filepath.Join(configDir, fmt.Sprintf("%s.yml", environment))
		configFiles = append(configFiles, envFile)
	}

	// Load and merge configurations
	return config.LoadAndMergeConfigs(configFiles)
}

// handleCreate creates a new API key
func handleCreate(ctx context.Context, store *apikeys.Store, provider, actualKey, description string, costLimit int64, tagsStr, redactPIIFlag string, logger *slog.Logger) {
	// Validate provider
	validProviders := []string{"openai", "anthropic", "gemini"}
	isValid := false
	for _, p := range validProviders {
		if provider == p {
			isValid = true
			break
		}
	}
	if !isValid {
		logger.Error("Invalid provider", "provider", provider, "valid", validProviders)
		os.Exit(1)
	}

	// Parse tags
	tags := make(map[string]string)
	if tagsStr != "" {
		for _, tag := range strings.Split(tagsStr, ",") {
			parts := strings.SplitN(tag, "=", 2)
			if len(parts) == 2 {
				tags[parts[0]] = parts[1]
			}
		}
	}

	// Create the key
	var redactPII *bool
	switch strings.ToLower(strings.TrimSpace(redactPIIFlag)) {
	case "true", "1", "yes", "on":
		v := true
		redactPII = &v
	case "false", "0", "no", "off":
		v := false
		redactPII = &v
	case "":
	default:
		logger.Error("Invalid -redact-pii value; use true, false, or omit", "value", redactPIIFlag)
		os.Exit(1)
	}

	apiKey, err := store.CreateKey(ctx, provider, actualKey, description, costLimit, tags, redactPII)
	if err != nil {
		logger.Error("Failed to create API key", "error", err)
		os.Exit(1)
	}

	fmt.Printf("\n✅ API Key Created Successfully!\n\n")
	fmt.Printf("⚠️  This is the only time the full key value will be displayed.\n")
	fmt.Printf("    Copy it to a secrets manager now; subsequent `list`/`show` calls will show a redacted form.\n\n")
	fmt.Printf("Key:         %s\n", apiKey.PK)
	fmt.Printf("Provider:    %s\n", apiKey.Provider)
	fmt.Printf("Description: %s\n", apiKey.Description)
	fmt.Printf("Cost Limit:  %s\n", formatKeyCostLimit(apiKey))
	fmt.Printf("Created:     %s\n", apiKey.CreatedAt.Format(time.RFC3339))
	if len(apiKey.Tags) > 0 {
		fmt.Printf("Tags:        %v\n", apiKey.Tags)
	}
	fmt.Printf("\n🔑 Use this key in your API requests by replacing your provider key with: %s\n", apiKey.PK)
}

// handleList lists all API keys
func handleList(ctx context.Context, store *apikeys.Store, provider string, logger *slog.Logger) {
	keys, err := store.ListKeys(ctx, provider)
	if err != nil {
		logger.Error("Failed to list API keys", "error", err)
		os.Exit(1)
	}

	if len(keys) == 0 {
		fmt.Println("No API keys found")
		return
	}

	// Create a tabwriter for formatted output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tPROVIDER\tDESCRIPTION\tCOST LIMIT\tENABLED\tCREATED")
	fmt.Fprintln(w, "---\t--------\t-----------\t----------\t-------\t-------")

	// Redact key values in the list view so terminal scrollback, shell
	// history, CI logs, or screenshots don't accidentally expose bearer
	// secrets. Operators who need the full value should run create (which
	// prints once) or fetch the underlying record directly.
	for _, key := range keys {
		fmt.Fprintf(
			w, "%s\t%s\t%s\t%s\t%v\t%s\n",
			apikeys.RedactKey(key.PK),
			key.Provider,
			key.Description,
			formatKeyCostLimit(key),
			key.Enabled,
			key.CreatedAt.Format("2006-01-02"),
		)
	}
	w.Flush()
}

// handleShow shows details of a specific API key
func handleShow(ctx context.Context, store *apikeys.Store, keyID string, logger *slog.Logger) {
	key, err := store.GetKey(ctx, keyID)
	if err != nil {
		logger.Error("Failed to get API key", "error", err)
		os.Exit(1)
	}

	fmt.Printf("\nAPI Key Details:\n\n")
	fmt.Printf("Key:         %s\n", apikeys.RedactKey(key.PK))
	fmt.Printf("Provider:    %s\n", key.Provider)
	fmt.Printf("Description: %s\n", key.Description)
	fmt.Printf("Cost Limit:  %s\n", formatKeyCostLimit(key))
	fmt.Printf("Enabled:     %v\n", key.Enabled)
	fmt.Printf("Created:     %s\n", key.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Updated:     %s\n", key.UpdatedAt.Format(time.RFC3339))
	if key.ExpiresAt != nil {
		fmt.Printf("Expires:     %s\n", key.ExpiresAt.Format(time.RFC3339))
	}
	if len(key.Tags) > 0 {
		fmt.Printf("Tags:        %v\n", key.Tags)
	}
	// Don't show the actual key for security reasons
	fmt.Printf("Actual Key:  ***HIDDEN***\n")
}

// handleDelete deletes an API key
func handleDelete(ctx context.Context, store *apikeys.Store, keyID string, logger *slog.Logger) {
	err := store.DeleteKey(ctx, keyID)
	if err != nil {
		logger.Error("Failed to delete API key", "error", err)
		os.Exit(1)
	}

	fmt.Printf("✅ API key %s deleted successfully\n", keyID)
}

// handleDisable disables an API key
func handleDisable(ctx context.Context, store *apikeys.Store, keyID string, logger *slog.Logger) {
	err := store.UpdateKey(ctx, keyID, map[string]interface{}{
		"enabled": false,
	})
	if err != nil {
		logger.Error("Failed to disable API key", "error", err)
		os.Exit(1)
	}

	fmt.Printf("✅ API key %s disabled successfully\n", keyID)
}

// handleEnable enables an API key
func handleEnable(ctx context.Context, store *apikeys.Store, keyID string, logger *slog.Logger) {
	err := store.UpdateKey(ctx, keyID, map[string]interface{}{
		"enabled": true,
	})
	if err != nil {
		logger.Error("Failed to enable API key", "error", err)
		os.Exit(1)
	}

	fmt.Printf("✅ API key %s enabled successfully\n", keyID)
}

func formatKeyCostLimit(key *apikeys.APIKey) string {
	if apikeys.IsPersonalKey(key) {
		if key.MonthlyCostLimit <= 0 {
			return "Unlimited/month"
		}
		return fmt.Sprintf("$%.2f/month", float64(key.MonthlyCostLimit)/100)
	}
	if key.DailyCostLimit <= 0 {
		return "Unlimited/day"
	}
	return fmt.Sprintf("$%.2f/day", float64(key.DailyCostLimit)/100)
}
