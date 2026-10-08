package middleware

import (
	"bytes"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// captureStdLog redirects the standard logger for the duration of fn and
// returns everything it wrote.
func captureStdLog(fn func()) string {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()
	fn()
	return buf.String()
}

func newStreamingCapture(chunkTrace bool) *responseCapture {
	return &responseCapture{
		ResponseWriter: httptest.NewRecorder(),
		body:           &bytes.Buffer{},
		isStreaming:    true,
		requestStart:   time.Now(),
		chunkTrace:     chunkTrace,
	}
}

func TestResponseCapture_ChunkTraceOff_NoPerChunkLogButSummaryHistogramKept(t *testing.T) {
	rc := newStreamingCapture(false)
	out := captureStdLog(func() {
		for range 50 {
			_, _ = rc.Write([]byte("event: content_block_delta\ndata: {\"type\":\"text_delta\",\"text\":\"x\"}\n\n"))
		}
	})
	assert.NotContains(t, out, "📡", "per-chunk trace must be silent at INFO")
	assert.NotContains(t, out, "streaming stall", "no stall occurred")
	assert.Equal(t, int64(50), rc.chunkCount)
	// The end-of-stream summary still sees the cumulative histogram.
	assert.Equal(t, int64(50), rc.sseEventCounts["content_block_delta"])
	assert.Equal(t, int64(50), rc.sseDataTypes["text_delta"])
	assert.Contains(t, rc.formatEventCounts(), "event:content_block_delta=50")
}

func TestResponseCapture_ChunkTraceOn_LogsEveryChunk(t *testing.T) {
	rc := newStreamingCapture(true)
	out := captureStdLog(func() {
		for range 3 {
			_, _ = rc.Write([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"))
		}
	})
	assert.Equal(t, 3, strings.Count(out, "📡"))
	assert.Contains(t, out, "types=event:ping,type:ping")
}

func TestResponseCapture_StallWarningSurvivesAtInfo(t *testing.T) {
	rc := newStreamingCapture(false)
	_, _ = rc.Write([]byte("event: message_start\n\n"))
	// Simulate a long upstream gap without sleeping.
	rc.lastChunkAt = time.Now().Add(-(streamStallThreshold + time.Second))
	out := captureStdLog(func() {
		_, _ = rc.Write([]byte("event: ping\n\n"))
	})
	assert.Contains(t, out, "⚠ streaming stall")
	assert.Contains(t, out, "before chunk #2")
	assert.NotContains(t, out, "📡")
}

func TestSniffSSE_NoPerChunkMapWhenNotWanted(t *testing.T) {
	rc := &responseCapture{}
	got := rc.sniffSSE([]byte("event: ping\ndata: {\"type\":\"ping\"}\n"), false)
	assert.Nil(t, got)
	assert.Equal(t, int64(1), rc.sseEventCounts["ping"])
	assert.Equal(t, int64(1), rc.sseDataTypes["ping"])

	chunk := []byte("event: ping\ndata: {\"type\":\"ping\"}\n")
	allocs := testing.AllocsPerRun(100, func() {
		rc.sniffSSE(chunk, false)
	})
	assert.Zero(t, allocs, "cumulative-only sniff must not allocate per chunk (got %v allocs/op)", allocs)
}
