package ratelimit

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
)

// memoryLimiter is a thread-safe in-memory rate limiter.
type memoryLimiter struct {
	cfg          *config.YAMLConfig
	mu           sync.Mutex
	minute       map[string]*counters
	day          map[string]*counters
	minTick      time.Time
	dayTick      time.Time
	perKeyLookup PerKeyOverrideFunc
}

// SetPerKeyOverride installs a dynamic per-key override source (e.g. backed by
// the API-key store). Safe to call once at startup before serving traffic.
func (m *memoryLimiter) SetPerKeyOverride(fn PerKeyOverrideFunc) {
	m.perKeyLookup = fn
}

type counters struct {
	Requests int
	Tokens   int
}

func NewMemoryLimiter(cfg *config.YAMLConfig) RateLimiter {
	return &memoryLimiter{
		cfg:     cfg,
		minute:  make(map[string]*counters),
		day:     make(map[string]*counters),
		minTick: time.Now().Truncate(time.Minute),
		dayTick: time.Now().Truncate(24 * time.Hour),
	}
}

func (m *memoryLimiter) CheckAndReserve(ctx context.Context, id string, scope ScopeKeys, estTokens int, now time.Time) (ReservationResult, error) {
	if err := ctx.Err(); err != nil {
		return ReservationResult{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.rotateWindowsLocked(now)

	keys := m.scopeKeys(scope)
	for _, k := range keys {
		// minute window
		minC := m.getCounterLocked(m.minute, k)
		minLim := m.limitFor(k, true)
		if m.exceeds(minC, minLim, estTokens) {
			// Derive Limit and Remaining from the metric that actually
			// tripped, so the X-RateLimit-* headers are internally consistent
			// (matches the Redis script's per-check computation).
			metric := exceededMetric(minC, minLim, estTokens)
			remaining, limitVal := 0, 0
			if metric == "tokens" {
				limitVal = minLim.tokPerWindow
				remaining = max0(minLim.tokPerWindow - (minC.Tokens + estTokens))
			} else {
				limitVal = minLim.reqPerWindow
				remaining = max0(minLim.reqPerWindow - (minC.Requests + 1))
			}
			details := &LimitDetails{ScopeKey: k, Metric: metric, Window: "minute", Limit: limitVal, Remaining: remaining}
			// Retry-After is the true time to the window boundary, matching
			// the Redis backend, not a flat 60 (a denial at second 55 can
			// retry in 5s).
			return ReservationResult{Allowed: false, RetryAfterSeconds: secToMinuteEnd(now), Reason: "minute limit exceeded", Details: details}, nil
		}
		// day window
		dayC := m.getCounterLocked(m.day, k)
		dayLim := m.limitFor(k, false)
		if m.exceeds(dayC, dayLim, estTokens) {
			metric := exceededMetric(dayC, dayLim, estTokens)
			remaining, limitVal := 0, 0
			if metric == "tokens" {
				limitVal = dayLim.tokPerWindow
				remaining = max0(dayLim.tokPerWindow - (dayC.Tokens + estTokens))
			} else {
				limitVal = dayLim.reqPerWindow
				remaining = max0(dayLim.reqPerWindow - (dayC.Requests + 1))
			}
			details := &LimitDetails{ScopeKey: k, Metric: metric, Window: "day", Limit: limitVal, Remaining: remaining}
			return ReservationResult{Allowed: false, RetryAfterSeconds: int(time.Until(m.dayTick.Add(24 * time.Hour)).Seconds()), Reason: "daily limit exceeded", Details: details}, nil
		}
	}

	// Apply reservation
	for _, k := range keys {
		m.getCounterLocked(m.minute, k).Requests++
		m.getCounterLocked(m.minute, k).Tokens += estTokens
		m.getCounterLocked(m.day, k).Requests++
		m.getCounterLocked(m.day, k).Tokens += estTokens
	}

	return ReservationResult{Allowed: true, ReservationID: id}, nil
}

func (m *memoryLimiter) Adjust(ctx context.Context, id string, scope ScopeKeys, tokenDelta int, reservedAt, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.rotateWindowsLocked(now)
	// Only touch windows the reservation was actually made in; if a window
	// rotated between reserve and reconcile, the reservation vanished with it
	// and applying the delta here would mutate other requests' counters.
	sameMin := reservedAt.Truncate(time.Minute).Equal(m.minTick)
	sameDay := reservedAt.Truncate(24 * time.Hour).Equal(m.dayTick)
	keys := m.scopeKeys(scope)
	for _, k := range keys {
		if sameMin {
			c := m.getCounterLocked(m.minute, k)
			// Clamp like the Redis script does: a large negative delta must
			// not drive the counter negative and grant free capacity.
			c.Tokens = max0(c.Tokens + tokenDelta)
		}
		if sameDay {
			c := m.getCounterLocked(m.day, k)
			c.Tokens = max0(c.Tokens + tokenDelta)
		}
	}
	return nil
}

// Cancel undoes a prior reservation. estTokens MUST mirror what was passed
// to CheckAndReserve for this reservation; otherwise the reserved tokens
// remain in the window and silently under-credit the limit.
func (m *memoryLimiter) Cancel(ctx context.Context, id string, scope ScopeKeys, estTokens int, reservedAt, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.rotateWindowsLocked(now)
	sameMin := reservedAt.Truncate(time.Minute).Equal(m.minTick)
	sameDay := reservedAt.Truncate(24 * time.Hour).Equal(m.dayTick)
	keys := m.scopeKeys(scope)
	for _, k := range keys {
		if sameMin {
			minC := m.getCounterLocked(m.minute, k)
			minC.Requests = max0(minC.Requests - 1)
			minC.Tokens = max0(minC.Tokens - estTokens)
		}
		if sameDay {
			dayC := m.getCounterLocked(m.day, k)
			dayC.Requests = max0(dayC.Requests - 1)
			dayC.Tokens = max0(dayC.Tokens - estTokens)
		}
	}
	return nil
}

// Snapshot returns configured limits and current in-memory counter values.
func (m *memoryLimiter) Snapshot(now time.Time) LimitsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.rotateWindowsLocked(now)

	rl := m.cfg.Features.RateLimiting
	snap := LimitsSnapshot{
		Enabled:   rl.Enabled,
		Backend:   rl.Backend,
		Limits:    rl.Limits,
		Overrides: rl.Overrides,
		Minute: &WindowSnapshot{
			WindowStart: m.minTick.Format(time.RFC3339),
			Counters:    copyCounterMap(m.minute),
		},
		Day: &WindowSnapshot{
			WindowStart: m.dayTick.Format(time.RFC3339),
			Counters:    copyCounterMap(m.day),
		},
	}
	if snap.Backend == "" {
		snap.Backend = "memory"
	}
	return snap
}

func copyCounterMap(src map[string]*counters) map[string]CounterSnapshot {
	out := make(map[string]CounterSnapshot, len(src))
	for k, c := range src {
		if c == nil {
			continue
		}
		out[k] = CounterSnapshot{Requests: c.Requests, Tokens: c.Tokens}
	}
	return out
}

func (m *memoryLimiter) rotateWindowsLocked(now time.Time) {
	min := now.Truncate(time.Minute)
	if !min.Equal(m.minTick) {
		m.minute = make(map[string]*counters)
		m.minTick = min
	}
	day := now.Truncate(24 * time.Hour)
	if !day.Equal(m.dayTick) {
		m.day = make(map[string]*counters)
		m.dayTick = day
	}
}

func (m *memoryLimiter) getCounterLocked(bucket map[string]*counters, key string) *counters {
	if c, ok := bucket[key]; ok {
		return c
	}
	c := &counters{}
	bucket[key] = c
	return c
}

func (m *memoryLimiter) exceeds(c *counters, lim limits, addTokens int) bool {
	if lim.reqPerWindow > 0 && c.Requests+1 > lim.reqPerWindow {
		return true
	}
	if lim.tokPerWindow > 0 {
		// Allow the first token-bearing request in the window optimistically.
		if c.Tokens == 0 {
			// Skip token exceed check for the first request in this window.
		} else if c.Tokens+addTokens > lim.tokPerWindow {
			return true
		}
	}
	return false
}

type limits struct {
	reqPerWindow int
	tokPerWindow int
}

func (m *memoryLimiter) limitFor(key string, minute bool) limits {
	base := m.cfg.Features.RateLimiting.Limits
	lim := limits{}
	if minute {
		lim.reqPerWindow = base.RequestsPerMinute
		lim.tokPerWindow = base.TokensPerMinute
	} else {
		lim.reqPerWindow = base.RequestsPerDay
		lim.tokPerWindow = base.TokensPerDay
	}
	// Apply overrides by key namespace
	overrides := m.cfg.Features.RateLimiting.Overrides
	if strings.HasPrefix(key, "model:") {
		name := strings.TrimPrefix(key, "model:")
		if o, ok := overrides.PerModel[name]; ok {
			if minute {
				if o.RequestsPerMinute > 0 {
					lim.reqPerWindow = o.RequestsPerMinute
				}
				if o.TokensPerMinute > 0 {
					lim.tokPerWindow = o.TokensPerMinute
				}
			} else {
				if o.RequestsPerDay > 0 {
					lim.reqPerWindow = o.RequestsPerDay
				}
				if o.TokensPerDay > 0 {
					lim.tokPerWindow = o.TokensPerDay
				}
			}
		}
	} else if strings.HasPrefix(key, "key:") {
		id := strings.TrimPrefix(key, "key:")
		o, ok := overrides.PerKey[id]
		// Dynamic per-key overrides (API-key record) take precedence over
		// static YAML overrides when present.
		if m.perKeyLookup != nil {
			if dyn, found := m.perKeyLookup(id); found {
				o, ok = dyn, true
			}
		}
		if ok {
			if minute {
				if o.RequestsPerMinute > 0 {
					lim.reqPerWindow = o.RequestsPerMinute
				}
				if o.TokensPerMinute > 0 {
					lim.tokPerWindow = o.TokensPerMinute
				}
			} else {
				if o.RequestsPerDay > 0 {
					lim.reqPerWindow = o.RequestsPerDay
				}
				if o.TokensPerDay > 0 {
					lim.tokPerWindow = o.TokensPerDay
				}
			}
		}
	} else if strings.HasPrefix(key, "user:") {
		id := strings.TrimPrefix(key, "user:")
		if o, ok := overrides.PerUser[id]; ok {
			if minute {
				if o.RequestsPerMinute > 0 {
					lim.reqPerWindow = o.RequestsPerMinute
				}
				if o.TokensPerMinute > 0 {
					lim.tokPerWindow = o.TokensPerMinute
				}
			} else {
				if o.RequestsPerDay > 0 {
					lim.reqPerWindow = o.RequestsPerDay
				}
				if o.TokensPerDay > 0 {
					lim.tokPerWindow = o.TokensPerDay
				}
			}
		}
	}
	return lim
}

func (m *memoryLimiter) scopeKeys(scope ScopeKeys) []string {
	// We track separate counters for each scope dimension such that any
	// configured limit can apply independently. Use plain strings.
	keys := []string{"global"}
	if scope.Provider != "" {
		keys = append(keys, "provider:"+scope.Provider)
	}
	if scope.Model != "" {
		keys = append(keys, "model:"+scope.Model)
	}
	if scope.APIKey != "" {
		keys = append(keys, "key:"+scope.APIKey)
	}
	if scope.UserID != "" {
		keys = append(keys, "user:"+scope.UserID)
	}
	return keys
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func exceededMetric(c *counters, lim limits, addTokens int) string {
	if lim.tokPerWindow > 0 && c.Tokens+addTokens > lim.tokPerWindow {
		return "tokens"
	}
	return "requests"
}
