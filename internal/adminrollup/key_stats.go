package adminrollup

import (
	"context"
	"encoding/json"
	"time"
)

// KeyCostDayStats holds fleet-wide cost counters for one masked key on one UTC day.
type KeyCostDayStats struct {
	SpendUSD       float64 `json:"spend_usd"`
	InputSpendUSD  float64 `json:"input_spend_usd"`
	OutputSpendUSD float64 `json:"output_spend_usd"`
	Requests       int64   `json:"requests"`
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
}

// KeyDayPoint is one UTC day's scalar for per-key history charts.
type KeyDayPoint struct {
	Day   string  `json:"day"`
	Value float64 `json:"value"`
}

var costMemberFields = []struct {
	field string
	apply func(*KeyCostDayStats, float64)
}{
	{"spend_usd", func(s *KeyCostDayStats, v float64) { s.SpendUSD = v }},
	{"input_spend_usd", func(s *KeyCostDayStats, v float64) { s.InputSpendUSD = v }},
	{"output_spend_usd", func(s *KeyCostDayStats, v float64) { s.OutputSpendUSD = v }},
	{"requests", func(s *KeyCostDayStats, v float64) { s.Requests = int64(v) }},
	{"input_tokens", func(s *KeyCostDayStats, v float64) { s.InputTokens = int64(v) }},
	{"output_tokens", func(s *KeyCostDayStats, v float64) { s.OutputTokens = int64(v) }},
}

func applyCostField(s *KeyCostDayStats, field string, v float64) {
	for _, spec := range costMemberFields {
		if spec.field == field {
			spec.apply(s, v)
			return
		}
	}
}

// KeyCostDayStats reads exact fleet-wide cost counters for one key from today's
// by_key hash (not the top-N-capped dashboard snapshot).
func (s *Store) KeyCostDayStats(ctx context.Context, day, keyID string) (KeyCostDayStats, bool, error) {
	if s == nil || s.be == nil || keyID == "" {
		return KeyCostDayStats{}, false, nil
	}
	hashKey := dimKey(MetricCost, day, "by_key")
	var out KeyCostDayStats
	var any bool
	for _, spec := range costMemberFields {
		v, err := s.be.hget(ctx, hashKey, dimMemberField(keyID, spec.field))
		if err != nil {
			return KeyCostDayStats{}, false, err
		}
		if v != 0 {
			any = true
			spec.apply(&out, v)
		}
	}
	return out, any, nil
}

// CostDayByKey reads exact counters for every key seen today in one HGETALL.
func (s *Store) CostDayByKey(ctx context.Context, day string) (map[string]KeyCostDayStats, error) {
	return s.costDayDim(ctx, day, "by_key")
}

// CostDayByProvider reads exact per-provider counters for one UTC day.
func (s *Store) CostDayByProvider(ctx context.Context, day string) (map[string]KeyCostDayStats, error) {
	return s.costDayDim(ctx, day, "by_provider")
}

// CostDayByUser reads exact per-user-scope counters for one UTC day.
func (s *Store) CostDayByUser(ctx context.Context, day string) (map[string]KeyCostDayStats, error) {
	return s.costDayDim(ctx, day, "by_user")
}

func (s *Store) costDayDim(ctx context.Context, day, dim string) (map[string]KeyCostDayStats, error) {
	if s == nil || s.be == nil {
		return nil, nil
	}
	h, err := s.be.hgetall(ctx, dimKey(MetricCost, day, dim))
	if err != nil {
		return nil, err
	}
	out := make(map[string]KeyCostDayStats, len(h))
	for k, v := range h {
		member, field, ok := parseDimMemberField(k)
		if !ok {
			continue
		}
		stats := out[member]
		applyCostField(&stats, field, v)
		out[member] = stats
	}
	return out, nil
}

// MonthlySpendByKey reads the exact month-to-date spend of every key in one HGETALL.
func (s *Store) MonthlySpendByKey(ctx context.Context, metric, month string) (map[string]float64, error) {
	if s == nil || s.be == nil {
		return nil, nil
	}
	h, err := s.be.hgetall(ctx, monthKey(metric, month))
	if err != nil {
		return nil, err
	}
	return flattenCostByKey(h), nil
}

// CostDay is one archived UTC day's cost aggregates. ByKeySpend and ByUserSpend
// are top-N capped at archive time and omit the "other_*" remainder rows.
type CostDay struct {
	Day         string
	Totals      KeyCostDayStats
	ByKeySpend  map[string]float64
	ByProvider  map[string]KeyCostDayStats
	ByUserSpend map[string]float64
}

// CostDailyHistory returns the archived prior days (oldest first, up to
// HistoryDays, today excluded) in one MGET. Days without an archive are skipped.
func (s *Store) CostDailyHistory(ctx context.Context) ([]CostDay, error) {
	if s == nil || s.be == nil {
		return nil, nil
	}
	now := time.Now().UTC()
	days := make([]string, 0, s.historyDays)
	keys := make([]string, 0, s.historyDays)
	for i := s.historyDays; i >= 1; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		days = append(days, day)
		keys = append(keys, dailyKey(MetricCost, day))
	}
	raw, err := s.be.mget(ctx, keys)
	if err != nil {
		return nil, err
	}
	out := make([]CostDay, 0, len(raw))
	for i, payload := range raw {
		if payload == nil {
			continue
		}
		var rec DayRecord
		if err := json.Unmarshal([]byte(*payload), &rec); err != nil {
			s.logger.Warn("admin rollup: skip corrupt record", "metric", MetricCost, "key", keys[i], "error", err)
			continue
		}
		if len(rec.Data) == 0 {
			continue
		}
		out = append(out, costDayFromData(days[i], rec.Data))
	}
	return out, nil
}

func costDayFromData(day string, data map[string]interface{}) CostDay {
	cd := CostDay{
		Day:         day,
		ByKeySpend:  map[string]float64{},
		ByProvider:  map[string]KeyCostDayStats{},
		ByUserSpend: map[string]float64{},
	}
	cd.Totals = KeyCostDayStats{
		SpendUSD:       FloatField(data, "spend_today_usd"),
		InputSpendUSD:  FloatField(data, "input_spend_today_usd"),
		OutputSpendUSD: FloatField(data, "output_spend_today_usd"),
		Requests:       int64(FloatField(data, "requests_today")),
		InputTokens:    int64(FloatField(data, "input_tokens_today")),
		OutputTokens:   int64(FloatField(data, "output_tokens_today")),
	}
	if rows, ok := data["by_key"].([]interface{}); ok {
		for _, raw := range rows {
			row, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			id, _ := row["key_id"].(string)
			if id == "" || id == "other_key" {
				continue
			}
			cd.ByKeySpend[id] = FloatField(row, "spend_usd")
		}
	}
	if rows, ok := data["by_provider"].([]interface{}); ok {
		for _, raw := range rows {
			row, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := row["name"].(string)
			if name == "" {
				continue
			}
			var stats KeyCostDayStats
			for _, spec := range costMemberFields {
				spec.apply(&stats, FloatField(row, spec.field))
			}
			cd.ByProvider[name] = stats
		}
	}
	if users, ok := data["by_user"].(map[string]interface{}); ok {
		for scope, raw := range users {
			row, ok := raw.(map[string]interface{})
			if !ok || scope == "other_user" {
				continue
			}
			cd.ByUserSpend[scope] = FloatField(row, "spend_usd")
		}
	}
	return cd
}

// KeyPIIDayCount reads the fleet-wide PII scan count for one key on a UTC day.
func (s *Store) KeyPIIDayCount(ctx context.Context, day, keyID string) (int64, bool, error) {
	if s == nil || s.be == nil || keyID == "" {
		return 0, false, nil
	}
	v, err := s.be.hget(ctx, dimKey(MetricPII, day, "by_key"), keyID)
	if err != nil {
		return 0, false, err
	}
	if v == 0 {
		return 0, false, nil
	}
	return int64(v), true, nil
}

// KeyCostDailySeries returns per-day spend for one key over the last days UTC
// days (inclusive of today). Today is read from the live hash; prior days use
// archived daily JSON (top-N capped at archive time).
func (s *Store) KeyCostDailySeries(ctx context.Context, keyID string, days int) ([]KeyDayPoint, bool, error) {
	return s.keyDailySeries(ctx, MetricCost, keyID, days, costSpendFromDayData)
}

// KeyPIIDailySeries returns per-day PII detection counts for one key.
func (s *Store) KeyPIIDailySeries(ctx context.Context, keyID string, days int) ([]KeyDayPoint, bool, error) {
	return s.keyDailySeries(ctx, MetricPII, keyID, days, piiCountFromDayData)
}

type dayScalarFn func(map[string]interface{}, string) float64

func (s *Store) keyDailySeries(
	ctx context.Context,
	metric, keyID string,
	days int,
	scalar dayScalarFn,
) ([]KeyDayPoint, bool, error) {
	if s == nil || s.be == nil || keyID == "" || days <= 0 {
		return nil, false, nil
	}
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	out := make([]KeyDayPoint, 0, days)
	var any bool
	for i := days - 1; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		var value float64
		if day == today {
			switch metric {
			case MetricCost:
				stats, ok, err := s.KeyCostDayStats(ctx, day, keyID)
				if err != nil {
					return nil, false, err
				}
				if ok {
					any = true
				}
				value = stats.SpendUSD
			case MetricPII:
				count, ok, err := s.KeyPIIDayCount(ctx, day, keyID)
				if err != nil {
					return nil, false, err
				}
				if ok {
					any = true
				}
				value = float64(count)
			}
		} else {
			data, ok, err := s.loadDailyData(ctx, metric, day)
			if err != nil {
				return nil, false, err
			}
			if ok {
				value = scalar(data, keyID)
				if value > 0 {
					any = true
				}
			}
		}
		out = append(out, KeyDayPoint{Day: day, Value: value})
	}
	return out, any, nil
}

func (s *Store) loadDailyData(ctx context.Context, metric, day string) (map[string]interface{}, bool, error) {
	raw, err := s.be.mget(ctx, []string{dailyKey(metric, day)})
	if err != nil {
		return nil, false, err
	}
	if len(raw) == 0 || raw[0] == nil {
		return nil, false, nil
	}
	var rec DayRecord
	if err := json.Unmarshal([]byte(*raw[0]), &rec); err != nil {
		return nil, false, err
	}
	if len(rec.Data) == 0 {
		return nil, false, nil
	}
	return rec.Data, true, nil
}

func costSpendFromDayData(data map[string]interface{}, keyID string) float64 {
	rows, ok := data["by_key"].([]interface{})
	if !ok {
		return 0
	}
	for _, raw := range rows {
		row, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := row["key_id"].(string)
		if id != keyID {
			continue
		}
		switch v := row["spend_usd"].(type) {
		case float64:
			return v
		case int:
			return float64(v)
		case int64:
			return float64(v)
		}
	}
	return 0
}

func piiCountFromDayData(data map[string]interface{}, keyID string) float64 {
	rows, ok := data["top_keys"].([]interface{})
	if !ok {
		return 0
	}
	for _, raw := range rows {
		row, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := row["name"].(string)
		if name != keyID {
			continue
		}
		switch v := row["count"].(type) {
		case float64:
			return v
		case int:
			return float64(v)
		case int64:
			return float64(v)
		}
	}
	return 0
}
