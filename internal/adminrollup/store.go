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
	MetricCost            = "cost"
	MetricPII             = "pii"
	MetricIDGate          = "id_gate"
	MetricUsage           = "usage"
	MetricCircuit         = "circuit"
	MetricCircuitActivity = "circuit_activity"
	MetricRateLimit       = "ratelimit"
	MetricModelStatus     = "model_status"

	keyPrefix = "llm:admin:"

	// todayTTL bounds the lifetime of an in-progress "today" key. It is far
	// longer than a UTC day so a live day is always rewritten well before it
	// expires, but it guarantees an orphaned today key (e.g. the proxy
	// restarts across midnight before the day-rollover archive runs) self-
	// expires instead of leaking forever.
	todayTTL = 48 * time.Hour

	// hourlyTTL bounds per-hour rollup hashes. Long enough to cover the full
	// UTC day plus a few hours of overlap so late-arriving flushes still land.
	hourlyTTL = 26 * time.Hour

	// monthTTL bounds monthly per-key spend hashes. It is longer than a calendar
	// month so in-progress months are rewritten before expiry.
	monthTTL = 35 * 24 * time.Hour
)

// DayRecord is one UTC calendar day of rolled-up stats (stored in Redis).
type DayRecord struct {
	Day  string                 `json:"day"`
	Data map[string]interface{} `json:"data"`
}

// HourRecord is one UTC hour of summed totals for the current day (stored in
// Redis). Only Totals fields are tracked — no per-dimension breakdown.
type HourRecord struct {
	Hour int                `json:"hour"`
	Data map[string]float64 `json:"data"`
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

func monthKey(metric, month string) string {
	return fmt.Sprintf("%s%s:month:%s:by_key", keyPrefix, metric, month)
}

func monthReservedKey(metric, month string) string {
	return fmt.Sprintf("%s%s:month:%s:%s", keyPrefix, metric, month, monthReservedDim)
}

func hourlyKey(metric, day string, hour int) string {
	return fmt.Sprintf("%s%s:hourly:%s:%02d", keyPrefix, metric, day, hour)
}

// hourlySnapKey stores a gauge-typed snapshot (e.g. circuit peaks) as a JSON
// blob so the value can be overwritten each tick rather than accumulated.
func hourlySnapKey(metric, day string, hour int) string {
	return fmt.Sprintf("%s%s:hourly:%s:%02d:snap", keyPrefix, metric, day, hour)
}

// ApplyHourlyTotals atomically merges totals into the current UTC hour's hash.
// Only the Totals map from a Delta is stored — dimensions are not tracked at
// hourly granularity since the chart only needs per-hour scalar values.
func (s *Store) ApplyHourlyTotals(ctx context.Context, metric, day string, hour int, totals map[string]float64) error {
	if s == nil || len(totals) == 0 {
		return nil
	}
	key := hourlyKey(metric, day, hour)
	for field, val := range totals {
		if err := s.be.hincrbyfloat(ctx, key, field, val, hourlyTTL); err != nil {
			return err
		}
	}
	return nil
}

// LoadTodayHourly returns per-hour aggregates for the current UTC day, in
// ascending hour order, omitting hours that have no data.
func (s *Store) LoadTodayHourly(ctx context.Context, metric string) ([]HourRecord, error) {
	if s == nil {
		return nil, nil
	}
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	currentHour := now.Hour()
	var out []HourRecord
	for h := 0; h <= currentHour; h++ {
		data, err := s.be.hgetall(ctx, hourlyKey(metric, day, h))
		if err != nil {
			return nil, err
		}
		if len(data) > 0 {
			out = append(out, HourRecord{Hour: h, Data: data})
		}
	}
	return out, nil
}

// MergeHourly attaches hourly_history and hourly_history_available to a stats
// snapshot map. Callers use this in Snapshot() alongside MergeHistory so the
// "Today" trend chart can use Redis-backed per-hour data rather than a
// browser-side sparkline.
func (s *Store) MergeHourly(ctx context.Context, metric string, snap map[string]interface{}) {
	if s == nil || snap == nil {
		return
	}
	records, err := s.LoadTodayHourly(ctx, metric)
	if err != nil {
		s.logger.Warn("admin rollup: load hourly failed", "metric", metric, "error", err)
		snap["hourly_history_available"] = false
		return
	}
	rows := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		rows = append(rows, hourlyRowFromTotals(metric, rec.Hour, rec.Data))
	}
	snap["hourly_history"] = rows
	snap["hourly_history_available"] = true
}

// SaveHourlySnapshot writes a gauge-typed snapshot for one UTC hour as a JSON
// blob (overwriting any previous value for that hour). Use this instead of
// ApplyHourlyTotals when the metric is a gauge (windowed peak) rather than a
// cumulative delta — e.g. circuit-breaker failure counts.
func (s *Store) SaveHourlySnapshot(ctx context.Context, metric, day string, hour int, data map[string]interface{}) error {
	if s == nil || len(data) == 0 {
		return nil
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.be.set(ctx, hourlySnapKey(metric, day, hour), payload, hourlyTTL)
}

// LoadTodayHourlySnapshots returns per-hour snapshots for the current UTC day
// (gauge blobs written by SaveHourlySnapshot), in ascending hour order.
func (s *Store) LoadTodayHourlySnapshots(ctx context.Context, metric string) ([]HourRecord, error) {
	if s == nil {
		return nil, nil
	}
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	currentHour := now.Hour()
	keys := make([]string, currentHour+1)
	for h := 0; h <= currentHour; h++ {
		keys[h] = hourlySnapKey(metric, day, h)
	}
	vals, err := s.be.mget(ctx, keys)
	if err != nil {
		return nil, err
	}
	var out []HourRecord
	for i, raw := range vals {
		if raw == nil {
			continue
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(*raw), &data); err != nil {
			s.logger.Warn("admin rollup: skip corrupt hourly snap", "metric", metric, "hour", i, "error", err)
			continue
		}
		rec := HourRecord{Hour: i, Data: make(map[string]float64, len(data))}
		for k, v := range data {
			switch n := v.(type) {
			case float64:
				rec.Data[k] = n
			case int:
				rec.Data[k] = float64(n)
			case int64:
				rec.Data[k] = float64(n)
			}
		}
		if len(rec.Data) > 0 {
			out = append(out, rec)
		}
	}
	return out, nil
}

// MergeHourlySnapshots is like MergeHourly but reads gauge blobs written by
// SaveHourlySnapshot rather than accumulated hash increments. Callers use this
// for metrics where the value is a windowed peak (e.g. circuit-breaker failure
// count) rather than a cumulative total.
func (s *Store) MergeHourlySnapshots(ctx context.Context, metric string, snap map[string]interface{}) {
	if s == nil || snap == nil {
		return
	}
	records, err := s.LoadTodayHourlySnapshots(ctx, metric)
	if err != nil {
		s.logger.Warn("admin rollup: load hourly snapshots failed", "metric", metric, "error", err)
		snap["hourly_history_available"] = false
		return
	}
	rows := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		row := map[string]interface{}{"hour": rec.Hour}
		for k, v := range rec.Data {
			row[k] = v
		}
		rows = append(rows, row)
	}
	snap["hourly_history"] = rows
	snap["hourly_history_available"] = true
}

// ApplyDelta atomically folds an instance delta into today's hash aggregates.
func (s *Store) ApplyDelta(ctx context.Context, metric, day string, d Delta) error {
	if s == nil || d.empty() {
		return nil
	}
	return s.be.applyDelta(ctx, metric, day, d, todayTTL)
}

// TryElectArchiver returns true when this instance wins a short-lived lock to
// write circuit daily peaks (one writer per metric/day).
func (s *Store) TryElectArchiver(ctx context.Context, metric, day, holder string) bool {
	if s == nil {
		return false
	}
	key := fmt.Sprintf("%s%s:archiver:%s", keyPrefix, metric, day)
	ok, err := s.be.trySetNX(ctx, key, holder, 26*time.Hour)
	return err == nil && ok
}

// KeySpendUSD reads the fleet-wide spend (USD) recorded for a single masked
// key in the given metric/day's by_key hash. Used by hard cluster-wide cost
// limit enforcement, which needs the exact per-key value rather than the
// top-N-capped dashboard view. Returns 0 (no error) when the key has no spend.
func (s *Store) KeySpendUSD(ctx context.Context, metric, day, keyID string) (float64, error) {
	if s == nil || s.be == nil || keyID == "" {
		return 0, nil
	}
	return s.be.hget(ctx, dimKey(metric, day, "by_key"), dimMemberField(keyID, "spend_usd"))
}

// KeyMonthlySpendUSD reads the fleet-wide monthly spend (USD) recorded for a
// single masked key in the given metric/month's by_key hash.
func (s *Store) KeyMonthlySpendUSD(ctx context.Context, metric, month, keyID string) (float64, error) {
	if s == nil || s.be == nil || keyID == "" {
		return 0, nil
	}
	return s.be.hget(ctx, monthKey(metric, month), dimMemberField(keyID, "spend_usd"))
}

// ApplyMonthlyKeySpend atomically folds spendUSD into the monthly per-key hash.
func (s *Store) ApplyMonthlyKeySpend(ctx context.Context, metric, month, keyID string, spendUSD float64) error {
	if s == nil || s.be == nil || keyID == "" || spendUSD == 0 {
		return nil
	}
	return s.be.hincrbyfloat(ctx, monthKey(metric, month), dimMemberField(keyID, "spend_usd"), spendUSD, monthTTL)
}

func (s *Store) loadHash(ctx context.Context, key string) (map[string]float64, error) {
	if s == nil {
		return nil, nil
	}
	return s.be.hgetall(ctx, key)
}

func (s *Store) buildTodayData(ctx context.Context, metric, day string, caps TopNCaps) (map[string]interface{}, bool) {
	totals, err := s.loadHash(ctx, totalsKey(metric, day))
	if err != nil || len(totals) == 0 {
		return nil, false
	}
	switch metric {
	case MetricCost:
		byProv, _ := s.loadHash(ctx, dimKey(metric, day, "by_provider"))
		byKey, _ := s.loadHash(ctx, dimKey(metric, day, "by_key"))
		byUser, _ := s.loadHash(ctx, dimKey(metric, day, "by_user"))
		return costDataFromAggregates(totals, byProv, byKey, byUser, caps), true
	case MetricUsage:
		byModel, _ := s.loadHash(ctx, dimKey(metric, day, "by_model"))
		byProv, _ := s.loadHash(ctx, dimKey(metric, day, "by_provider"))
		byKey, _ := s.loadHash(ctx, dimKey(metric, day, "by_key"))
		byUser, _ := s.loadHash(ctx, dimKey(metric, day, "by_user"))
		return usageDataFromAggregates(totals, byModel, byProv, byKey, byUser, caps), true
	case MetricPII:
		byEntity, _ := s.loadHash(ctx, dimKey(metric, day, "by_entity"))
		byProv, _ := s.loadHash(ctx, dimKey(metric, day, "by_provider"))
		byKey, _ := s.loadHash(ctx, dimKey(metric, day, "by_key"))
		return piiDataFromAggregates(totals, byEntity, byProv, byKey, caps), true
	case MetricIDGate:
		byEntity, _ := s.loadHash(ctx, dimKey(metric, day, "by_entity"))
		byProv, _ := s.loadHash(ctx, dimKey(metric, day, "by_provider"))
		byKey, _ := s.loadHash(ctx, dimKey(metric, day, "by_key"))
		return idGateDataFromAggregates(totals, byEntity, byProv, byKey, caps), true
	case MetricCircuitActivity:
		byProv, _ := s.loadHash(ctx, dimKey(metric, day, "by_provider"))
		byKey, _ := s.loadHash(ctx, dimKey(metric, day, "by_key"))
		return circuitActivityDataFromAggregates(totals, byProv, byKey), true
	case MetricModelStatus:
		byRetired, _ := s.loadHash(ctx, dimKey(metric, day, "by_retired"))
		byDeprecated, _ := s.loadHash(ctx, dimKey(metric, day, "by_deprecated"))
		byUnknown, _ := s.loadHash(ctx, dimKey(metric, day, "by_unknown"))
		return modelStatusDataFromAggregates(totals, byRetired, byDeprecated, byUnknown, caps), true
	case MetricRateLimit:
		byProv, _ := s.loadHash(ctx, dimKey(metric, day, "by_provider"))
		byReason, _ := s.loadHash(ctx, dimKey(metric, day, "by_reason"))
		return rateLimitDataFromAggregates(totals, byProv, byReason), true
	default:
		return nil, false
	}
}

// MergeToday overlays fleet-wide today totals from Redis onto a live snapshot.
func (s *Store) MergeToday(ctx context.Context, metric, day string, snap map[string]interface{}, caps TopNCaps) {
	if s == nil || snap == nil {
		return
	}
	data, ok := s.buildTodayData(ctx, metric, day, caps)
	if !ok {
		return
	}
	for k, v := range data {
		snap[k] = v
	}
}

// ArchiveDailyFromAggregates copies completed hash aggregates to the daily JSON
// key and removes today's hash keys.
func (s *Store) ArchiveDailyFromAggregates(ctx context.Context, metric, day string, caps TopNCaps) error {
	if s == nil {
		return nil
	}
	data, ok := s.buildTodayData(ctx, metric, day, caps)
	if !ok {
		return nil
	}
	if err := s.ArchiveDaily(ctx, metric, day, data); err != nil {
		return err
	}
	_ = s.be.del(ctx, totalsKey(metric, day))
	for _, dim := range []string{"by_provider", "by_key", "by_model", "by_user", "by_entity"} {
		_ = s.be.del(ctx, dimKey(metric, day, dim))
	}
	_ = s.be.del(ctx, todayKey(metric, day))
	return nil
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

	byDay := make(map[string]DayRecord)
	keys := make([]string, 0, len(days)*2)
	keyDays := make([]string, 0, len(days)*2)
	for _, d := range days {
		keys = append(keys, dailyKey(metric, d))
		keyDays = append(keyDays, d)
		if d == today {
			if agg, ok := s.buildTodayData(ctx, metric, d, TopNCaps{ByKey: 100, ByUser: 100}); ok {
				byDay[d] = DayRecord{Day: d, Data: agg}
			} else {
				keys = append(keys, todayKey(metric, d))
				keyDays = append(keyDays, d)
			}
		}
	}

	vals, err := s.be.mget(ctx, keys)
	if err != nil {
		return nil, err
	}

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
//
// Rollup writing is gated ONLY on rollups.enabled — intentionally independent
// of admin_dashboard.enabled (which gates the HTTP dashboard server). This lets
// sidecars (dashboard off) still publish usage/cost/ratelimit/circuit rollups to
// the shared Redis that the standalone dashboard reads, so the dashboard reflects
// all proxy traffic, not just the rarely-hit standalone service.
func ConfigFromYAML(admin config.AdminDashboardConfig) Config {
	return Config{
		Enabled:       admin.Rollups.Enabled,
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
