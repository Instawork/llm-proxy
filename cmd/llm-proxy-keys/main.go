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
	// Command-line flags
	var (
		configDir   = flag.String("config-dir", "configs", "Path to configuration directory")
		environment = flag.String("env", "dev", "Environment (dev, staging, production)")
		provider    = flag.String("provider", "", "Provider name (openai, anthropic, gemini)")
		actualKey   = flag.String("key", "", "Actual provider API key")
		description = flag.String("desc", "", "Description for the key")
		costLimit   = flag.Int64("cost-limit", 10000, "Daily cost limit in cents (default: $100)")
		listKeys    = flag.Bool("list", false, "List all API keys")
		deleteKey   = flag.String("delete", "", "Delete an API key")
		disableKey  = flag.String("disable", "", "Disable an API key")
		enableKey   = flag.String("enable", "", "Enable an API key")
		showKey     = flag.String("show", "", "Show details of a specific API key")
		tags        = flag.String("tags", "", "Comma-separated key=value tags")
	)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "LLM Proxy API Key Manager v%s\n\n", version)
		fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  Create a key:    -provider=openai -key=sk-xxx -desc=\"Production key\" -cost-limit=50000\n")
		fmt.Fprintf(os.Stderr, "  List keys:       -list\n")
		fmt.Fprintf(os.Stderr, "  Show key:        -show=iw:xxx\n")
		fmt.Fprintf(os.Stderr, "  Delete key:      -delete=iw:xxx\n")
		fmt.Fprintf(os.Stderr, "  Disable key:     -disable=iw:xxx\n")
		fmt.Fprintf(os.Stderr, "  Enable key:      -enable=iw:xxx\n\n")
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

	// Create API key store
	store, err := apikeys.NewStore(apikeys.StoreConfig{
		TableName: yamlConfig.Features.APIKeyManagement.TableName,
		Region:    yamlConfig.Features.APIKeyManagement.Region,
		Logger:    logger,
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
		handleCreate(ctx, store, *provider, *actualKey, *description, *costLimit, *tags, logger)
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
func handleCreate(ctx context.Context, store *apikeys.Store, provider, actualKey, description string, costLimit int64, tagsStr string, logger *slog.Logger) {
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
	apiKey, err := store.CreateKey(ctx, provider, actualKey, description, costLimit, tags)
	if err != nil {
		logger.Error("Failed to create API key", "error", err)
		os.Exit(1)
	}

	fmt.Printf("\n✅ API Key Created Successfully!\n\n")
	fmt.Printf("Key:         %s\n", apiKey.PK)
	fmt.Printf("Provider:    %s\n", apiKey.Provider)
	fmt.Printf("Description: %s\n", apiKey.Description)
	fmt.Printf("Cost Limit:  $%.2f/day\n", float64(apiKey.DailyCostLimit)/100)
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

	for _, key := range keys {
		fmt.Fprintf(w, "%s\t%s\t%s\t$%.2f/day\t%v\t%s\n",
			key.PK,
			key.Provider,
			key.Description,
			float64(key.DailyCostLimit)/100,
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
	fmt.Printf("Key:         %s\n", key.PK)
	fmt.Printf("Provider:    %s\n", key.Provider)
	fmt.Printf("Description: %s\n", key.Description)
	fmt.Printf("Cost Limit:  $%.2f/day\n", float64(key.DailyCostLimit)/100)
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
