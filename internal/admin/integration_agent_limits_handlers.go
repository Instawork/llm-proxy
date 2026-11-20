package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/gorilla/mux"
)

// Agent spend states, ordered so a min_state filter is a comparison.
const (
	agentSpendStateOK   = "ok"
	agentSpendStateSoft = "soft"
	agentSpendStateHard = "hard"
)

var agentSpendStateRank = map[string]int{
	agentSpendStateOK:   0,
	agentSpendStateSoft: 1,
	agentSpendStateHard: 2,
}

const (
	agentLimitMaxBodyBytes = 4 << 10
	agentSpendMonthLayout  = "2006-01"
	maxUpdatedByLen        = 254
	// agentSpendReadTimeout bounds each Redis and DynamoDB read so a slow
	// store answers well inside Finch's 10 second client timeout.
	agentSpendReadTimeout = 3 * time.Second
)

// agentSpendCents rounds up the same way the reservation script does, so
// the reported state matches the enforcement decision.
func agentSpendCents(spendUSD float64) int64 {
	if spendUSD <= 0 {
		return 0
	}
	return int64(math.Ceil(spendUSD * 100))
}

// agentSpendState compares metered spend with the effective limits. A limit
// of 0 is a $0 limit, reached at once. Exemption does not change the state:
// Finch still sends the soft email for exempt agents and skips the hard one.
func agentSpendState(spendCents int64, e apikeys.EffectiveAgentLimits) string {
	switch {
	case spendCents >= e.HardMonthlyCents:
		return agentSpendStateHard
	case spendCents >= e.SoftMonthlyCents:
		return agentSpendStateSoft
	default:
		return agentSpendStateOK
	}
}

func (h *handler) agentSpendLimitsConfig() config.AgentSpendLimitsConfig {
	if h.deps.YAMLConfig == nil {
		return config.AgentSpendLimitsConfig{}
	}
	return h.deps.YAMLConfig.Features.AgentSpendLimits
}

// agentLimitsReady writes the error and returns false unless the feature is
// enabled and both the limits table and the spend counter are reachable.
func (h *handler) agentLimitsReady(w http.ResponseWriter) (config.AgentSpendLimitsConfig, bool) {
	cfg := h.agentSpendLimitsConfig()
	switch {
	case !cfg.Enabled:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent spend limits are disabled"})
		return cfg, false
	case h.deps.APIKeyStore == nil:
		h.writeAPIKeyStoreUnavailable(w)
		return cfg, false
	case h.deps.AdminRollupStore == nil:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent spend counter unavailable"})
		return cfg, false
	}
	return cfg, true
}

// agentLabel returns the {label} path variable when it is a governed,
// storable label.
func agentLabel(w http.ResponseWriter, r *http.Request, cfg config.AgentSpendLimitsConfig) (string, bool) {
	label := mux.Vars(r)["label"]
	if !cfg.Governs(label) || apikeys.ValidateAgentLabel(label) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("label must start with %q and contain no '/', '|', '#', or whitespace", cfg.UserPrefix)})
		return "", false
	}
	return label, true
}

func currentAgentSpendMonth() string {
	return time.Now().UTC().Format(agentSpendMonthLayout)
}

// loadAgentLimit reads the override straight from the table, not through
// the resolver cache, so an edit is visible on the next read.
func (h *handler) loadAgentLimit(ctx context.Context, label string) (*apikeys.AgentLimit, error) {
	ctx, cancel := context.WithTimeout(ctx, agentSpendReadTimeout)
	defer cancel()
	rec, err := h.deps.APIKeyStore.GetAgentLimit(ctx, label)
	if errors.Is(err, apikeys.ErrAgentLimitNotFound) {
		return nil, nil
	}
	return rec, err
}

func (h *handler) agentMonthSpendCents(ctx context.Context, month, label string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, agentSpendReadTimeout)
	defer cancel()
	usd, err := h.deps.AdminRollupStore.UserMonthlySpendUSD(ctx, adminrollup.MetricCost, month, label)
	if err != nil {
		return 0, err
	}
	return agentSpendCents(usd), nil
}

func agentLimitResponse(cfg config.AgentSpendLimitsConfig, month string, spendCents int64, eff apikeys.EffectiveAgentLimits) IntegrationAgentLimitResponse {
	resp := IntegrationAgentLimitResponse{
		Label:      eff.Label,
		Month:      month,
		Mode:       cfg.Mode,
		SpendCents: spendCents,
		SoftCents:  eff.SoftMonthlyCents,
		HardCents:  eff.HardMonthlyCents,
		Exempt:     eff.Exempt,
		State:      agentSpendState(spendCents, eff),
		Defaults: IntegrationAgentLimitDefaults{
			SoftMonthlyCents:    cfg.DefaultSoftMonthlyCents,
			HardMonthlyCents:    cfg.DefaultHardMonthlyCents,
			MaxHardMonthlyCents: cfg.MaxHardMonthlyCents,
		},
	}
	if o := eff.Override; o != nil {
		resp.Override = &IntegrationAgentLimitOverride{
			SoftMonthlyCents: o.SoftMonthlyCents,
			HardMonthlyCents: o.HardMonthlyCents,
			Exempt:           o.Exempt,
		}
		if o.UpdatedBy != "" {
			resp.Override.UpdatedBy = &o.UpdatedBy
		}
		if !o.UpdatedAt.IsZero() {
			resp.Override.UpdatedAt = &o.UpdatedAt
		}
	}
	return resp
}

// writeAgentLimit answers GET and PUT with the label's current spend.
func (h *handler) writeAgentLimit(w http.ResponseWriter, r *http.Request, cfg config.AgentSpendLimitsConfig, override *apikeys.AgentLimit, label string) {
	month := currentAgentSpendMonth()
	spend, err := h.agentMonthSpendCents(r.Context(), month, label)
	if err != nil {
		h.deps.Logger.Error("admin: integration agent spend read failed", "caller", integrationCallerFrom(r.Context()).id, "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "failed to read agent spend"})
		return
	}
	writeJSON(w, http.StatusOK, agentLimitResponse(cfg, month, spend, apikeys.MergeAgentLimit(cfg, label, override)))
}

func (h *handler) handleIntegrationGetAgentLimit(w http.ResponseWriter, r *http.Request) {
	cfg, ok := h.agentLimitsReady(w)
	if !ok {
		return
	}
	label, ok := agentLabel(w, r, cfg)
	if !ok {
		return
	}
	override, err := h.loadAgentLimit(r.Context(), label)
	if err != nil {
		h.deps.Logger.Error("admin: integration agent limit read failed", "caller", integrationCallerFrom(r.Context()).id, "label", label, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load agent limit"})
		return
	}
	h.writeAgentLimit(w, r, cfg, override, label)
}

// validUpdatedBy checks only that updated_by looks like one email address;
// which Finch users may edit is Finch's decision.
func validUpdatedBy(s string) bool {
	at := strings.IndexByte(s, '@')
	return len(s) <= maxUpdatedByLen && at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t\r\n,;<>")
}

func agentLimitUpdate[T any](defined bool, v *T) apikeys.FieldUpdate[T] {
	switch {
	case !defined:
		return apikeys.FieldUpdate[T]{}
	case v == nil:
		return apikeys.ClearField[T]()
	default:
		return apikeys.SetField(*v)
	}
}

func (h *handler) handleIntegrationPutAgentLimit(w http.ResponseWriter, r *http.Request) {
	cfg, ok := h.agentLimitsReady(w)
	if !ok {
		return
	}
	label, ok := agentLabel(w, r, cfg)
	if !ok {
		return
	}
	caller := integrationCallerFrom(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, agentLimitMaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var body IntegrationPutAgentLimitBody
	if err := dec.Decode(&body); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: fields are soft_monthly_cents, hard_monthly_cents, exempt (int, int, bool, or null) and updated_by"})
		return
	}
	body.UpdatedBy = strings.TrimSpace(body.UpdatedBy)
	if !validUpdatedBy(body.UpdatedBy) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "updated_by must be the editor's email address"})
		return
	}

	// Serialize this process's read-check-write so two edits can't each pass
	// the bounds check against the same old item.
	h.agentLimitWriteMu.Lock()
	defer h.agentLimitWriteMu.Unlock()

	cur, err := h.loadAgentLimit(r.Context(), label)
	if err != nil {
		h.deps.Logger.Error("admin: integration agent limit read failed", "caller", caller.id, "label", label, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load agent limit"})
		return
	}
	var next apikeys.AgentLimit
	if cur != nil {
		next = *cur
	}
	if body.SoftMonthlyCents.Defined {
		next.SoftMonthlyCents = body.SoftMonthlyCents.Value
	}
	if body.HardMonthlyCents.Defined {
		next.HardMonthlyCents = body.HardMonthlyCents.Value
	}
	if body.Exempt.Defined {
		next.Exempt = body.Exempt.Value
	}
	if err := apikeys.MergeAgentLimit(cfg, label, &next).Check(cfg.MaxHardMonthlyCents); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	stored, err := h.deps.APIKeyStore.PutAgentLimit(r.Context(), label, apikeys.AgentLimitUpdate{
		SoftMonthlyCents: agentLimitUpdate(body.SoftMonthlyCents.Defined, body.SoftMonthlyCents.Value),
		HardMonthlyCents: agentLimitUpdate(body.HardMonthlyCents.Defined, body.HardMonthlyCents.Value),
		Exempt:           agentLimitUpdate(body.Exempt.Defined, body.Exempt.Value),
		UpdatedBy:        body.UpdatedBy,
	})
	if err != nil {
		h.deps.Logger.Error("admin: integration agent limit write failed", "caller", caller.id, "label", label, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update agent limit"})
		return
	}
	if h.deps.AgentLimitResolver != nil {
		h.deps.AgentLimitResolver.Invalidate(label)
	}
	h.deps.Logger.Info("admin: integration agent limit updated", "caller", caller.id, "label", label)
	h.writeAgentLimit(w, r, cfg, stored, label)
}

func (h *handler) handleIntegrationListAgentSpend(w http.ResponseWriter, r *http.Request) {
	cfg, ok := h.agentLimitsReady(w)
	if !ok {
		return
	}
	caller := integrationCallerFrom(r.Context())
	q := r.URL.Query()

	month := q.Get("month")
	if month == "" {
		month = currentAgentSpendMonth()
	} else if t, err := time.Parse(agentSpendMonthLayout, month); err != nil || t.Format(agentSpendMonthLayout) != month {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "month must be YYYY-MM"})
		return
	}
	minState := q.Get("min_state")
	if minState == "" {
		minState = agentSpendStateOK
	}
	minRank, ok := agentSpendStateRank[minState]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "min_state must be ok, soft, or hard"})
		return
	}

	spendCtx, cancel := context.WithTimeout(r.Context(), agentSpendReadTimeout)
	spend, err := h.deps.AdminRollupStore.UserMonthlySpendAll(spendCtx, adminrollup.MetricCost, month)
	cancel()
	if err != nil {
		h.deps.Logger.Error("admin: integration agent spend list failed", "caller", caller.id, "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "failed to read agent spend"})
		return
	}
	limitsCtx, cancel := context.WithTimeout(r.Context(), agentSpendReadTimeout)
	overrides, err := h.deps.APIKeyStore.ListAgentLimits(limitsCtx)
	cancel()
	if err != nil {
		h.deps.Logger.Error("admin: integration agent limit list failed", "caller", caller.id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load agent limits"})
		return
	}

	agents := make([]IntegrationAgentSpendEntry, 0)
	for label, usd := range spend {
		if !cfg.Governs(label) || apikeys.ValidateAgentLabel(label) != nil {
			continue
		}
		eff := apikeys.MergeAgentLimit(cfg, label, overrides[label])
		cents := agentSpendCents(usd)
		state := agentSpendState(cents, eff)
		if agentSpendStateRank[state] < minRank {
			continue
		}
		agents = append(agents, IntegrationAgentSpendEntry{
			Label:      label,
			SpendCents: cents,
			SoftCents:  eff.SoftMonthlyCents,
			HardCents:  eff.HardMonthlyCents,
			Exempt:     eff.Exempt,
			State:      state,
		})
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Label < agents[j].Label })
	writeJSON(w, http.StatusOK, IntegrationAgentSpendResponse{Month: month, Mode: cfg.Mode, Agents: agents})
}
