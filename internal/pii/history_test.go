package pii

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/history"
)

func TestRecorder_EmitsHistoryOnRedaction(t *testing.T) {
	s, w := history.NewRecordingSink(history.Config{
		MaxRecords: 1000,
		MaxBytes:   1 << 30,
		MaxAge:     time.Hour,
		Gzip:       false,
		GzipSet:    true,
	})
	r := NewRecorder()
	r.BindHistory(s, history.StreamPII)
	r.RecordRedaction("openai", "iw:abc", map[string]int{"EMAIL_ADDRESS": 1}, 100, time.Millisecond, OutcomeOK)
	require.NoError(t, s.Close())
	require.Len(t, w.Chunks, 1)
	require.Equal(t, history.StreamPII, w.Chunks[0].Stream)
	require.Contains(t, string(w.Chunks[0].Body), `"EMAIL_ADDRESS"`)
}
