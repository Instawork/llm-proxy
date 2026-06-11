package adminrollup

import (
	"context"
	"sync"
	"time"
)

const persistDebounce = 3 * time.Second

// Persister debounces Redis writes for a metric/day pair.
type Persister struct {
	store  *Store
	metric string

	mu      sync.Mutex
	timers  map[string]*time.Timer
	pending map[string]map[string]interface{}
}

// NewPersister returns a persister for one metric family (cost, pii, …).
func NewPersister(store *Store, metric string) *Persister {
	if store == nil {
		return nil
	}
	return &Persister{
		store:   store,
		metric:  metric,
		timers:  make(map[string]*time.Timer),
		pending: make(map[string]map[string]interface{}),
	}
}

// QueueToday schedules a debounced write of today's snapshot.
func (p *Persister) QueueToday(day string, data map[string]interface{}) {
	if p == nil || p.store == nil || data == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending[day] = data
	if t, ok := p.timers[day]; ok {
		t.Stop()
	}
	p.timers[day] = time.AfterFunc(persistDebounce, func() {
		p.flushDay(day)
	})
}

// ArchiveImmediately writes the completed day to the daily key (day rollover).
func (p *Persister) ArchiveImmediately(day string, data map[string]interface{}) {
	if p == nil || p.store == nil || data == nil {
		return
	}
	p.mu.Lock()
	if t, ok := p.timers[day]; ok {
		t.Stop()
		delete(p.timers, day)
	}
	delete(p.pending, day)
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.store.ArchiveDaily(ctx, p.metric, day, data); err != nil {
		p.store.logger.Warn("admin rollup: archive failed", "metric", p.metric, "day", day, "error", err)
	}
}

func (p *Persister) flushDay(day string) {
	p.mu.Lock()
	data := p.pending[day]
	delete(p.pending, day)
	delete(p.timers, day)
	p.mu.Unlock()
	if data == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.store.SaveToday(ctx, p.metric, day, data); err != nil {
		p.store.logger.Warn("admin rollup: save today failed", "metric", p.metric, "day", day, "error", err)
	}
}

// FlushNow forces all pending today writes (for shutdown).
func (p *Persister) FlushNow() {
	if p == nil {
		return
	}
	p.mu.Lock()
	days := make([]string, 0, len(p.pending))
	for d := range p.pending {
		days = append(days, d)
	}
	p.mu.Unlock()
	for _, d := range days {
		p.flushDay(d)
	}
}
