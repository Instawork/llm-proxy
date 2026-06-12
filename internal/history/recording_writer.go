package history

import (
	"context"
	"sync"
)

// RecordedChunk is one flushed history chunk captured by RecordingWriter.
type RecordedChunk struct {
	Stream     string
	ObjectName string
	Body       []byte
}

// RecordingWriter is a chunkWriter that captures writes for tests.
type RecordingWriter struct {
	mu     sync.Mutex
	Chunks []RecordedChunk
}

// WriteChunk implements chunkWriter for test capture.
func (r *RecordingWriter) WriteChunk(_ context.Context, stream, objectName string, body []byte) error {
	b := make([]byte, len(body))
	copy(b, body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Chunks = append(r.Chunks, RecordedChunk{
		Stream:     stream,
		ObjectName: objectName,
		Body:       b,
	})
	return nil
}

// NewRecordingSink builds a sink wired to a RecordingWriter.
func NewRecordingSink(cfg Config) (*Sink, *RecordingWriter) {
	w := &RecordingWriter{}
	return NewWithWriter(w, cfg), w
}
