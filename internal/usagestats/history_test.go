package usagestats

import (
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
	r.BindHistory(s, history.StreamUsage)
	r.RecordRequest("openai", "gpt-4o-mini", "iw:abc", "user-1", 100, 50)
	require.NoError(t, s.Close())
	require.Len(t, w.Chunks, 1)
	require.Equal(t, history.StreamUsage, w.Chunks[0].Stream)
	require.Contains(t, string(w.Chunks[0].Body), `"gpt-4o-mini"`)
}
