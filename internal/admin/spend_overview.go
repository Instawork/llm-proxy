package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/middleware"
)

const (
	spendScopeMine  = "mine"
	spendScopeFleet = "fleet"

	spendCaveatRollupOff            = "rollup_off"
	spendCaveatHistoryTopN          = "history_top_n"
	spendCaveatProviderMonthPartial = "provider_month_partial"
)

type spendCapResponse struct {
	Period string `json:"period"`
	Cents  int64  `json:"cents"`
}

type spendKeyRow struct {
	KeyID       string               `json:"key_id"`
	Provider    string               `json:"provider"`
	Description string               `json:"description"`
	OwnerEmail  string               `json:"owner_email,omitempty"`
	Enabled     bool                 `json:"enabled"`
	Today       keyCostStatsResponse `json:"today"`
	Month       keyCostMonthResponse `json:"month"`
	Cap         *spendCapResponse    `json:"cap,omitempty"`
}

type spendProviderRow struct {
	Name  string               `json:"name"`
	Today keyCostStatsResponse `json:"today"`
	Month keyCostMonthResponse `json:"month"`
}

type spendUserRow struct {
	Scope string               `json:"scope"`
	Today keyCostStatsResponse `json:"today"`
	Month keyCostMonthResponse `json:"month"`
}

type spendDayPoint struct {
	Day      string  `json:"day"`
	SpendUSD float64 `json:"spend_usd"`
}

type spendTotalsResponse struct {
	Today keyCostStatsResponse `json:"today"`
	Month keyCostMonthResponse `json:"month"`
}

type fleetSpendResponse struct {
	Users        []spendUserRow      `json:"users"`
	Unattributed spendTotalsResponse `json:"unattributed"`
}

type spendOverviewResponse struct {
	Scope           string              `json:"scope"`
	Day             string              `json:"day"`
	Month           string              `json:"month"`
	RollupAvailable bool                `json:"rollup_available"`
	RollupBackend   string              `json:"rollup_backend,omitempty"`
	Totals          spendTotalsResponse `json:"totals"`
	Keys            []spendKeyRow       `json:"keys"`
	Providers       []spendProviderRow  `json:"providers"`
	History         []spendDayPoint     `json:"history"`
	Fleet           *fleetSpendResponse `json:"fleet,omitempty"`
	Caveats         []string            `json:"caveats"`
}

type spendKeyLister interface {
	ListKeys(ctx context.Context, provider string) ([]*apikeys.APIKey, error)
	ListKeysByOwner(ctx context.Context, ownerEmail, provider string) ([]*apikeys.APIKey, error)
}

type spendOverviewService struct {
	keys               spendKeyLister
	costSummary        func() map[string]interface{}
	rollup             *adminrollup.Store
	now                func() time.Time
	personalMonthlyCap int64
	logger             *slog.Logger
}

// spendInputs is everything buildSpendOverview needs, gathered by one round of I/O.
type spendInputs struct {
	scope           string
	role            adminusers.Role
	email           string
	day, month      string
	keys            []*apikeys.APIKey
	snap            map[string]interface{}
	rollupBackend   string
	rollupOK        bool
	redisByKey      map[string]adminrollup.KeyCostDayStats
	redisByProvider map[string]adminrollup.KeyCostDayStats
	redisByUser     map[string]adminrollup.KeyCostDayStats
	monthByKey      map[string]float64
	history         []adminrollup.CostDay
	historyDays     int
	personalCap     int64
}

func (s spendOverviewService) Overview(ctx context.Context, role adminusers.Role, email string) (spendOverviewResponse, error) {
	in, err := s.load(ctx, role, email)
	if err != nil {
		return spendOverviewResponse{}, err
	}
	return buildSpendOverview(in), nil
}

func (s spendOverviewService) load(ctx context.Context, role adminusers.Role, email string) (spendInputs, error) {
	now := s.now().UTC()
	in := spendInputs{
		scope:       spendScopeFleet,
		role:        role,
		email:       email,
		day:         now.Format("2006-01-02"),
		month:       now.Format("2006-01"),
		personalCap: s.personalMonthlyCap,
	}
	var err error
	if role == adminusers.RoleViewer {
		in.scope = spendScopeMine
		in.keys, err = s.keys.ListKeysByOwner(ctx, email, "")
	} else {
		in.keys, err = s.keys.ListKeys(ctx, "")
	}
	if err != nil {
		return spendInputs{}, err
	}
	in.snap = safeSummary(s.costSummary)

	if s.rollup == nil {
		return in, nil
	}
	in.rollupBackend = s.rollup.Backend()
	in.historyDays = s.rollup.HistoryDays()
	in.rollupOK = true
	fail := func(what string, err error) {
		s.logger.Error("admin: spend overview "+what+" read failed", "error", err)
		in.rollupOK = false
	}
	if in.redisByKey, err = s.rollup.CostDayByKey(ctx, in.day); err != nil {
		fail("by_key", err)
	}
	if in.monthByKey, err = s.rollup.MonthlySpendByKey(ctx, adminrollup.MetricCost, in.month); err != nil {
		fail("month", err)
	}
	if in.history, err = s.rollup.CostDailyHistory(ctx); err != nil {
		fail("history", err)
	}
	if in.scope == spendScopeFleet {
		if in.redisByProvider, err = s.rollup.CostDayByProvider(ctx, in.day); err != nil {
			fail("by_provider", err)
		}
		if in.redisByUser, err = s.rollup.CostDayByUser(ctx, in.day); err != nil {
			fail("by_user", err)
		}
	}
	return in, nil
}

func buildSpendOverview(in spendInputs) spendOverviewResponse {
	resp := spendOverviewResponse{
		Scope:           in.scope,
		Day:             in.day,
		Month:           in.month,
		RollupAvailable: in.rollupOK,
		RollupBackend:   in.rollupBackend,
		Keys:            []spendKeyRow{},
		Providers:       []spendProviderRow{},
		History:         []spendDayPoint{},
		Caveats:         []string{},
	}
	if !in.rollupOK {
		resp.Caveats = append(resp.Caveats, spendCaveatRollupOff)
	}

	for _, k := range in.keys {
		if k == nil || !canAccessKey(in.role, in.email, k) {
			continue
		}
		masked := middleware.MaskKeyID(k.PK)
		redis, redisOK := in.redisByKey[masked]
		today := keyCostToday(in.snap, masked, k.PK, in.day, redis, redisOK && in.rollupOK, in.rollupOK)
		row := spendKeyRow{
			KeyID:       masked,
			Provider:    k.Provider,
			Description: k.Description,
			Enabled:     k.Enabled,
			Today:       today,
			Month:       keyCostMonth(in.month, in.monthByKey[masked], in.rollupOK, in.rollupOK, today.SpendUSD),
			Cap:         resolveSpendCap(k, in.personalCap),
		}
		if in.scope == spendScopeFleet {
			row.OwnerEmail = k.OwnerEmail
		}
		resp.Keys = append(resp.Keys, row)
	}
	sort.SliceStable(resp.Keys, func(i, j int) bool {
		a, b := resp.Keys[i], resp.Keys[j]
		if a.Month.SpendUSD != b.Month.SpendUSD {
			return a.Month.SpendUSD > b.Month.SpendUSD
		}
		if a.Today.SpendUSD != b.Today.SpendUSD {
			return a.Today.SpendUSD > b.Today.SpendUSD
		}
		return a.KeyID < b.KeyID
	})

	if in.scope == spendScopeMine {
		buildMineSpend(&resp, in)
	} else {
		buildFleetSpend(&resp, in)
	}

	if in.rollupOK {
		for _, cd := range in.history {
			resp.History = append(resp.History, spendDayPoint{Day: cd.Day, SpendUSD: historyDaySpend(cd, in.scope, resp.Keys)})
		}
	}
	resp.History = append(resp.History, spendDayPoint{Day: in.day, SpendUSD: resp.Totals.Today.SpendUSD})
	if in.rollupOK && len(in.history) > 0 && in.scope == spendScopeMine {
		resp.Caveats = append(resp.Caveats, spendCaveatHistoryTopN)
	}
	return resp
}

// buildMineSpend derives providers and totals from the owned key rows: a viewer
// holds at most one personal key per provider, so no fleet dimension is needed.
func buildMineSpend(resp *spendOverviewResponse, in spendInputs) {
	byProvider := map[string]*spendProviderRow{}
	var names []string
	for _, row := range resp.Keys {
		p := byProvider[row.Provider]
		if p == nil {
			p = &spendProviderRow{
				Name:  row.Provider,
				Today: keyCostStatsResponse{Source: row.Today.Source},
				Month: keyCostMonthResponse{Month: in.month, Source: row.Month.Source},
			}
			byProvider[row.Provider] = p
			names = append(names, row.Provider)
		}
		p.Today = addCostStats(p.Today, row.Today)
		p.Month.SpendUSD += row.Month.SpendUSD
		p.Month.Source = weakerSource(p.Month.Source, row.Month.Source)
	}
	sort.Strings(names)
	resp.Totals.Month = keyCostMonthResponse{Month: in.month, Source: "memory"}
	if in.rollupOK {
		resp.Totals.Month.Source = "redis"
	}
	for _, name := range names {
		p := byProvider[name]
		resp.Providers = append(resp.Providers, *p)
		resp.Totals.Today = addCostStats(resp.Totals.Today, p.Today)
		resp.Totals.Month.SpendUSD += p.Month.SpendUSD
		resp.Totals.Month.Source = weakerSource(resp.Totals.Month.Source, p.Month.Source)
	}
	if resp.Totals.Today.Source == "" {
		resp.Totals.Today.Source = "memory"
		if in.rollupOK {
			resp.Totals.Today.Source = "redis"
		}
	}
}

func buildFleetSpend(resp *spendOverviewResponse, in spendInputs) {
	monthDays := historyInMonth(in.history, in.month)
	monthSource := "memory"
	if in.rollupOK {
		monthSource = "redis"
	}

	// Providers: exact today hash max-merged with the in-process row; month is
	// today plus the archived by_provider rows of this month.
	memProviders := map[string]memoryKeyCost{}
	for _, row := range recentRowsFromSnap(in.snap, "by_provider") {
		if name := asString(row["name"]); name != "" {
			memProviders[name] = memoryCostFromRow(row)
		}
	}
	names := unionNames(memProviders, in.redisByProvider)
	for _, name := range names {
		redis, redisOK := in.redisByProvider[name]
		today := mergeKeyCostStats(memProviders[name], redis, redisOK && in.rollupOK, in.rollupOK)
		month := keyCostMonthResponse{Month: in.month, Source: monthSource, SpendUSD: today.SpendUSD}
		for _, cd := range monthDays {
			month.SpendUSD += cd.ByProvider[name].SpendUSD
		}
		resp.Providers = append(resp.Providers, spendProviderRow{Name: name, Today: today, Month: month})
		resp.Totals.Today = addCostStats(resp.Totals.Today, today)
	}
	sort.SliceStable(resp.Providers, func(i, j int) bool {
		return resp.Providers[i].Month.SpendUSD > resp.Providers[j].Month.SpendUSD
	})
	if resp.Totals.Today.Source == "" {
		resp.Totals.Today.Source = monthSource
	}
	if in.rollupOK && in.historyDays < dayOfMonth(in.day)-1 {
		resp.Caveats = append(resp.Caveats, spendCaveatProviderMonthPartial)
	}

	// Month total is the exact monthly hash over every key ever seen this month.
	var monthSum float64
	for _, v := range in.monthByKey {
		monthSum += v
	}
	resp.Totals.Month = keyCostMonth(in.month, monthSum, in.rollupOK, in.rollupOK, resp.Totals.Today.SpendUSD)

	// Users: request-scoped user ids, not key owners.
	memUsers := map[string]memoryKeyCost{}
	for scope, row := range mapRowsFromSnap(in.snap, "by_user") {
		memUsers[scope] = memoryCostFromRow(row)
	}
	users := []spendUserRow{}
	for _, scope := range unionNames(memUsers, in.redisByUser) {
		redis, redisOK := in.redisByUser[scope]
		today := mergeKeyCostStats(memUsers[scope], redis, redisOK && in.rollupOK, in.rollupOK)
		month := keyCostMonthResponse{Month: in.month, Source: monthSource, SpendUSD: today.SpendUSD}
		for _, cd := range monthDays {
			month.SpendUSD += cd.ByUserSpend[scope]
		}
		users = append(users, spendUserRow{Scope: scope, Today: today, Month: month})
	}
	sort.SliceStable(users, func(i, j int) bool {
		return users[i].Month.SpendUSD > users[j].Month.SpendUSD
	})
	if in.rollupOK && len(monthDays) > 0 && len(users) > 0 {
		resp.Caveats = append(resp.Caveats, spendCaveatHistoryTopN)
	}

	var keysToday keyCostStatsResponse
	var keysMonth float64
	for _, row := range resp.Keys {
		keysToday = addCostStats(keysToday, row.Today)
		keysMonth += row.Month.SpendUSD
	}
	resp.Fleet = &fleetSpendResponse{
		Users: users,
		Unattributed: spendTotalsResponse{
			Today: subtractCostStats(resp.Totals.Today, keysToday),
			Month: keyCostMonthResponse{
				Month:    in.month,
				Source:   resp.Totals.Month.Source,
				SpendUSD: maxFloat(0, resp.Totals.Month.SpendUSD-keysMonth),
			},
		},
	}
}

// resolveSpendCap picks the cap the key is actually enforced against:
// explicit monthly, then explicit daily, then the personal-key default.
func resolveSpendCap(k *apikeys.APIKey, personalMonthlyCents int64) *spendCapResponse {
	switch {
	case k.MonthlyCostLimit > 0:
		return &spendCapResponse{Period: "monthly", Cents: k.MonthlyCostLimit}
	case k.DailyCostLimit > 0:
		return &spendCapResponse{Period: "daily", Cents: k.DailyCostLimit}
	case apikeys.IsPersonalKey(k) && personalMonthlyCents > 0:
		return &spendCapResponse{Period: "monthly", Cents: personalMonthlyCents}
	default:
		return nil
	}
}

func historyInMonth(history []adminrollup.CostDay, month string) []adminrollup.CostDay {
	out := make([]adminrollup.CostDay, 0, len(history))
	for _, cd := range history {
		if strings.HasPrefix(cd.Day, month) {
			out = append(out, cd)
		}
	}
	return out
}

func historyDaySpend(cd adminrollup.CostDay, scope string, keys []spendKeyRow) float64 {
	if scope == spendScopeFleet {
		return cd.Totals.SpendUSD
	}
	var sum float64
	for _, row := range keys {
		sum += cd.ByKeySpend[row.KeyID]
	}
	return sum
}

func dayOfMonth(day string) int {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return 0
	}
	return t.Day()
}

func unionNames(mem map[string]memoryKeyCost, redis map[string]adminrollup.KeyCostDayStats) []string {
	seen := map[string]struct{}{}
	for name := range mem {
		seen[name] = struct{}{}
	}
	for name := range redis {
		seen[name] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func mapRowsFromSnap(snap map[string]interface{}, field string) map[string]map[string]interface{} {
	raw, ok := snap[field]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var rows map[string]map[string]interface{}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil
	}
	return rows
}

func addCostStats(a, b keyCostStatsResponse) keyCostStatsResponse {
	return keyCostStatsResponse{
		Source:         weakerSource(a.Source, b.Source),
		SpendUSD:       a.SpendUSD + b.SpendUSD,
		InputSpendUSD:  a.InputSpendUSD + b.InputSpendUSD,
		OutputSpendUSD: a.OutputSpendUSD + b.OutputSpendUSD,
		Requests:       a.Requests + b.Requests,
		InputTokens:    a.InputTokens + b.InputTokens,
		OutputTokens:   a.OutputTokens + b.OutputTokens,
	}
}

func subtractCostStats(total, part keyCostStatsResponse) keyCostStatsResponse {
	return keyCostStatsResponse{
		Source:         total.Source,
		SpendUSD:       maxFloat(0, total.SpendUSD-part.SpendUSD),
		InputSpendUSD:  maxFloat(0, total.InputSpendUSD-part.InputSpendUSD),
		OutputSpendUSD: maxFloat(0, total.OutputSpendUSD-part.OutputSpendUSD),
		Requests:       maxInt64(0, total.Requests-part.Requests),
		InputTokens:    maxInt64(0, total.InputTokens-part.InputTokens),
		OutputTokens:   maxInt64(0, total.OutputTokens-part.OutputTokens),
	}
}

// weakerSource labels an aggregate by its least authoritative component:
// memory < redislive < redis. An empty side defers to the other.
func weakerSource(a, b string) string {
	rank := func(s string) int {
		switch s {
		case "memory":
			return 0
		case "redislive":
			return 1
		case "redis":
			return 2
		default:
			return 3
		}
	}
	if rank(a) <= rank(b) {
		return a
	}
	return b
}

func (h *handler) spendOverviewService() spendOverviewService {
	return spendOverviewService{
		keys:               h.deps.APIKeyStore,
		costSummary:        h.deps.CostSummary,
		rollup:             h.deps.AdminRollupStore,
		now:                time.Now,
		personalMonthlyCap: h.viewerPersonalMonthlyLimit(),
		logger:             h.deps.Logger,
	}
}

func (h *handler) handleSpendOverview(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
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
	resp, err := h.spendOverviewService().Overview(r.Context(), role, user.Email)
	if err != nil {
		h.deps.Logger.Error("admin: spend overview failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load spend overview"})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
