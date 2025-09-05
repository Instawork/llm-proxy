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
	cfg     *config.YAMLConfig
	mu      sync.Mutex
	minute  map[string]*counters
	day     map[string]*counters
	minTick time.Time
	dayTick time.Time
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
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()

	m.rotateWindowsLocked(now)

	keys := m.scopeKeys(scope)
	for _, k := range keys {
		// minute window
		minC := m.getCounterLocked(m.minute, k)
		minLim := m.limitFor(k, true)
		if m.exceeds(minC, minLim, estTokens) {
			remaining := 0
			if minLim.tokPerWindow > 0 {
				remaining = max0(minLim.tokPerWindow - (minC.Tokens + estTokens))
			} else if minLim.reqPerWindow > 0 {
				remaining = max0(minLim.reqPerWindow - (minC.Requests + 1))
			}
			details := &LimitDetails{ScopeKey: k, Metric: exceededMetric(minC, minLim, estTokens), Window: "minute", Limit: maxInt(minLim.reqPerWindow, minLim.tokPerWindow), Remaining: remaining}
			return ReservationResult{Allowed: false, RetryAfterSeconds: 60, Reason: "minute limit exceeded", Details: details}, nil
		}
		// day window
		dayC := m.getCounterLocked(m.day, k)
		dayLim := m.limitFor(k, false)
		if m.exceeds(dayC, dayLim, estTokens) {
			remaining := 0
			if dayLim.tokPerWindow > 0 {
				remaining = max0(dayLim.tokPerWindow - (dayC.Tokens + estTokens))
			} else if dayLim.reqPerWindow > 0 {
				remaining = max0(dayLim.reqPerWindow - (dayC.Requests + 1))
			}
			details := &LimitDetails{ScopeKey: k, Metric: exceededMetric(dayC, dayLim, estTokens), Window: "day", Limit: maxInt(dayLim.reqPerWindow, dayLim.tokPerWindow), Remaining: remaining}
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

func (m *memoryLimiter) Adjust(ctx context.Context, id string, scope ScopeKeys, tokenDelta int, now time.Time) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()

	m.rotateWindowsLocked(now)
	keys := m.scopeKeys(scope)
	for _, k := range keys {
		m.getCounterLocked(m.minute, k).Tokens += tokenDelta
		m.getCounterLocked(m.day, k).Tokens += tokenDelta
	}
	return nil
}

func (m *memoryLimiter) Cancel(ctx context.Context, id string, scope ScopeKeys, now time.Time) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()

	m.rotateWindowsLocked(now)
	keys := m.scopeKeys(scope)
	for _, k := range keys {
		m.getCounterLocked(m.minute, k).Requests--
		m.getCounterLocked(m.minute, k).Tokens = max0(m.getCounterLocked(m.minute, k).Tokens)
		m.getCounterLocked(m.day, k).Requests--
		m.getCounterLocked(m.day, k).Tokens = max0(m.getCounterLocked(m.day, k).Tokens)
	}
	return nil
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
	if lim.tokPerWindow > 0 && c.Tokens+addTokens > lim.tokPerWindow {
		return true
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
		if o, ok := overrides.PerKey[id]; ok {
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
