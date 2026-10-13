package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/config"
)

const version = "1.0.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "set-role", "set-service-key-access", "list", "get":
		runCommand(os.Args[1], os.Args[2:])
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "LLM Proxy Admin User Manager v%s\n\n", version)
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  %s set-role -email user@example.com -role admin\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s set-service-key-access -email user@example.com -enabled=true\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s list\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s get -email user@example.com\n\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "Flags:\n")
	fmt.Fprintf(os.Stderr, "  -config-dir  Path to configuration directory (default: configs)\n")
	fmt.Fprintf(os.Stderr, "  -env         Environment overlay (default: dev)\n")
	fmt.Fprintf(os.Stderr, "  -email       User email\n")
	fmt.Fprintf(os.Stderr, "  -role        admin, editor, or viewer\n")
	fmt.Fprintf(os.Stderr, "  -enabled     set-service-key-access: true grants viewers read-only visibility of org-wide service keys, false revokes (default: true)\n")
}

func runCommand(command string, args []string) {
	fs := flag.NewFlagSet(command, flag.ExitOnError)
	configDir := fs.String("config-dir", "configs", "Path to configuration directory")
	environment := fs.String("env", "dev", "Environment (dev, staging, production)")
	email := fs.String("email", "", "User email")
	role := fs.String("role", "", "Role: admin, editor, or viewer")
	enabled := fs.Bool("enabled", true, "set-service-key-access: grant (true) or revoke (false)")
	_ = fs.Parse(args)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	yamlConfig, err := loadConfig(*configDir, *environment)
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}
	if !yamlConfig.Features.AdminDashboard.Enabled {
		logger.Error("admin dashboard is not enabled in configuration")
		os.Exit(1)
	}

	userCfg := yamlConfig.Features.AdminDashboard.Users.DynamoDB
	if userCfg.TableName == "" || userCfg.Region == "" {
		logger.Error("admin users dynamodb table_name and region are required in config")
		os.Exit(1)
	}

	endpointURL := userCfg.EndpointURL
	if endpointURL == "" {
		endpointURL = os.Getenv("AWS_ENDPOINT_URL")
	}

	store, err := adminusers.NewStore(adminusers.StoreConfig{
		TableName:       userCfg.TableName,
		Region:          userCfg.Region,
		EndpointURL:     endpointURL,
		Logger:          logger,
		AutoCreateTable: userCfg.AutoCreateTable,
	})
	if err != nil {
		logger.Error("failed to create admin user store", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch command {
	case "set-role":
		if *email == "" || *role == "" {
			logger.Error("set-role requires -email and -role")
			os.Exit(1)
		}
		handleSetRole(ctx, store, *email, *role, logger)
	case "set-service-key-access":
		if *email == "" {
			logger.Error("set-service-key-access requires -email")
			os.Exit(1)
		}
		handleSetServiceKeyAccess(ctx, store, *email, *enabled, logger)
	case "list":
		handleList(ctx, store, logger)
	case "get":
		if *email == "" {
			logger.Error("get requires -email")
			os.Exit(1)
		}
		handleGet(ctx, store, *email, logger)
	}
}

func loadConfig(configDir, environment string) (*config.YAMLConfig, error) {
	configFiles := []string{filepath.Join(configDir, "base.yml")}
	if environment != "base" {
		configFiles = append(configFiles, filepath.Join(configDir, fmt.Sprintf("%s.yml", environment)))
	}
	return config.LoadAndMergeConfigs(configFiles)
}

func handleSetRole(ctx context.Context, store *adminusers.Store, email, roleStr string, logger *slog.Logger) {
	role, err := adminusers.ParseRole(roleStr)
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}

	_, getErr := store.GetUser(ctx, email)
	if getErr != nil {
		if errors.Is(getErr, adminusers.ErrUserNotFound) {
			if _, err := store.CreateUser(ctx, email, role); err != nil {
				logger.Error("failed to create user", "error", err)
				os.Exit(1)
			}
			fmt.Printf("✅ Created %s with role %s\n", strings.ToLower(email), role)
			return
		}
		logger.Error("failed to get user", "error", getErr)
		os.Exit(1)
	}

	if err := store.SetRole(ctx, email, role); err != nil {
		logger.Error("failed to set role", "error", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Updated %s to role %s\n", strings.ToLower(email), role)
}

func handleSetServiceKeyAccess(ctx context.Context, store *adminusers.Store, email string, enabled bool, logger *slog.Logger) {
	if _, err := store.GetUser(ctx, email); err != nil {
		if !errors.Is(err, adminusers.ErrUserNotFound) {
			logger.Error("failed to get user", "error", err)
			os.Exit(1)
		}
		if _, err := store.CreateUser(ctx, email, adminusers.RoleViewer); err != nil {
			logger.Error("failed to create user", "error", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Created %s with role %s\n", strings.ToLower(email), adminusers.RoleViewer)
	}

	if err := store.SetServiceKeyAccess(ctx, email, enabled); err != nil {
		logger.Error("failed to set service key access", "error", err)
		os.Exit(1)
	}
	if enabled {
		fmt.Printf("✅ %s can now view service keys\n", strings.ToLower(email))
	} else {
		fmt.Printf("✅ %s can no longer view service keys\n", strings.ToLower(email))
	}
}

func handleList(ctx context.Context, store *adminusers.Store, logger *slog.Logger) {
	users, err := store.ListUsers(ctx)
	if err != nil {
		logger.Error("failed to list users", "error", err)
		os.Exit(1)
	}
	if len(users) == 0 {
		fmt.Println("No users found")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "EMAIL\tROLE\tSERVICE KEYS\tNAME\tLAST LOGIN\tCREATED")
	fmt.Fprintln(w, "-----\t----\t------------\t----\t----------\t-------")
	for _, u := range users {
		lastLogin := "—"
		if !u.LastLoginAt.IsZero() {
			lastLogin = u.LastLoginAt.Format(time.RFC3339)
		}
		name := u.Name
		if name == "" {
			name = "—"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", u.Email, u.Role, serviceKeyAccessLabel(u), name, lastLogin, u.CreatedAt.Format("2006-01-02"))
	}
	w.Flush()
}

func handleGet(ctx context.Context, store *adminusers.Store, email string, logger *slog.Logger) {
	u, err := store.GetUser(ctx, email)
	if err != nil {
		logger.Error("failed to get user", "error", err)
		os.Exit(1)
	}
	fmt.Printf("\nUser Details:\n\n")
	fmt.Printf("Email:      %s\n", u.Email)
	fmt.Printf("Role:       %s\n", u.Role)
	fmt.Printf("Service keys: %s\n", serviceKeyAccessLabel(u))
	if u.Name != "" {
		fmt.Printf("Name:       %s\n", u.Name)
	}
	if !u.LastLoginAt.IsZero() {
		fmt.Printf("Last login: %s\n", u.LastLoginAt.Format(time.RFC3339))
	}
	fmt.Printf("Created:    %s\n", u.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Updated:    %s\n", u.UpdatedAt.Format(time.RFC3339))
}

func serviceKeyAccessLabel(u adminusers.User) string {
	switch {
	case u.Role == adminusers.RoleViewer && u.CanViewServiceKeys:
		return "granted"
	case u.Role == adminusers.RoleViewer:
		return "no"
	default:
		return "yes (role)"
	}
}
