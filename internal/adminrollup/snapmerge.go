package adminrollup

import (
	"encoding/json"
	"sort"
)

// Snap merge helpers shared by in-process recorders after MergeToday overlays
// fleet-wide Redis totals onto a live snapshot.

// SnapInt64 coerces JSON-ish snapshot values to int64.
func SnapInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	default:
		return 0
	}
}

// SnapFloat64 coerces JSON-ish snapshot values to float64.
func SnapFloat64(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

// MergeSnapInt64Max sets snap[key] to max(existing, local) when local > 0.
func MergeSnapInt64Max(snap map[string]interface{}, key string, local int64) {
	if snap == nil || local <= 0 {
		return
	}
	if local > SnapInt64(snap[key]) {
		snap[key] = local
	}
}

// MergeSnapFloat64Max sets snap[key] to max(existing, local) when local > 0.
func MergeSnapFloat64Max(snap map[string]interface{}, key string, local float64) {
	if snap == nil || local <= 0 {
		return
	}
	if local > SnapFloat64(snap[key]) {
		snap[key] = local
	}
}

// MergeInt64Maps returns per-key max(a, b).
func MergeInt64Maps(a, b map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if v > out[k] {
			out[k] = v
		}
	}
	return out
}

// NameCountMapFromSnap parses {name,count} rows from a snapshot field.
func NameCountMapFromSnap(raw interface{}) map[string]int64 {
	if raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		name, _ := row["name"].(string)
		if name == "" {
			continue
		}
		out[name] = SnapInt64(row["count"])
	}
	return out
}

// MergeSnapNameCounts merges local name→count maps into snap[field] as sorted rows.
func MergeSnapNameCounts(snap map[string]interface{}, field string, local map[string]int64, limit int) {
	if snap == nil || len(local) == 0 {
		return
	}
	merged := MergeInt64Maps(NameCountMapFromSnap(snap[field]), local)
	if len(merged) == 0 {
		return
	}
	snap[field] = kvFromNameVals(nameValsFromMap(merged, limit))
}

// MergeSnapInt64Map merges map[string]int64 fields (e.g. by_provider counters).
func MergeSnapInt64Map(snap map[string]interface{}, field string, local map[string]int64) {
	if snap == nil || len(local) == 0 {
		return
	}
	var existing map[string]int64
	switch raw := snap[field].(type) {
	case map[string]int64:
		existing = raw
	case map[string]interface{}:
		existing = make(map[string]int64, len(raw))
		for k, v := range raw {
			existing[k] = SnapInt64(v)
		}
	}
	snap[field] = MergeInt64Maps(existing, local)
}

// PIIDetectionRate computes detection rate from rollup totals.
func PIIDetectionRate(scanned, withPII, failOpen, failClosed, oversize int64) float64 {
	clean := scanned - failOpen - failClosed - oversize
	if clean <= 0 {
		return 0
	}
	return float64(withPII) / float64(clean)
}

func nameValsFromMap(m map[string]int64, limit int) []nameVal {
	pairs := make([]nameVal, 0, len(m))
	for name, count := range m {
		pairs = append(pairs, nameVal{Name: name, Val: float64(count)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Val != pairs[j].Val {
			return pairs[i].Val > pairs[j].Val
		}
		return pairs[i].Name < pairs[j].Name
	})
	if limit > 0 && len(pairs) > limit {
		pairs = pairs[:limit]
	}
	return pairs
}
