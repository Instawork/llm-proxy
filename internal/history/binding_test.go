package history

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type captureWriter struct {
	mu    sync.Mutex
	calls []string
}

func (c *captureWriter) WriteChunk(_ context.Context, stream, objectName string, _ []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, stream+":"+objectName)
	return nil
}

func TestBinding_EmitHistory(t *testing.T) {
	w := &captureWriter{}
	s := newTestSink(1000, w)
	var b Binding
	b.BindHistory(s, StreamCost)
	b.EmitHistory(map[string]any{"ok": true})
	require.NoError(t, s.Close())
	require.Len(t, w.calls, 1)
	require.Contains(t, w.calls[0], "cost:")
}

func TestBinding_UnboundNoOp(t *testing.T) {
	var b Binding
	b.EmitHistory(map[string]any{"x": 1})
}

func TestBinding_NilReceiver(t *testing.T) {
	var b *Binding
	b.BindHistory(nil, StreamCost)
	b.EmitHistory(map[string]any{"x": 1})
}
