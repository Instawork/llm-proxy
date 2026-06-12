// Package adminrollup persists admin-dashboard daily metrics to Redis so
// charts and stat cards survive process restarts. Keys live under llm:admin:*
// on a dedicated Redis DB (default 6) separate from circuit breaker (5) and
// rate limiting.
package adminrollup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	redis "github.com/redis/go-redis/v9"
)

const (
	MetricCost      = "cost"
	MetricPII       = "pii"
	MetricUsage     = "usage"
	MetricCircuit   = "circuit"
	MetricRateLimit = "ratelimit"

	keyPrefix = "llm:admin:"

	// todayTTL bounds the lifetime of an in-progress "today" key. It is far
	// longer than a UTC day so a live day is always rewritten well before it
	// expires, but it guarantees an orphaned today key (e.g. the proxy
	// restarts across midnight before the day-rollover archive runs) self-
	// expires instead of leaking forever.
	todayTTL = 48 * time.Hour
)

// DayRecord is one UTC calendar day of rolled-up stats (stored in Redis).
type DayRecord struct {
	Day  string                 `json:"day"`
	Data map[string]interface{} `json:"data"`
}

// Store writes and reads daily admin rollups through a pluggable backend
// (Redis for durability, in-memory for local dev / tests).
type Store struct {
	be            backend
	logger        *slog.Logger
	retentionDays int
	historyDays   int
}

// Config mirrors admin_dashboard.rollups YAML.
type Config struct {
	Enabled bool
	// Backend selects "redis" or "memory"; empty infers from Redis presence.
	Backend       string
	Redis         *config.RedisConfig
	RetentionDays int
	HistoryDays   int
	Logger        *slog.Logger
}

// NewStore builds the configured backend (PINGing Redis when selected).
// Returns nil, nil when Enabled is false.
func NewStore(cfg Config) (*Store, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	be, err := newBackend(cfg)
	if err != nil {
		return nil, err
	}

	retention := cfg.RetentionDays
	if retention <= 0 {
		retention = 90
	}
	history := cfg.HistoryDays
	if history <= 0 {
		history = 30
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Store{
		be:            be,
		logger:        logger,
		retentionDays: retention,
		historyDays:   history,
	}, nil
}

// Backend reports the active backend kind ("redis" or "memory").
func (s *Store) Backend() string {
	if s == nil || s.be == nil {
		return ""
	}
	return s.be.kind()
}

// Close releases backend resources.
func (s *Store) Close() error {
	if s == nil || s.be == nil {
		return nil
	}
	return s.be.close()
}

// HistoryDays returns how many prior UTC days LoadHistory includes.
func (s *Store) HistoryDays() int {
	if s == nil {
		return 0
	}
	return s.historyDays
}

func dailyKey(metric, day string) string {
	return fmt.Sprintf("%s%s:daily:%s", keyPrefix, metric, day)
}

func todayKey(metric, day string) string {
	return fmt.Sprintf("%s%s:today:%s", keyPrefix, metric, day)
}

// SaveToday writes the in-progress UTC day blob with a bounded TTL so an
// orphaned today key (restart across midnight) self-expires; a live day is
// rewritten long before todayTTL elapses.
func (s *Store) SaveToday(ctx context.Context, metric, day string, data map[string]interface{}) error {
	if s == nil {
		return nil
	}
	payload, err := json.Marshal(DayRecord{Day: day, Data: data})
	if err != nil {
		return err
	}
	return s.be.set(ctx, todayKey(metric, day), payload, todayTTL)
}

// ArchiveDaily copies a completed day to the daily key with retention TTL and
// removes the now-superseded today key.
func (s *Store) ArchiveDaily(ctx context.Context, metric, day string, data map[string]interface{}) error {
	if s == nil {
		return nil
	}
	payload, err := json.Marshal(DayRecord{Day: day, Data: data})
	if err != nil {
		return err
	}
	ttl := time.Duration(s.retentionDays) * 24 * time.Hour
	if err := s.be.set(ctx, dailyKey(metric, day), payload, ttl); err != nil {
		return err
	}
	return s.be.del(ctx, todayKey(metric, day))
}

// LoadHistory returns prior daily archives plus today's blob if present,
// sorted oldest-first. Does not include days with no data.
func (s *Store) LoadHistory(ctx context.Context, metric string) ([]DayRecord, error) {
	if s == nil {
		return nil, nil
	}
	now := time.Now().UTC()
	today := now.Format("2006-01-02")

	var days []string
	for i := s.historyDays; i >= 1; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		days = append(days, d)
	}
	days = append(days, today)

	keys := make([]string, 0, len(days)*2)
	keyDays := make([]string, 0, len(days)*2)
	for _, d := range days {
		keys = append(keys, dailyKey(metric, d))
		keyDays = append(keyDays, d)
		if d == today {
			keys = append(keys, todayKey(metric, d))
			keyDays = append(keyDays, d)
		}
	}

	vals, err := s.be.mget(ctx, keys)
	if err != nil {
		return nil, err
	}

	byDay := make(map[string]DayRecord)
	for i, raw := range vals {
		if raw == nil {
			continue
		}
		var rec DayRecord
		if err := json.Unmarshal([]byte(*raw), &rec); err != nil {
			s.logger.Warn("admin rollup: skip corrupt record", "metric", metric, "key", keys[i], "error", err)
			continue
		}
		if rec.Day == "" {
			rec.Day = keyDays[i]
		}
		byDay[rec.Day] = rec
	}

	out := make([]DayRecord, 0, len(byDay))
	for _, d := range days {
		if rec, ok := byDay[d]; ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// MergeHistory attaches daily_history to a stats snapshot map.
func (s *Store) MergeHistory(ctx context.Context, metric string, snap map[string]interface{}) {
	if s == nil || snap == nil {
		return
	}
	history, err := s.LoadHistory(ctx, metric)
	if err != nil {
		s.logger.Warn("admin rollup: load history failed", "metric", metric, "error", err)
		snap["daily_history"] = []DayRecord{}
		snap["daily_history_available"] = false
		return
	}
	rows := make([]map[string]interface{}, 0, len(history))
	for _, rec := range history {
		row := map[string]interface{}{"day": rec.Day}
		for k, v := range rec.Data {
			row[k] = v
		}
		rows = append(rows, row)
	}
	snap["daily_history"] = rows
	snap["daily_history_available"] = true
}

func newRedisClient(r *config.RedisConfig) (*redis.Client, error) {
	url := os.ExpandEnv(r.URL)
	addr := os.ExpandEnv(r.Address)
	password := os.ExpandEnv(r.Password)

	var opts *redis.Options
	if url != "" {
		parsed, err := redis.ParseURL(url)
		if err != nil {
			return nil, fmt.Errorf("admin rollup redis URL: %w", err)
		}
		opts = parsed
		if addr != "" {
			opts.Addr = addr
		}
		if password != "" {
			opts.Password = password
		}
		if r.DBSet {
			opts.DB = r.DB
		}
	} else {
		if addr == "" {
			addr = "localhost:6379"
		}
		opts = &redis.Options{Addr: addr, Password: password, DB: r.DB}
	}

	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("admin rollup redis ping: %w", err)
	}
	return client, nil
}

// ConfigFromYAML builds rollup config from admin_dashboard.rollups.
func ConfigFromYAML(admin config.AdminDashboardConfig) Config {
	return Config{
		Enabled:       admin.Enabled && admin.Rollups.Enabled,
		Backend:       admin.Rollups.Backend,
		Redis:         admin.Rollups.Redis,
		RetentionDays: admin.Rollups.RetentionDays,
		HistoryDays:   admin.Rollups.HistoryDays,
	}
}

// ChartDays extracts day labels from daily_history rows.
func ChartDays(rows []map[string]interface{}) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if d, ok := row["day"].(string); ok {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// FloatField reads a numeric field from a daily_history row.
func FloatField(row map[string]interface{}, key string) float64 {
	v, ok := row[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

// NormalizeDay ensures YYYY-MM-DD formatting.
func NormalizeDay(day string) string {
	return strings.TrimSpace(day)
}
