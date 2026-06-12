package ratelimitstats

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/history"
)

func TestRecordDecision_NilRecorder(t *testing.T) {
	var r *Recorder
	r.RecordDecision("openai", "", "", "", false, "rpm", "requests", "minute", "k", 1, 0)
}

func TestSnapshot_Nil(t *testing.T) {
	var r *Recorder
	snap := r.Snapshot()
	require.False(t, snap["available"].(bool))
}

func TestRecordDecision_AllowedDoesNotEmitHistory(t *testing.T) {
	s, w := history.NewRecordingSink(history.Config{
		MaxRecords: 1000,
		MaxBytes:   1 << 30,
		MaxAge:     time.Hour,
		Gzip:       false,
		GzipSet:    true,
	})
	r := NewRecorder()
	r.BindHistory(s, history.StreamRateLimit)
	r.RecordDecision("openai", "gpt-4o", "iw:abc", "user-1", true, "", "rpm", "minute", "k", 100, 99)
	require.NoError(t, s.Close())
	require.Empty(t, w.Chunks)
}

func TestRecordDecision_BlockedEmitsHistory(t *testing.T) {
	s, w := history.NewRecordingSink(history.Config{
		MaxRecords: 1000,
		MaxBytes:   1 << 30,
		MaxAge:     time.Hour,
		Gzip:       false,
		GzipSet:    true,
	})
	r := NewRecorder()
	r.BindHistory(s, history.StreamRateLimit)
	r.RecordDecision("openai", "gpt-4o", "iw:abc", "user-1", false, "rpm", "requests", "minute", "k", 1, 0)
	require.NoError(t, s.Close())
	require.Len(t, w.Chunks, 1)
	require.Equal(t, history.StreamRateLimit, w.Chunks[0].Stream)
	require.Contains(t, string(w.Chunks[0].Body), `"reason":"rpm"`)
}

func TestRecordDecision_MemoryAggregatesAllDecisions(t *testing.T) {
	r := NewRecorder()
	r.RecordDecision("openai", "", "", "", true, "", "rpm", "minute", "k", 10, 9)
	r.RecordDecision("openai", "", "", "", false, "rpm", "requests", "minute", "k", 10, 0)
	snap := r.Snapshot()
	require.Equal(t, int64(2), snap["requests_total"])
	require.Equal(t, int64(1), snap["requests_allowed"])
	require.Equal(t, int64(1), snap["requests_blocked"])
}
