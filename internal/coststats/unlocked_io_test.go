package coststats

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/history"
)

// parkedWriter blocks the first chunk upload until release is closed (or the
// upload context expires), standing in for an S3 endpoint that has stopped
// answering; later uploads succeed immediately so only the first caller is
// ever stuck in the sink itself.
type parkedWriter struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (w *parkedWriter) WriteChunk(ctx context.Context, _, _ string, _ []byte) error {
	if w.calls.Add(1) != 1 {
		return nil
	}
	w.entered <- struct{}{}
	select {
	case <-w.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A history upload that stalls must only stall the request that tripped it,
// never every other RecordRequest in the process: EmitHistory has to run
// after r.mu is released.
func TestRecordRequest_StalledHistoryUploadDoesNotHoldRecorderLock(t *testing.T) {
	w := &parkedWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	// MaxRecords: 1 makes every Emit upload inline.
	sink := history.NewWithWriter(w, history.Config{MaxRecords: 1, MaxAge: time.Hour})
	t.Cleanup(func() {
		close(w.release)
		_ = sink.Close()
	})

	r := NewRecorder()
	r.BindHistory(sink, "cost")

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		r.RecordRequest("openai", "iw:stalled", "", "gpt-4o-mini", 0.01, 0.006, 0.004, 10, 5)
	}()

	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first RecordRequest never reached the history upload")
	}

	// While the first caller is parked inside the upload, a second caller
	// must still be able to record and a reader must still get a snapshot.
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		r.RecordRequest("openai", "iw:free", "", "gpt-4o-mini", 0.02, 0.012, 0.008, 20, 10)
	}()
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second RecordRequest blocked behind a stalled history upload: r.mu is held across EmitHistory")
	}
	select {
	case <-firstDone:
		t.Fatal("first RecordRequest should still be parked in the upload")
	default:
	}

	snap := r.Snapshot()
	if got := snap["requests_today"].(int64); got != 2 {
		t.Fatalf("requests_today = %d, want 2 (both requests counted before the upload finished)", got)
	}
	if got := snap["spend_today_usd"].(float64); got != 0.03 {
		t.Fatalf("spend_today_usd = %v, want 0.03", got)
	}
}
