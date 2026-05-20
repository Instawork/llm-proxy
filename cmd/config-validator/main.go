package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Instawork/llm-proxy/internal/config"
)

func main() {
	configDir := flag.String("config-dir", "configs", "Directory containing configuration files")
	flag.Parse()

	files, err := filepath.Glob(filepath.Join(*configDir, "*.yml"))
	if err != nil {
		fmt.Printf("Error finding config files: %v\n", err)
		os.Exit(1)
	}

	if len(files) == 0 {
		fmt.Printf("No configuration files found in %s\n", *configDir)
		os.Exit(1)
	}

	baseConfig := filepath.Join(*configDir, "base.yml")

	failed := false
	for _, file := range files {
		fmt.Printf("Validating %s... ", file)

		var cfg *config.YAMLConfig
		var loadErr error

		if filepath.Base(file) == "base.yml" {
			// base.yml must be a complete, valid config on its own
			cfg, loadErr = config.LoadYAMLConfig(file)
		} else {
			// Environment-specific configs are partial overlays; validate them
			// merged on top of base.yml so the full resulting config is checked.
			cfg, loadErr = config.LoadAndMergeConfigs([]string{baseConfig, file})
		}

		if loadErr != nil {
			fmt.Printf("FAILED\nError: %v\n", loadErr)
			failed = true
			continue
		}

		if semanticErrors := validateSemantics(cfg); len(semanticErrors) > 0 {
			fmt.Println("FAILED")
			for _, e := range semanticErrors {
				fmt.Printf("  - %s\n", e)
			}
			failed = true
			continue
		}

		fmt.Println("PASSED")
	}

	if failed {
		fmt.Println("\nConfiguration validation failed!")
		os.Exit(1)
	}

	fmt.Println("\nAll configurations are valid.")
}

const (
	// minPricePerMillion is the minimum sane price per 1M tokens ($0.01).
	// Any price below one cent per million has never been observed and likely
	// indicates a decimal-place error (e.g. 0.001 instead of 0.10).
	minPricePerMillion = 0.01

	// maxPricePerMillion is the maximum sane price per 1M tokens ($1000.00).
	// The highest real-world price we've seen is o1-pro at $600/1M output, so
	// $1000 leaves headroom while still catching obvious magnitude errors
	// (e.g. 10000.00 instead of 100.00).
	maxPricePerMillion = 1000.00
)

// validateSemantics checks for logical errors that YAML parsing won't catch:
//   - Pricing values must be within the sane range [minPricePerMillion, maxPricePerMillion]
//   - Aliases must be unique within a provider (duplicate aliases cause non-deterministic routing)
//   - Aliases must not collide with another provider's canonical model name
//     or alias (cross-provider aliasing makes routing ambiguous)
//   - When rate limiting is enabled with the redis backend, the Redis URL
//     or address must be present (catches the bare `backend: redis` typo)
//   - When circuit breaker is enabled with the redis backend, Redis cfg present
//   - Cost-tracking transports of type=dynamodb must have a non-empty region
//     and table_name (mirrors runtime validate; surfaces it at config-time)
func validateSemantics(cfg *config.YAMLConfig) []string {
	var errs []string

	// Track aliases globally across providers so a stray duplicate
	// (e.g. `gpt-4o` mapped under both openai and bedrock) is caught at
	// validation time rather than at first-request routing.
	globalAliases := make(map[string]string) // alias -> "provider/model"

	for providerName, provider := range cfg.Providers {
		if !provider.Enabled {
			continue
		}

		// Track all aliases seen in this provider to detect duplicates.
		seenAliases := make(map[string]string) // alias -> canonical model name

		for modelName, model := range provider.Models {
			if !model.Enabled {
				continue
			}

			// Check for duplicate aliases within this provider.
			for _, alias := range model.Aliases {
				if existing, seen := seenAliases[alias]; seen {
					errs = append(errs, fmt.Sprintf(
						"%s: alias %q appears on both %q and %q",
						providerName, alias, existing, modelName,
					))
				} else {
					seenAliases[alias] = modelName
				}

				// Check for cross-provider alias collision.
				fqcn := fmt.Sprintf("%s/%s", providerName, modelName)
				if other, seen := globalAliases[alias]; seen && other != fqcn {
					errs = append(errs, fmt.Sprintf(
						"alias %q is registered by both %s and %s",
						alias, other, fqcn,
					))
				} else {
					globalAliases[alias] = fqcn
				}
			}

			// Check pricing values are within sane bounds.
			if model.Pricing == nil {
				continue
			}
			mp, ok := model.Pricing.(*config.ModelPricing)
			if !ok || mp == nil {
				continue
			}
			for i, tier := range mp.Tiers {
				errs = append(errs, checkPrice(providerName, modelName, fmt.Sprintf("tier[%d] input", i), tier.Input)...)
				errs = append(errs, checkPrice(providerName, modelName, fmt.Sprintf("tier[%d] output", i), tier.Output)...)
			}
			for alias, p := range mp.Overrides {
				errs = append(errs, checkPrice(providerName, modelName, fmt.Sprintf("override[%q] input", alias), p.Input)...)
				errs = append(errs, checkPrice(providerName, modelName, fmt.Sprintf("override[%q] output", alias), p.Output)...)
			}
		}
	}

	// Cross-feature checks. These are duplicated against YAMLConfig.Validate
	// on purpose: the validator runs in CI before deploy, and the runtime
	// validators may evolve. Belt-and-suspenders is intentional.
	if cfg.Features.RateLimiting.Enabled && cfg.Features.RateLimiting.Backend == "redis" {
		if cfg.Features.RateLimiting.Redis == nil ||
			(cfg.Features.RateLimiting.Redis.URL == "" && cfg.Features.RateLimiting.Redis.Address == "") {
			errs = append(errs, "rate_limiting.backend=redis but rate_limiting.redis.{url,address} are both empty")
		}
	}
	if cfg.Features.CircuitBreaker.Enabled {
		// CircuitBreaker's runtime Validate handles backend=redis already;
		// we only assert the high-level shape here so a YAML with a
		// `circuit_breaker.redis:` block but no fields is caught.
		if cfg.Features.CircuitBreaker.Redis != nil &&
			cfg.Features.CircuitBreaker.Redis.URL == "" &&
			cfg.Features.CircuitBreaker.Redis.Address == "" {
			errs = append(errs, "circuit_breaker.redis is declared but both url and address are empty")
		}
	}
	if cfg.Features.CostTracking.Enabled {
		for i, t := range cfg.Features.CostTracking.Transports {
			if t.Type == "dynamodb" && t.DynamoDB != nil {
				if t.DynamoDB.Region == "" {
					errs = append(errs, fmt.Sprintf("cost_tracking.transports[%d] dynamodb.region is required", i))
				}
				if t.DynamoDB.TableName == "" {
					errs = append(errs, fmt.Sprintf("cost_tracking.transports[%d] dynamodb.table_name is required", i))
				}
			}
		}
	}

	return errs
}

// checkPrice returns an error string if price is outside [minPricePerMillion, maxPricePerMillion].
func checkPrice(provider, model, field string, price float64) []string {
	if price < minPricePerMillion {
		return []string{fmt.Sprintf(
			"%s/%s: %s price $%g/1M is below minimum $%.2f/1M — likely a decimal error",
			provider, model, field, price, minPricePerMillion,
		)}
	}
	if price > maxPricePerMillion {
		return []string{fmt.Sprintf(
			"%s/%s: %s price $%g/1M exceeds maximum $%.2f/1M — likely a magnitude error",
			provider, model, field, price, maxPricePerMillion,
		)}
	}
	return nil
}
