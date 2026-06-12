package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/history"
)

func TestCloseGlobalHistorySinkFlushesBufferedEvents(t *testing.T) {
	prev := globalHistorySink
	t.Cleanup(func() { globalHistorySink = prev })

	s, w := history.NewRecordingSink(history.Config{
		MaxRecords: 1000,
		MaxBytes:   1 << 30,
		MaxAge:     time.Hour,
		Gzip:       false,
		GzipSet:    true,
	})
	globalHistorySink = s
	s.Emit(history.StreamCost, map[string]any{"shutdown": true})

	require.NoError(t, closeGlobalHistorySink())
	require.Len(t, w.Chunks, 1)
	require.Contains(t, string(w.Chunks[0].Body), `"shutdown":true`)
}

func TestCloseGlobalHistorySinkNilSafe(t *testing.T) {
	prev := globalHistorySink
	t.Cleanup(func() { globalHistorySink = prev })
	globalHistorySink = nil
	require.NoError(t, closeGlobalHistorySink())
}
