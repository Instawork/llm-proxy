package cost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
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

// WriteRecord writes a cost record to the file
func (ft *FileTransport) WriteRecord(record *CostRecord) error {
	ft.fileMutex.Lock()
	defer ft.fileMutex.Unlock()
	
	// Ensure output directory exists
	dir := filepath.Dir(ft.outputFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	
	// Open file in append mode
	file, err := os.OpenFile(ft.outputFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
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

// ReadRecords reads cost records from the file since the given time
func (ft *FileTransport) ReadRecords(since time.Time) ([]CostRecord, error) {
	ft.fileMutex.Lock()
	defer ft.fileMutex.Unlock()
	
	file, err := os.Open(ft.outputFile)
	if os.IsNotExist(err) {
		return []CostRecord{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to open cost file: %w", err)
	}
	defer file.Close()
	
	var records []CostRecord
	decoder := json.NewDecoder(file)
	
	for decoder.More() {
		var record CostRecord
		if err := decoder.Decode(&record); err != nil {
			// Log warning but continue processing other records
			continue
		}
		// Skip records older than the since time
		if record.Timestamp.Before(since) {
			continue
		}
		records = append(records, record)
	}
	return records, nil
} 
