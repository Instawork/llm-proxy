package cost

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/Instawork/llm-proxy/internal/config"
)

// FileTransport implements Transport interface for file-based cost tracking
type FileTransport struct {
	outputFile string
	fileMutex  sync.Mutex
}

// NewFileTransport creates a new file-based transport
func NewFileTransport(outputFile string) *FileTransport {
	return &FileTransport{
		outputFile: outputFile,
	}
}

// FromConfig creates a FileTransport from configuration
func (ft *FileTransport) FromConfig(transportConfig interface{}, logger *slog.Logger) (Transport, error) {
	switch cfg := transportConfig.(type) {
	case *config.TransportConfig:
		if cfg.File == nil {
			return nil, fmt.Errorf("file transport configuration not found")
		}
		logger.Debug("💰 File Transport: Creating from structured config", "path", cfg.File.Path)
		return NewFileTransport(cfg.File.Path), nil

	case map[string]interface{}:
		fileConfig, ok := cfg["file"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("file transport configuration not found")
		}
		path, ok := fileConfig["path"].(string)
		if !ok {
			return nil, fmt.Errorf("file path not specified")
		}
		logger.Debug("💰 File Transport: Creating from map config", "path", path)
		return NewFileTransport(path), nil

	default:
		return nil, fmt.Errorf("unsupported config type for file transport: %T", transportConfig)
	}
}

// NewFileTransportFromConfig creates a FileTransport from configuration (convenience function)
func NewFileTransportFromConfig(transportConfig interface{}, logger *slog.Logger) (Transport, error) {
	ft := &FileTransport{}
	return ft.FromConfig(transportConfig, logger)
}

// WriteRecord writes a cost record to the file
func (ft *FileTransport) WriteRecord(record *CostRecord) error {
	ft.fileMutex.Lock()
	defer ft.fileMutex.Unlock()

	// Ensure output directory exists
	dir := filepath.Dir(ft.outputFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Open file in append mode
	file, err := os.OpenFile(ft.outputFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open cost tracking file: %w", err)
	}
	defer file.Close()

	// Write record as JSON line
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(record); err != nil {
		return fmt.Errorf("failed to write cost record: %w", err)
	}

	return nil
}
