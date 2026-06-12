package adminrollup

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	redis "github.com/redis/go-redis/v9"
)

func todayBase(metric, day string) string {
	return fmt.Sprintf("%s%s:today:%s", keyPrefix, metric, day)
}

func totalsKey(metric, day string) string {
	return todayBase(metric, day) + ":totals"
}

func dimKey(metric, day, dim string) string {
	return todayBase(metric, day) + ":" + dim
}

var rollupApplyScript = redis.NewScript(`
local base = KEYS[1]
local ttl = tonumber(ARGV[1])
local totals = cjson.decode(ARGV[2])
local dims = cjson.decode(ARGV[3])

local totalsKey = base .. ":totals"
for field, delta in pairs(totals) do
  redis.call("HINCRBYFLOAT", totalsKey, field, delta)
end
if next(totals) ~= nil then
  redis.call("EXPIRE", totalsKey, ttl)
end

for dim, members in pairs(dims) do
  local dimKey = base .. ":" .. dim
  for member, delta in pairs(members) do
    redis.call("HINCRBYFLOAT", dimKey, member, delta)
  end
  if next(members) ~= nil then
    redis.call("EXPIRE", dimKey, ttl)
  end
end
return 1
`)

func applyDeltaRedis(ctx context.Context, rdb *redis.Client, metric, day string, d Delta, ttl time.Duration) error {
	if d.empty() {
		return nil
	}
	totalsJSON, err := json.Marshal(d.Totals)
	if err != nil {
		return err
	}
	dimsJSON, err := json.Marshal(d.Dimensions)
	if err != nil {
		return err
	}
	sec := int(ttl.Seconds())
	if sec <= 0 {
		sec = int(todayTTL.Seconds())
	}
	return rollupApplyScript.Run(ctx, rdb, []string{todayBase(metric, day)}, sec, string(totalsJSON), string(dimsJSON)).Err()
}

func hgetallFloat(m map[string]string) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			out[k] = f
		}
	}
	return out
}

type nameVal struct {
	Name string  `json:"name"`
	Val  float64 `json:"count"`
}

func topNWithOther(h map[string]float64, n int) (top []nameVal, other float64) {
	pairs := make([]nameVal, 0, len(h))
	for k, v := range h {
		pairs = append(pairs, nameVal{Name: k, Val: v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Val != pairs[j].Val {
			return pairs[i].Val > pairs[j].Val
		}
		return pairs[i].Name < pairs[j].Name
	})
	if n <= 0 || len(pairs) <= n {
		return pairs, 0
	}
	for _, p := range pairs[n:] {
		other += p.Val
	}
	return pairs[:n], other
}

// DimMemberField builds a hash field key for multi-metric dimension members.
func DimMemberField(member, field string) string {
	return member + "|" + field
}

func dimMemberField(member, field string) string {
	return DimMemberField(member, field)
}

func parseDimMemberField(key string) (member, field string, ok bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == '|' {
			return key[:i], key[i+1:], true
		}
	}
	return "", "", false
}

func buildDimRows(h map[string]float64, fields []string) map[string]map[string]float64 {
	out := make(map[string]map[string]float64)
	for k, v := range h {
		member, field, ok := parseDimMemberField(k)
		if !ok {
			continue
		}
		if out[member] == nil {
			out[member] = make(map[string]float64)
		}
		out[member][field] = v
	}
	return out
}

func flattenCostByKey(h map[string]float64) map[string]float64 {
	out := make(map[string]float64)
	for k, v := range h {
		member, field, ok := parseDimMemberField(k)
		if !ok || field != "spend_usd" {
			continue
		}
		out[member] = v
	}
	return out
}

func costDataFromAggregates(totals map[string]float64, byProvider, byKey map[string]float64, caps TopNCaps) map[string]interface{} {
	provRows := buildDimRows(byProvider, nil)

	byProviderOut := make([]map[string]interface{}, 0, len(provRows))
	for name, fields := range provRows {
		row := map[string]interface{}{"name": name}
		for f, v := range fields {
			row[f] = v
		}
		byProviderOut = append(byProviderOut, row)
	}
	sort.Slice(byProviderOut, func(i, j int) bool {
		a, _ := byProviderOut[i]["spend_usd"].(float64)
		b, _ := byProviderOut[j]["spend_usd"].(float64)
		return a > b
	})

	keyRows := buildDimRows(byKey, nil)
	topKeys, otherSpend := topNWithOther(flattenCostByKey(byKey), caps.ByKey)
	byKeyOut := make([]map[string]interface{}, 0, len(topKeys)+1)
	for _, p := range topKeys {
		fields := keyRows[p.Name]
		row := map[string]interface{}{"key_id": p.Name}
		if fields != nil {
			for f, v := range fields {
				row[f] = v
			}
		} else {
			row["spend_usd"] = p.Val
		}
		byKeyOut = append(byKeyOut, row)
	}
	if otherSpend > 0 {
		byKeyOut = append(byKeyOut, map[string]interface{}{
			"key_id":    "other_key",
			"spend_usd": otherSpend,
		})
	}

	data := map[string]interface{}{
		"spend_today_usd":        totals["spend_usd"],
		"input_spend_today_usd":  totals["input_spend_usd"],
		"output_spend_today_usd": totals["output_spend_usd"],
		"requests_today":         int64(totals["requests"]),
		"input_tokens_today":     int64(totals["input_tokens"]),
		"output_tokens_today":    int64(totals["output_tokens"]),
		"by_provider":            byProviderOut,
		"by_key":                 byKeyOut,
	}
	return data
}

func usageDataFromAggregates(totals map[string]float64, byModel, byProvider, byKey, byUser map[string]float64, caps TopNCaps) map[string]interface{} {
	data := map[string]interface{}{
		"requests_today": int64(totals["requests"]),
		"tokens_today":   int64(totals["tokens"]),
	}
	if len(byModel) > 0 {
		data["by_model"] = scopeMapFromDim(byModel)
	}
	if len(byProvider) > 0 {
		data["by_provider"] = scopeMapFromDim(byProvider)
	}
	if len(byKey) > 0 {
		top, other := topNWithOther(flattenScopeDim(byKey), caps.ByKey)
		data["by_key"] = scopeMapFromNames(top, other, "key")
	}
	if len(byUser) > 0 {
		top, other := topNWithOther(flattenScopeDim(byUser), caps.ByUser)
		data["by_user"] = scopeMapFromNames(top, other, "user")
	}
	return data
}

func flattenScopeDim(h map[string]float64) map[string]float64 {
	out := make(map[string]float64)
	for k, v := range h {
		member, field, ok := parseDimMemberField(k)
		if !ok || field != "tokens" {
			continue
		}
		out[member] = v
	}
	return out
}

func scopeMapFromDim(h map[string]float64) map[string]map[string]float64 {
	rows := buildDimRows(h, nil)
	out := make(map[string]map[string]float64, len(rows))
	for member, fields := range rows {
		m := make(map[string]float64, len(fields))
		for f, v := range fields {
			m[f] = v
		}
		out[member] = m
	}
	return out
}

func scopeMapFromNames(top []nameVal, other float64, kind string) map[string]map[string]float64 {
	out := make(map[string]map[string]float64, len(top)+1)
	for _, p := range top {
		out[p.Name] = map[string]float64{"tokens": p.Val}
	}
	if other > 0 {
		out["other_"+kind] = map[string]float64{"tokens": other}
	}
	return out
}

func piiDataFromAggregates(totals map[string]float64, byEntity, byProvider, byKey map[string]float64, caps TopNCaps) map[string]interface{} {
	scanned := totals["requests_scanned"]
	withPII := totals["requests_with_pii"]
	failOpen := totals["fail_open"]
	failClosed := totals["fail_closed"]
	oversize := totals["oversize"]
	clean := scanned - failOpen - failClosed - oversize
	var rate float64
	if clean > 0 {
		rate = withPII / clean
	}

	byEntityOut := make([]nameVal, 0, len(byEntity))
	for k, v := range byEntity {
		byEntityOut = append(byEntityOut, nameVal{Name: k, Val: v})
	}
	sort.Slice(byEntityOut, func(i, j int) bool {
		return byEntityOut[i].Val > byEntityOut[j].Val
	})

	byProvOut := make([]nameVal, 0, len(byProvider))
	for k, v := range byProvider {
		byProvOut = append(byProvOut, nameVal{Name: k, Val: v})
	}
	sort.Slice(byProvOut, func(i, j int) bool {
		return byProvOut[i].Val > byProvOut[j].Val
	})

	topKeys, otherKeys := topNWithOther(byKey, caps.ByKey)
	topKeysOut := make([]nameVal, len(topKeys))
	copy(topKeysOut, topKeys)
	if otherKeys > 0 {
		topKeysOut = append(topKeysOut, nameVal{Name: "other_key", Val: otherKeys})
	}

	return map[string]interface{}{
		"requests_scanned":  int64(scanned),
		"requests_with_pii": int64(withPII),
		"entities_total":    int64(totals["entities_total"]),
		"detection_rate":    rate,
		"fail_open":         int64(failOpen),
		"fail_closed":       int64(failClosed),
		"oversize":          int64(oversize),
		"by_entity":         kvFromNameVals(byEntityOut),
		"by_provider":       kvFromNameVals(byProvOut),
		"top_keys":          kvFromNameVals(topKeysOut),
	}
}

func kvFromNameVals(vals []nameVal) []map[string]interface{} {
	out := make([]map[string]interface{}, len(vals))
	for i, v := range vals {
		out[i] = map[string]interface{}{"name": v.Name, "count": int64(v.Val)}
	}
	return out
}
