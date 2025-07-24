package ratelimit

import (
	"fmt"
	"sync"
	"time"
)

// MemoryStore implements RateLimitStore using in-memory storage
type MemoryStore struct {
	// Token buckets storage
	buckets map[string]*TokenBucket
	bucketsMutex sync.RWMutex
	
	// Usage records storage
	records map[string][]*UsageRecord
	recordsMutex sync.RWMutex
	
	// Usage stats cache
	statsCache map[string]*UsageStats
	statsCacheMutex sync.RWMutex
	
	// Configuration
	maxRecordsPerKey int
	cleanupInterval  time.Duration
	
	// Cleanup management
	stopCleanup chan struct{}
	cleanupDone chan struct{}
}

// NewMemoryStore creates a new in-memory rate limiting store
func NewMemoryStore() *MemoryStore {
	store := &MemoryStore{
		buckets:          make(map[string]*TokenBucket),
		records:          make(map[string][]*UsageRecord),
		statsCache:       make(map[string]*UsageStats),
		maxRecordsPerKey: 10000, // Maximum number of records to keep per key
		cleanupInterval:  5 * time.Minute, // Cleanup expired data every 5 minutes
		stopCleanup:      make(chan struct{}),
		cleanupDone:      make(chan struct{}),
	}
	
	// Start background cleanup goroutine
	go store.backgroundCleanup()
	
	return store
}

// NewMemoryStoreWithConfig creates a new in-memory store with custom configuration
func NewMemoryStoreWithConfig(maxRecordsPerKey int, cleanupInterval time.Duration) *MemoryStore {
	store := &MemoryStore{
		buckets:          make(map[string]*TokenBucket),
		records:          make(map[string][]*UsageRecord),
		statsCache:       make(map[string]*UsageStats),
		maxRecordsPerKey: maxRecordsPerKey,
		cleanupInterval:  cleanupInterval,
		stopCleanup:      make(chan struct{}),
		cleanupDone:      make(chan struct{}),
	}
	
	// Start background cleanup goroutine
	go store.backgroundCleanup()
	
	return store
}

// GetBucket implements RateLimitStore interface
func (ms *MemoryStore) GetBucket(key string) (*TokenBucket, error) {
	ms.bucketsMutex.RLock()
	defer ms.bucketsMutex.RUnlock()
	
	bucket, exists := ms.buckets[key]
	if !exists {
		return nil, nil // Return nil if bucket doesn't exist (not an error)
	}
	
	// Check if bucket is expired
	if bucket.IsExpired() {
		return nil, nil // Return nil for expired buckets
	}
	
	return bucket, nil
}

// SetBucket implements RateLimitStore interface
func (ms *MemoryStore) SetBucket(key string, bucket *TokenBucket) error {
	ms.bucketsMutex.Lock()
	defer ms.bucketsMutex.Unlock()
	
	ms.buckets[key] = bucket
	return nil
}

// DeleteBucket implements RateLimitStore interface
func (ms *MemoryStore) DeleteBucket(key string) error {
	ms.bucketsMutex.Lock()
	defer ms.bucketsMutex.Unlock()
	
	delete(ms.buckets, key)
	return nil
}

// AddUsageRecord implements RateLimitStore interface
func (ms *MemoryStore) AddUsageRecord(key string, record *UsageRecord) error {
	ms.recordsMutex.Lock()
	defer ms.recordsMutex.Unlock()
	
	// Add the record to the key's record list
	ms.records[key] = append(ms.records[key], record)
	
	// Trim records if we exceed the maximum
	if len(ms.records[key]) > ms.maxRecordsPerKey {
		// Remove the oldest records
		ms.records[key] = ms.records[key][len(ms.records[key])-ms.maxRecordsPerKey:]
	}
	
	// Invalidate stats cache for this key
	ms.statsCacheMutex.Lock()
	delete(ms.statsCache, key)
	ms.statsCacheMutex.Unlock()
	
	return nil
}

// GetUsageStats implements RateLimitStore interface
func (ms *MemoryStore) GetUsageStats(key string) (*UsageStats, error) {
	// Check cache first
	ms.statsCacheMutex.RLock()
	if stats, exists := ms.statsCache[key]; exists {
		ms.statsCacheMutex.RUnlock()
		return stats, nil
	}
	ms.statsCacheMutex.RUnlock()
	
	// Get records for the key
	ms.recordsMutex.RLock()
	records, exists := ms.records[key]
	if !exists || len(records) == 0 {
		ms.recordsMutex.RUnlock()
		return NewUsageStats(time.Now(), time.Now()), nil
	}
	
	// Calculate time window
	windowStart := records[0].Timestamp
	windowEnd := records[len(records)-1].Timestamp
	
	// Create stats and add all records
	stats := NewUsageStats(windowStart, windowEnd)
	for _, record := range records {
		stats.AddRecord(record)
	}
	ms.recordsMutex.RUnlock()
	
	// Calculate rates
	stats.CalculateRates()
	
	// Cache the stats
	ms.statsCacheMutex.Lock()
	ms.statsCache[key] = stats
	ms.statsCacheMutex.Unlock()
	
	return stats, nil
}

// GetUsageRecords implements RateLimitStore interface
func (ms *MemoryStore) GetUsageRecords(key string, since time.Time) ([]*UsageRecord, error) {
	ms.recordsMutex.RLock()
	defer ms.recordsMutex.RUnlock()
	
	records, exists := ms.records[key]
	if !exists {
		return []*UsageRecord{}, nil
	}
	
	// Filter records by timestamp
	var filteredRecords []*UsageRecord
	for _, record := range records {
		if record.Timestamp.After(since) {
			filteredRecords = append(filteredRecords, record)
		}
	}
	
	return filteredRecords, nil
}

// ResetUsage implements RateLimitStore interface
func (ms *MemoryStore) ResetUsage(key string) error {
	ms.recordsMutex.Lock()
	delete(ms.records, key)
	ms.recordsMutex.Unlock()
	
	ms.statsCacheMutex.Lock()
	delete(ms.statsCache, key)
	ms.statsCacheMutex.Unlock()
	
	return nil
}

// CleanupExpired implements RateLimitStore interface
func (ms *MemoryStore) CleanupExpired() error {
	now := time.Now()
	cleanupCount := 0
	
	// Clean up expired buckets
	ms.bucketsMutex.Lock()
	for key, bucket := range ms.buckets {
		if bucket.IsExpired() {
			delete(ms.buckets, key)
			cleanupCount++
		}
	}
	ms.bucketsMutex.Unlock()
	
	// Clean up old records (older than 24 hours)
	cutoffTime := now.Add(-24 * time.Hour)
	ms.recordsMutex.Lock()
	for key, records := range ms.records {
		var filteredRecords []*UsageRecord
		for _, record := range records {
			if record.Timestamp.After(cutoffTime) {
				filteredRecords = append(filteredRecords, record)
			}
		}
		
		if len(filteredRecords) != len(records) {
			ms.records[key] = filteredRecords
			cleanupCount++
		}
		
		// Remove empty record lists
		if len(filteredRecords) == 0 {
			delete(ms.records, key)
		}
	}
	ms.recordsMutex.Unlock()
	
	// Clear stats cache to force recalculation
	ms.statsCacheMutex.Lock()
	ms.statsCache = make(map[string]*UsageStats)
	ms.statsCacheMutex.Unlock()
	
	// Log cleanup activity if any occurred
	if cleanupCount > 0 {
		fmt.Printf("Rate limit cleanup: removed %d expired entries\n", cleanupCount)
	}
	
	return nil
}

// GetAllKeys implements RateLimitStore interface
func (ms *MemoryStore) GetAllKeys() ([]string, error) {
	keySet := make(map[string]bool)
	
	// Collect keys from buckets
	ms.bucketsMutex.RLock()
	for key := range ms.buckets {
		keySet[key] = true
	}
	ms.bucketsMutex.RUnlock()
	
	// Collect keys from records
	ms.recordsMutex.RLock()
	for key := range ms.records {
		keySet[key] = true
	}
	ms.recordsMutex.RUnlock()
	
	// Convert to slice
	keys := make([]string, 0, len(keySet))
	for key := range keySet {
		keys = append(keys, key)
	}
	
	return keys, nil
}

// Close implements RateLimitStore interface
func (ms *MemoryStore) Close() error {
	// Stop the cleanup goroutine
	close(ms.stopCleanup)
	
	// Wait for cleanup to finish
	<-ms.cleanupDone
	
	// Clear all data
	ms.bucketsMutex.Lock()
	ms.buckets = make(map[string]*TokenBucket)
	ms.bucketsMutex.Unlock()
	
	ms.recordsMutex.Lock()
	ms.records = make(map[string][]*UsageRecord)
	ms.recordsMutex.Unlock()
	
	ms.statsCacheMutex.Lock()
	ms.statsCache = make(map[string]*UsageStats)
	ms.statsCacheMutex.Unlock()
	
	return nil
}

// backgroundCleanup runs periodic cleanup in a separate goroutine
func (ms *MemoryStore) backgroundCleanup() {
	ticker := time.NewTicker(ms.cleanupInterval)
	defer ticker.Stop()
	defer close(ms.cleanupDone)
	
	for {
		select {
		case <-ticker.C:
			ms.CleanupExpired()
		case <-ms.stopCleanup:
			return
		}
	}
}

// GetStats returns statistics about the memory store
func (ms *MemoryStore) GetStats() map[string]interface{} {
	ms.bucketsMutex.RLock()
	bucketCount := len(ms.buckets)
	ms.bucketsMutex.RUnlock()
	
	ms.recordsMutex.RLock()
	recordKeyCount := len(ms.records)
	totalRecords := 0
	for _, records := range ms.records {
		totalRecords += len(records)
	}
	ms.recordsMutex.RUnlock()
	
	ms.statsCacheMutex.RLock()
	cacheSize := len(ms.statsCache)
	ms.statsCacheMutex.RUnlock()
	
	return map[string]interface{}{
		"type":              "memory",
		"buckets":           bucketCount,
		"record_keys":       recordKeyCount,
		"total_records":     totalRecords,
		"cache_size":        cacheSize,
		"max_records_per_key": ms.maxRecordsPerKey,
		"cleanup_interval":  ms.cleanupInterval.String(),
	}
}

// GetBucketCount returns the number of active buckets
func (ms *MemoryStore) GetBucketCount() int {
	ms.bucketsMutex.RLock()
	defer ms.bucketsMutex.RUnlock()
	return len(ms.buckets)
}

// GetRecordCount returns the total number of usage records
func (ms *MemoryStore) GetRecordCount() int {
	ms.recordsMutex.RLock()
	defer ms.recordsMutex.RUnlock()
	
	total := 0
	for _, records := range ms.records {
		total += len(records)
	}
	return total
}

// GetCacheSize returns the size of the stats cache
func (ms *MemoryStore) GetCacheSize() int {
	ms.statsCacheMutex.RLock()
	defer ms.statsCacheMutex.RUnlock()
	return len(ms.statsCache)
}

// ClearCache clears the stats cache
func (ms *MemoryStore) ClearCache() {
	ms.statsCacheMutex.Lock()
	defer ms.statsCacheMutex.Unlock()
	ms.statsCache = make(map[string]*UsageStats)
}

// GetMemoryUsage returns an estimate of memory usage (in bytes)
func (ms *MemoryStore) GetMemoryUsage() int64 {
	var totalSize int64
	
	// Estimate bucket storage size
	ms.bucketsMutex.RLock()
	totalSize += int64(len(ms.buckets) * 100) // Rough estimate per bucket
	ms.bucketsMutex.RUnlock()
	
	// Estimate record storage size
	ms.recordsMutex.RLock()
	for _, records := range ms.records {
		totalSize += int64(len(records) * 200) // Rough estimate per record
	}
	ms.recordsMutex.RUnlock()
	
	// Estimate cache size
	ms.statsCacheMutex.RLock()
	totalSize += int64(len(ms.statsCache) * 500) // Rough estimate per stats object
	ms.statsCacheMutex.RUnlock()
	
	return totalSize
}

// ForceCleanup forces an immediate cleanup of expired data
func (ms *MemoryStore) ForceCleanup() error {
	return ms.CleanupExpired()
}

// SetMaxRecordsPerKey sets the maximum number of records to keep per key
func (ms *MemoryStore) SetMaxRecordsPerKey(maxRecords int) {
	ms.maxRecordsPerKey = maxRecords
}

// SetCleanupInterval sets the cleanup interval
func (ms *MemoryStore) SetCleanupInterval(interval time.Duration) {
	ms.cleanupInterval = interval
} 
