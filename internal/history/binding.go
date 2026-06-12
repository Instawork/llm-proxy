package history

import "sync"

// Binding is embedded by metric recorders to emit per-event row history.
// Zero value is a no-op until BindHistory is called.
type Binding struct {
	mu     sync.RWMutex
	sink   *Sink
	stream string
}

// BindHistory attaches the shared sink and stream name. Intended to be called
// once at startup before traffic.
func (b *Binding) BindHistory(sink *Sink, stream string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.sink, b.stream = sink, stream
	b.mu.Unlock()
}

// EmitHistory archives one raw event. No-op when unbound or sink is nil.
func (b *Binding) EmitHistory(event any) {
	if b == nil {
		return
	}
	b.mu.RLock()
	sink, stream := b.sink, b.stream
	b.mu.RUnlock()
	if sink == nil {
		return
	}
	sink.Emit(stream, event)
}
