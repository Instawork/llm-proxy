package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Instawork/llm-proxy/internal/provision"
	redis "github.com/redis/go-redis/v9"
)

func runPoolCommand(args []string) {
	fs := flag.NewFlagSet("pool", flag.ExitOnError)
	configDir := fs.String("config-dir", "configs", "Path to configuration directory")
	environment := fs.String("env", "dev", "Environment (dev, staging, production)")
	provider := fs.String("provider", "", "Provider name (anthropic)")
	key := fs.String("key", "", "Upstream API key to add to the pool")
	listKey := fs.String("pool-key", "", "Redis list key override")

	if len(args) == 0 {
		printPoolUsage()
		os.Exit(1)
	}
	subcmd := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		os.Exit(1)
	}

	if *provider != "anthropic" && *provider != "" {
		fmt.Fprintf(os.Stderr, "only anthropic pool operations are supported\n")
		os.Exit(1)
	}
	if *provider == "" {
		*provider = "anthropic"
	}

	yamlConfig, err := loadConfig(*configDir, *environment)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	poolRedisKey := yamlConfig.Features.APIKeyManagement.Provisioning.Anthropic.PoolRedisKey
	if *listKey != "" {
		poolRedisKey = *listKey
	}
	if poolRedisKey == "" {
		poolRedisKey = "llm:provision:anthropic:available"
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" && yamlConfig.Features.CircuitBreaker.Redis != nil {
		redisURL = os.ExpandEnv(yamlConfig.Features.CircuitBreaker.Redis.URL)
	}
	if redisURL == "" {
		fmt.Fprintf(os.Stderr, "REDIS_URL is required for pool operations\n")
		os.Exit(1)
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "redis url: %v\n", err)
		os.Exit(1)
	}
	rdb := redis.NewClient(opts)
	ctx := context.Background()

	switch subcmd {
	case "add":
		if strings.TrimSpace(*key) == "" {
			fmt.Fprintf(os.Stderr, "pool add requires --key\n")
			os.Exit(1)
		}
		if err := provision.PoolAdd(ctx, rdb, poolRedisKey, *key); err != nil {
			fmt.Fprintf(os.Stderr, "pool add: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Added key to %s\n", poolRedisKey)
	case "status":
		n, err := provision.PoolLen(ctx, rdb, poolRedisKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pool status: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("anthropic pool %s: %d available\n", poolRedisKey, n)
	default:
		printPoolUsage()
		os.Exit(1)
	}
}

func printPoolUsage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  %s pool add --provider anthropic --key sk-ant-api03-...\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s pool status --provider anthropic\n", os.Args[0])
}
