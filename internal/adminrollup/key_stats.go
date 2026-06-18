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
