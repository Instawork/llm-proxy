package coststats

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/history"
)

func TestRecorder_EmitsHistoryOnRequest(t *testing.T) {
	s, w := history.NewRecordingSink(history.Config{
		MaxRecords: 1000,
		MaxBytes:   1 << 30,
		MaxAge:     time.Hour,
		Gzip:       false,
		GzipSet:    true,
	})

	r := NewRecorder()
	r.BindHistory(s, history.StreamCost)
	r.RecordRequest("openai", "iw:abc", "chat", "gpt-4o-mini", 0.01, 0.006, 0.004, 100, 50)
	require.NoError(t, s.Close())
	require.Len(t, w.Chunks, 1)
	require.Equal(t, history.StreamCost, w.Chunks[0].Stream)
	require.Contains(t, string(w.Chunks[0].Body), `"provider":"openai"`)
	require.Contains(t, string(w.Chunks[0].Body), `"gpt-4o-mini"`)
}

func TestRecorder_HistoryUnboundNoChunks(t *testing.T) {
	s, w := history.NewRecordingSink(history.Config{
		MaxRecords: 1000,
		MaxBytes:   1 << 30,
		MaxAge:     time.Hour,
		Gzip:       false,
		GzipSet:    true,
	})
	r := NewRecorder()
	r.RecordRequest("openai", "iw:abc", "chat", "gpt-4o-mini", 0.01, 0.006, 0.004, 100, 50)
	require.NoError(t, s.Close())
	require.Empty(t, w.Chunks)
}

func TestRecorder_HistoryStreamName(t *testing.T) {
	s, w := history.NewRecordingSink(history.Config{
		MaxRecords: 1,
		MaxBytes:   1 << 30,
		MaxAge:     time.Hour,
		Gzip:       false,
		GzipSet:    true,
	})
	r := NewRecorder()
	r.BindHistory(s, history.StreamCost)
	r.RecordRequest("openai", "k", "", "m", 1, 0.6, 0.4, 1, 1)
	require.Len(t, w.Chunks, 1)
	require.True(t, strings.HasSuffix(w.Chunks[0].ObjectName, ".jsonl"))
}
