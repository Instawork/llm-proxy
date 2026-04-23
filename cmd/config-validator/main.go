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

	failed := false
	for _, file := range files {
		fmt.Printf("Validating %s... ", file)
		
		var err error
		if filepath.Base(file) == "base.yml" {
			// base.yml must be a complete, valid config
			_, err = config.LoadYAMLConfig(file)
		} else {
			// Other configs are environment-specific overlays and may be partial.
			// We validate them by merging with base.yml.
			_, err = config.LoadAndMergeConfigs([]string{"configs/base.yml", file})
		}

		if err != nil {
			fmt.Printf("FAILED\nError: %v\n", err)
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
