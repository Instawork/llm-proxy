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
func validateSemantics(cfg *config.YAMLConfig) []string {
	var errs []string

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

			// Check for duplicate aliases.
			for _, alias := range model.Aliases {
				if existing, seen := seenAliases[alias]; seen {
					errs = append(errs, fmt.Sprintf(
						"%s: alias %q appears on both %q and %q",
						providerName, alias, existing, modelName,
					))
				} else {
					seenAliases[alias] = modelName
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
