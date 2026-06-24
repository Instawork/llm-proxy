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

	mu           sync.Mutex
	timers       map[string]*time.Timer
	pending      map[string]map[string]interface{}
	deltaTimers  map[string]*time.Timer
	deltaPending map[string]Delta
}

// NewPersister returns a persister for one metric family (cost, pii, …).
func NewPersister(store *Store, metric string) *Persister {
	if store == nil {
		return nil
	}
	return &Persister{
		store:        store,
		metric:       metric,
		timers:       make(map[string]*time.Timer),
		pending:      make(map[string]map[string]interface{}),
		deltaTimers:  make(map[string]*time.Timer),
		deltaPending: make(map[string]Delta),
	}
}

// QueueToday schedules a debounced write of today's snapshot (legacy JSON path).
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

// QueueDelta schedules a debounced atomic merge of today's additive delta.
func (p *Persister) QueueDelta(day string, d Delta) {
	if p == nil || p.store == nil || d.empty() {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.deltaPending == nil {
		p.deltaPending = make(map[string]Delta)
	}
	acc := p.deltaPending[day]
	acc = mergeDelta(acc, d)
	p.deltaPending[day] = acc
	if t, ok := p.deltaTimers[day]; ok {
		t.Stop()
	}
	p.deltaTimers[day] = time.AfterFunc(persistDebounce, func() {
		p.flushDeltaDay(day)
	})
}

func mergeDelta(acc, d Delta) Delta {
	if acc.Totals == nil {
		acc.Totals = make(map[string]float64)
	}
	for k, v := range d.Totals {
		acc.Totals[k] += v
	}
	if acc.Dimensions == nil {
		acc.Dimensions = make(map[string]map[string]float64)
	}
	for dim, members := range d.Dimensions {
		if acc.Dimensions[dim] == nil {
			acc.Dimensions[dim] = make(map[string]float64)
		}
		for m, v := range members {
			acc.Dimensions[dim][m] += v
		}
	}
	return acc
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

func (p *Persister) flushDeltaDay(day string) {
	p.mu.Lock()
	d := p.deltaPending[day]
	delete(p.deltaPending, day)
	delete(p.deltaTimers, day)
	p.mu.Unlock()
	if d.empty() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.store.ApplyDelta(ctx, p.metric, day, d); err != nil {
		p.store.logger.Warn("admin rollup: apply delta failed", "metric", p.metric, "day", day, "error", err)
	}
	now := time.Now().UTC()
	if len(d.Totals) > 0 && day == now.Format("2006-01-02") {
		if err := p.store.ApplyHourlyTotals(ctx, p.metric, day, now.Hour(), d.Totals); err != nil {
			p.store.logger.Warn("admin rollup: apply hourly totals failed", "metric", p.metric, "day", day, "error", err)
		}
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
	days := make([]string, 0, len(p.pending)+len(p.deltaPending))
	for d := range p.pending {
		days = append(days, d)
	}
	for d := range p.deltaPending {
		days = append(days, d)
	}
	p.mu.Unlock()
	seen := make(map[string]struct{}, len(days))
	for _, d := range days {
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		p.flushDeltaDay(d)
		p.flushDay(d)
	}
}
