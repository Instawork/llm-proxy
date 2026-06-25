package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/middleware"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
	"github.com/gorilla/mux"
)

type keyCostStatsResponse struct {
	Source         string  `json:"source"`
	SpendUSD       float64 `json:"spend_usd"`
	InputSpendUSD  float64 `json:"input_spend_usd"`
	OutputSpendUSD float64 `json:"output_spend_usd"`
	Requests       int64   `json:"requests"`
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
}

type keyCostMonthResponse struct {
	Month    string  `json:"month"`
	SpendUSD float64 `json:"spend_usd"`
	Source   string  `json:"source"`
}

type keyPIIStatsResponse struct {
	Source     string `json:"source"`
	Detections int64  `json:"detections"`
}

type keyDayPointResponse struct {
	Day   string  `json:"day"`
	Value float64 `json:"value"`
}

type keyCostRecentResponse struct {
	Time           int64   `json:"time"`
	Provider       string  `json:"provider"`
	KeyID          string  `json:"key_id,omitempty"`
	SpendUSD       float64 `json:"spend_usd"`
	InputSpendUSD  float64 `json:"input_spend_usd,omitempty"`
	OutputSpendUSD float64 `json:"output_spend_usd,omitempty"`
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	Model          string  `json:"model,omitempty"`
}

type keyPIIRecentResponse struct {
	Time         int64          `json:"time"`
	Provider     string         `json:"provider"`
	KeyID        string         `json:"key_id,omitempty"`
	EntityCounts map[string]int `json:"entity_counts"`
	EntityTotal  int            `json:"entity_total"`
	DurationMs   float64        `json:"duration_ms"`
	Outcome      string         `json:"outcome"`
}

type keyRateUsageResponse struct {
	Window   string `json:"window"`
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
}

type keyStatsResponse struct {
	MaskedKeyID     string                  `json:"masked_key_id"`
	Day             string                  `json:"day"`
	RollupAvailable bool                    `json:"rollup_available"`
	RollupBackend   string                  `json:"rollup_backend,omitempty"`
	CostToday       keyCostStatsResponse    `json:"cost_today"`
	CostMonth       keyCostMonthResponse    `json:"cost_month"`
	PIIToday        keyPIIStatsResponse     `json:"pii_today"`
	RateUsage       []keyRateUsageResponse  `json:"rate_usage,omitempty"`
	RateBackend     string                  `json:"rate_backend,omitempty"`
	CostHistory     []keyDayPointResponse   `json:"cost_history"`
	PIIHistory      []keyDayPointResponse   `json:"pii_history"`
	RecentCost      []keyCostRecentResponse `json:"recent_cost"`
	RecentPII       []keyPIIRecentResponse  `json:"recent_pii"`
}

func (h *handler) handleKeyStats(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	keyID := strings.TrimSpace(mux.Vars(r)["key"])
	if keyID == "" || (!apikeys.HasKeyPrefix(keyID) && !apikeys.IsProxyMaskedKeyID(keyID)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid key identifier"})
		return
	}

	user, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	role, err := adminusers.ParseRole(user.Role)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	record, err := h.deps.APIKeyStore.GetKeyRecordByID(r.Context(), keyID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
			return
		}
		h.deps.Logger.Error("admin: key stats lookup failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load key"})
		return
	}
	if !canAccessKey(role, user.Email, record) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	masked := middleware.MaskKeyID(record.PK)
	today := time.Now().UTC().Format("2006-01-02")
	ctx := r.Context()

	resp := keyStatsResponse{
		MaskedKeyID: masked,
		Day:         today,
	}

	memCost := memoryCostForKey(h.deps.CostSummary, masked, record.PK)
	resp.RecentCost = sanitizeRecentCost(memoryRecentCostForKey(h.deps.CostSummary, masked, record.PK))
	resp.RecentPII = sanitizeRecentPII(memoryRecentPIIForKey(h.deps.PIISummary, masked))
	if len(resp.RecentCost) > 0 {
		memCost = mergeMemoryKeyCosts(memCost, memoryCostFromRecent(recentCostOnUTCDay(resp.RecentCost, today)))
	}
	memPII := memoryPIIForKey(h.deps.PIISummary, masked)
	if len(resp.RecentPII) > 0 {
		memPII = maxInt64(memPII, memoryPIIFromRecent(recentPIIOnUTCDay(resp.RecentPII, today)))
	}

	var redisCost adminrollup.KeyCostDayStats
	var redisCostOK bool
	var redisPII int64
	var redisPIIOK bool
	costRollupOK := false
	piiRollupOK := false
	if h.deps.AdminRollupStore != nil {
		resp.RollupBackend = h.deps.AdminRollupStore.Backend()

		var costErr, piiErr error
		redisCost, redisCostOK, costErr = h.deps.AdminRollupStore.KeyCostDayStats(ctx, today, masked)
		if costErr != nil {
			h.deps.Logger.Error("admin: key cost stats read failed", "key", masked, "error", costErr)
		}
		redisPII, redisPIIOK, piiErr = h.deps.AdminRollupStore.KeyPIIDayCount(ctx, today, masked)
		if piiErr != nil {
			h.deps.Logger.Error("admin: key pii stats read failed", "key", masked, "error", piiErr)
		}
		costRollupOK = costErr == nil
		piiRollupOK = piiErr == nil
		resp.RollupAvailable = costRollupOK && piiRollupOK

		if costSeries, _, histErr := h.deps.AdminRollupStore.KeyCostDailySeries(ctx, masked, 7); histErr != nil {
			h.deps.Logger.Error("admin: key cost history read failed", "key", masked, "error", histErr)
		} else {
			resp.CostHistory = toDayPoints(costSeries)
		}
		if piiSeries, _, histErr := h.deps.AdminRollupStore.KeyPIIDailySeries(ctx, masked, 7); histErr != nil {
			h.deps.Logger.Error("admin: key pii history read failed", "key", masked, "error", histErr)
		} else {
			resp.PIIHistory = toDayPoints(piiSeries)
		}
	}

	resp.CostToday = mergeKeyCostStats(memCost, redisCost, redisCostOK, costRollupOK)
	resp.CostMonth = keyCostMonthForKey(ctx, h.deps.AdminRollupStore, today, masked, resp.CostToday.SpendUSD, costRollupOK)
	resp.PIIToday = mergeKeyPIIStats(memPII, redisPII, redisPIIOK, piiRollupOK)
	if h.deps.RateLimiter != nil {
		if snapshotter, ok := h.deps.RateLimiter.(ratelimit.Snapshotter); ok {
			snap := snapshotter.Snapshot(time.Now())
			sanitizeLimitsSnapshot(&snap)
			resp.RateUsage = rateUsageForKeyFromSnapshot(snap, record.PK)
			resp.RateBackend = snap.Backend
			if resp.RateBackend == "" {
				resp.RateBackend = "memory"
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

type memoryKeyCost struct {
	SpendUSD       float64
	InputSpendUSD  float64
	OutputSpendUSD float64
	Requests       int64
	InputTokens    int64
	OutputTokens   int64
}

func recentRowsFromSnap(snap map[string]interface{}, field string) []map[string]interface{} {
	raw, ok := snap[field]
	if !ok || raw == nil {
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
	return rows
}

func memoryCostForKey(summary func() map[string]interface{}, masked, rawKey string) memoryKeyCost {
	snap := safeSummary(summary)
	for _, row := range recentRowsFromSnap(snap, "by_key") {
		id := asString(row["key_id"])
		if id != masked && id != rawKey {
			continue
		}
		return memoryKeyCost{
			SpendUSD:       asFloat(row["spend_usd"]),
			InputSpendUSD:  asFloat(row["input_spend_usd"]),
			OutputSpendUSD: asFloat(row["output_spend_usd"]),
			Requests:       int64(asFloat(row["requests"])),
			InputTokens:    int64(asFloat(row["input_tokens"])),
			OutputTokens:   int64(asFloat(row["output_tokens"])),
		}
	}
	return memoryKeyCost{}
}

func memoryPIIForKey(summary func() map[string]interface{}, masked string) int64 {
	snap := safeSummary(summary)
	for _, row := range recentRowsFromSnap(snap, "top_keys") {
		if asString(row["name"]) == masked {
			return int64(asFloat(row["count"]))
		}
	}
	var count int64
	for _, row := range recentRowsFromSnap(snap, "recent") {
		if asString(row["key_id"]) != masked {
			continue
		}
		if asFloat(row["entity_total"]) > 0 {
			count++
		}
	}
	return count
}

func memoryPIIFromRecent(events []keyPIIRecentResponse) int64 {
	var count int64
	for _, e := range events {
		if e.EntityTotal > 0 {
			count++
		}
	}
	return count
}

func utcDayBounds(day string) (start, end int64) {
	t, err := time.ParseInLocation("2006-01-02", day, time.UTC)
	if err != nil {
		return 0, 0
	}
	start = t.Unix()
	return start, start + 86400
}

func recentCostOnUTCDay(events []keyCostRecentResponse, day string) []keyCostRecentResponse {
	start, end := utcDayBounds(day)
	if start == 0 {
		return events
	}
	out := make([]keyCostRecentResponse, 0, len(events))
	for _, e := range events {
		if e.Time >= start && e.Time < end {
			out = append(out, e)
		}
	}
	return out
}

func recentPIIOnUTCDay(events []keyPIIRecentResponse, day string) []keyPIIRecentResponse {
	start, end := utcDayBounds(day)
	if start == 0 {
		return events
	}
	out := make([]keyPIIRecentResponse, 0, len(events))
	for _, e := range events {
		if e.Time >= start && e.Time < end {
			out = append(out, e)
		}
	}
	return out
}

func memoryRecentCostForKey(summary func() map[string]interface{}, masked, rawKey string) []map[string]interface{} {
	snap := safeSummary(summary)
	out := make([]map[string]interface{}, 0)
	for _, row := range recentRowsFromSnap(snap, "recent") {
		id := asString(row["key_id"])
		if id == masked || id == rawKey {
			out = append(out, row)
		}
	}
	return out
}

func memoryRecentPIIForKey(summary func() map[string]interface{}, masked string) []map[string]interface{} {
	snap := safeSummary(summary)
	out := make([]map[string]interface{}, 0)
	for _, row := range recentRowsFromSnap(snap, "recent") {
		if asString(row["key_id"]) == masked {
			out = append(out, row)
		}
	}
	return out
}

func sanitizeRecentCost(rows []map[string]interface{}) []keyCostRecentResponse {
	out := make([]keyCostRecentResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, keyCostRecentResponse{
			Time:           int64(asFloat(row["time"])),
			Provider:       asString(row["provider"]),
			KeyID:          middleware.MaskKeyID(asString(row["key_id"])),
			SpendUSD:       asFloat(row["spend_usd"]),
			InputSpendUSD:  asFloat(row["input_spend_usd"]),
			OutputSpendUSD: asFloat(row["output_spend_usd"]),
			InputTokens:    int(asFloat(row["input_tokens"])),
			OutputTokens:   int(asFloat(row["output_tokens"])),
			Model:          asString(row["model"]),
		})
	}
	return out
}

func sanitizeRecentPII(rows []map[string]interface{}) []keyPIIRecentResponse {
	out := make([]keyPIIRecentResponse, 0, len(rows))
	for _, row := range rows {
		entityCounts := map[string]int{}
		if raw, ok := row["entity_counts"].(map[string]interface{}); ok {
			for k, v := range raw {
				entityCounts[k] = int(asFloat(v))
			}
		}
		out = append(out, keyPIIRecentResponse{
			Time:         int64(asFloat(row["time"])),
			Provider:     asString(row["provider"]),
			KeyID:        middleware.MaskKeyID(asString(row["key_id"])),
			EntityCounts: entityCounts,
			EntityTotal:  int(asFloat(row["entity_total"])),
			DurationMs:   asFloat(row["duration_ms"]),
			Outcome:      asString(row["outcome"]),
		})
	}
	return out
}

func safeSummary(summary func() map[string]interface{}) map[string]interface{} {
	if summary == nil {
		return map[string]interface{}{}
	}
	snap := summary()
	if snap == nil {
		return map[string]interface{}{}
	}
	return snap
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}

func asFloat(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

func mergeKeyCostStats(mem memoryKeyCost, redis adminrollup.KeyCostDayStats, redisOK, rollupBound bool) keyCostStatsResponse {
	out := keyCostStatsResponse{
		Source:         "memory",
		SpendUSD:       mem.SpendUSD,
		InputSpendUSD:  mem.InputSpendUSD,
		OutputSpendUSD: mem.OutputSpendUSD,
		Requests:       mem.Requests,
		InputTokens:    mem.InputTokens,
		OutputTokens:   mem.OutputTokens,
	}
	if !rollupBound {
		return out
	}
	if redisOK {
		out.SpendUSD = maxFloat(mem.SpendUSD, redis.SpendUSD)
		out.InputSpendUSD = maxFloat(mem.InputSpendUSD, redis.InputSpendUSD)
		out.OutputSpendUSD = maxFloat(mem.OutputSpendUSD, redis.OutputSpendUSD)
		out.Requests = maxInt64(mem.Requests, redis.Requests)
		out.InputTokens = maxInt64(mem.InputTokens, redis.InputTokens)
		out.OutputTokens = maxInt64(mem.OutputTokens, redis.OutputTokens)
	}
	switch {
	case mem.SpendUSD > 0:
		out.Source = "redislive"
	case redisOK && redis.SpendUSD > 0:
		out.Source = "redis"
	default:
		out.Source = "redislive"
	}
	return out
}

func memoryCostFromRecent(events []keyCostRecentResponse) memoryKeyCost {
	var out memoryKeyCost
	for _, e := range events {
		out.SpendUSD += e.SpendUSD
		out.InputSpendUSD += e.InputSpendUSD
		out.OutputSpendUSD += e.OutputSpendUSD
		out.Requests++
		out.InputTokens += int64(e.InputTokens)
		out.OutputTokens += int64(e.OutputTokens)
	}
	return out
}

func mergeMemoryKeyCosts(a, b memoryKeyCost) memoryKeyCost {
	return memoryKeyCost{
		SpendUSD:       maxFloat(a.SpendUSD, b.SpendUSD),
		InputSpendUSD:  maxFloat(a.InputSpendUSD, b.InputSpendUSD),
		OutputSpendUSD: maxFloat(a.OutputSpendUSD, b.OutputSpendUSD),
		Requests:       maxInt64(a.Requests, b.Requests),
		InputTokens:    maxInt64(a.InputTokens, b.InputTokens),
		OutputTokens:   maxInt64(a.OutputTokens, b.OutputTokens),
	}
}

func rateUsageForKeyFromSnapshot(snap ratelimit.LimitsSnapshot, rawKey string) []keyRateUsageResponse {
	scopes := []string{RedactScopeKey("key:" + rawKey), "key:" + rawKey}
	out := make([]keyRateUsageResponse, 0, 2)
	for _, spec := range []struct {
		window string
		ws     *ratelimit.WindowSnapshot
	}{
		{"minute", snap.Minute},
		{"day", snap.Day},
	} {
		if spec.ws == nil {
			continue
		}
		counter := counterForScopes(spec.ws.Counters, scopes...)
		if counter.Requests == 0 && counter.Tokens == 0 {
			continue
		}
		out = append(out, keyRateUsageResponse{
			Window:   spec.window,
			Requests: int64(counter.Requests),
			Tokens:   int64(counter.Tokens),
		})
	}
	return out
}

func counterForScopes(counters map[string]ratelimit.CounterSnapshot, scopes ...string) ratelimit.CounterSnapshot {
	var out ratelimit.CounterSnapshot
	for _, scope := range scopes {
		c, ok := counters[scope]
		if !ok {
			continue
		}
		if c.Requests > out.Requests {
			out.Requests = c.Requests
		}
		if c.Tokens > out.Tokens {
			out.Tokens = c.Tokens
		}
	}
	return out
}

func keyCostMonthForKey(
	ctx context.Context,
	store *adminrollup.Store,
	day, masked string,
	todaySpend float64,
	rollupBound bool,
) keyCostMonthResponse {
	month := day[:7]
	out := keyCostMonthResponse{Month: month, Source: "memory"}
	if !rollupBound || store == nil {
		out.SpendUSD = todaySpend
		return out
	}
	spend, err := store.KeyMonthlySpendUSD(ctx, adminrollup.MetricCost, month, masked)
	if err != nil {
		out.Source = "redislive"
		out.SpendUSD = todaySpend
		return out
	}
	out.Source = "redis"
	out.SpendUSD = spend
	if todaySpend > out.SpendUSD {
		out.SpendUSD = todaySpend
		out.Source = "redislive"
	}
	return out
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func mergeKeyPIIStats(mem int64, redis int64, redisOK, rollupBound bool) keyPIIStatsResponse {
	out := keyPIIStatsResponse{Source: "memory", Detections: mem}
	if !rollupBound {
		return out
	}
	if redisOK {
		out.Detections = maxInt64(mem, redis)
	}
	switch {
	case mem > 0:
		out.Source = "redislive"
	case redisOK && redis > 0:
		out.Source = "redis"
	default:
		out.Source = "redislive"
	}
	return out
}

func toDayPoints(series []adminrollup.KeyDayPoint) []keyDayPointResponse {
	out := make([]keyDayPointResponse, len(series))
	for i, pt := range series {
		out[i] = keyDayPointResponse{Day: pt.Day, Value: pt.Value}
	}
	return out
}
