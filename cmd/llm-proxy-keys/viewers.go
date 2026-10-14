package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/Instawork/llm-proxy/internal/admin"
	"github.com/Instawork/llm-proxy/internal/apikeys"
)

type viewersAction string

const (
	viewersList   viewersAction = "list"
	viewersAdd    viewersAction = "add"
	viewersRemove viewersAction = "remove"
)

// runViewersCommand implements `viewers list|add|remove` for one org key.
func runViewersCommand(args []string) {
	if len(args) == 0 {
		printViewersUsage()
		os.Exit(1)
	}
	action := viewersAction(args[0])
	if action != viewersList && action != viewersAdd && action != viewersRemove {
		printViewersUsage()
		os.Exit(1)
	}

	fs := flag.NewFlagSet("viewers", flag.ExitOnError)
	configDir := fs.String("config-dir", "configs", "Path to configuration directory")
	environment := fs.String("env", "dev", "Environment (dev, staging, production)")
	key := fs.String("key", "", "Proxy key (sk-iw-...) to manage viewers on")
	emails := fs.String("email", "", "Comma-separated viewer email addresses (add/remove)")
	if err := fs.Parse(args[1:]); err != nil {
		os.Exit(1)
	}

	if strings.TrimSpace(*key) == "" {
		fmt.Fprintf(os.Stderr, "viewers %s requires -key\n", action)
		os.Exit(1)
	}
	var viewerEmails []string
	if action != viewersList {
		var err error
		viewerEmails, err = parseViewerEmails(*emails)
		if err != nil {
			fmt.Fprintf(os.Stderr, "viewers %s: %v\n", action, err)
			os.Exit(1)
		}
		if action == viewersRemove && len(viewerEmails) != 1 {
			fmt.Fprintf(os.Stderr, "viewers remove takes exactly one -email\n")
			os.Exit(1)
		}
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	yamlConfig, err := loadConfig(*configDir, *environment)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	if !yamlConfig.Features.APIKeyManagement.Enabled {
		fmt.Fprintf(os.Stderr, "api_key_management is disabled in %s config\n", *environment)
		os.Exit(1)
	}

	if action == viewersAdd {
		if err := checkViewerDomains(viewerEmails, yamlConfig.Features.AdminDashboard.AllowedDomain); err != nil {
			fmt.Fprintf(os.Stderr, "viewers add: %v\n", err)
			os.Exit(1)
		}
	}

	if viewersActionWrites(action) && *environment == "production" {
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

	if err := executeViewers(context.Background(), store, action, *key, viewerEmails); err != nil {
		fmt.Fprintf(os.Stderr, "viewers %s: %v\n", action, err)
		os.Exit(1)
	}
}

// viewersActionWrites reports whether the action mutates the key record and so
// must pass the production guard.
func viewersActionWrites(a viewersAction) bool {
	return a == viewersAdd || a == viewersRemove
}

// executeViewers runs action against store and prints the resulting viewer list.
func executeViewers(ctx context.Context, store *apikeys.Store, action viewersAction, key string, emails []string) error {
	var (
		record *apikeys.APIKey
		err    error
	)
	switch action {
	case viewersList:
		record, err = store.GetKeyRecord(ctx, key)
	case viewersAdd:
		record, err = store.AddKeyViewers(ctx, key, emails)
	case viewersRemove:
		record, err = store.RemoveKeyViewer(ctx, key, emails[0])
	}
	if err != nil {
		return err
	}
	printViewers(record)
	return nil
}

// printViewers prints the redacted key, implicit requester, and explicit viewers.
func printViewers(k *apikeys.APIKey) {
	fmt.Printf("Key:       %s\n", apikeys.RedactKey(k.PK))
	if k.RequesterEmail != "" {
		fmt.Printf("Requester: %s (implicit viewer)\n", k.RequesterEmail)
	}
	if len(k.ViewerEmails) == 0 {
		fmt.Printf("Viewers:   (none)\n")
		return
	}
	fmt.Printf("Viewers:   %s\n", strings.Join(k.ViewerEmails, ", "))
}

// checkViewerDomains applies the same allowed-domain rule as the admin API
// (env var, then YAML, then example.com) so the CLI cannot grant access the
// dashboard would refuse.
func checkViewerDomains(emails []string, yamlDomain string) error {
	domain := admin.ResolveAllowedDomain(yamlDomain)
	for _, e := range emails {
		if err := admin.ValidateAllowedEmail(e, domain); err != nil {
			return fmt.Errorf("%s: %w (allowed: %s)", e, err, domain)
		}
	}
	return nil
}

// parseViewerEmails splits a comma-separated list, normalizing and
// de-duplicating entries and rejecting anything that is not a bare address.
func parseViewerEmails(raw string) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		email := apikeys.NormalizeViewerEmail(part)
		if email == "" {
			continue
		}
		if err := apikeys.ValidateViewerEmail(email); err != nil {
			return nil, err
		}
		if _, dup := seen[email]; dup {
			continue
		}
		seen[email] = struct{}{}
		out = append(out, email)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one -email is required")
	}
	return out, nil
}

// printViewersUsage prints the viewers subcommand help.
func printViewersUsage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  %s viewers list   -key sk-iw-... [-env=dev]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s viewers add    -key sk-iw-... -email a@x.com,b@x.com [-env=dev]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s viewers remove -key sk-iw-... -email a@x.com [-env=dev]\n", os.Args[0])
}
