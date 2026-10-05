package adminrollup

import (
	"maps"
	"sort"
	"time"
)

// NameCount is one (name, count) row in an admin snapshot's top-N listing.
// The JSON shape matches what the dashboard already consumes from every
// *stats recorder, so recorders can share one type instead of each
// declaring an identical local kv struct.
type NameCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// TopN sorts m by count (desc) then name (asc) for a deterministic listing
// and truncates to n rows when n > 0. The result is never nil.
func TopN(m map[string]int64, n int) []NameCount {
	out := make([]NameCount, 0, len(m))
	for name, count := range m {
		out = append(out, NameCount{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// IntMapDelta returns cur[k] - prev[k] for every key in cur whose value
// moved, as the float64 dimensions a Delta carries. Keys only present in
// prev are ignored: counters are monotonic within a day, so a key cannot
// disappear from cur without a day rollover resetting prev as well.
func IntMapDelta(cur, prev map[string]int64) map[string]float64 {
	out := make(map[string]float64)
	for k, v := range cur {
		if dv := float64(v - prev[k]); dv != 0 {
			out[k] = dv
		}
	}
	return out
}

// CopyIntMap returns a shallow copy of m (never nil).
func CopyIntMap(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	maps.Copy(out, m)
	return out
}

// DayKey formats t as the UTC calendar day the rollup store buckets by.
func DayKey(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// RollDay advances *dayKey to now's UTC day. When the day changed it kicks
// off FinishDayRollover for the completed day (flush, then elected archive,
// both off the caller's lock on a background goroutine) and returns true so
// the recorder can reset its in-memory counters for the new day. Returns
// false, doing nothing, when *dayKey is already today.
//
// Recorders call this from maybeRollDay while holding their own mutex;
// only the string swap happens under that lock.
func (b *RecorderBinding) RollDay(dayKey *string, now time.Time, metric string, caps TopNCaps) bool {
	day := DayKey(now)
	if *dayKey == day {
		return false
	}
	oldDay := *dayKey
	*dayKey = day
	b.FinishDayRollover(metric, oldDay, caps)
	return true
}
