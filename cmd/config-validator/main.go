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

// validateSemantics checks for logical errors that YAML parsing won't catch:
//   - Pricing values must be positive (> 0)
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

			// Check pricing values are positive.
			if model.Pricing == nil {
				continue
			}
			mp, ok := model.Pricing.(*config.ModelPricing)
			if !ok || mp == nil {
				continue
			}
			for i, tier := range mp.Tiers {
				if tier.Input <= 0 {
					errs = append(errs, fmt.Sprintf(
						"%s/%s: tier[%d] input price must be > 0, got %g",
						providerName, modelName, i, tier.Input,
					))
				}
				if tier.Output <= 0 {
					errs = append(errs, fmt.Sprintf(
						"%s/%s: tier[%d] output price must be > 0, got %g",
						providerName, modelName, i, tier.Output,
					))
				}
			}
			for alias, p := range mp.Overrides {
				if p.Input <= 0 {
					errs = append(errs, fmt.Sprintf(
						"%s/%s: override[%q] input price must be > 0, got %g",
						providerName, modelName, alias, p.Input,
					))
				}
				if p.Output <= 0 {
					errs = append(errs, fmt.Sprintf(
						"%s/%s: override[%q] output price must be > 0, got %g",
						providerName, modelName, alias, p.Output,
					))
				}
			}
		}
	}

	return errs
}
