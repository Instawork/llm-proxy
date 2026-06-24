package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
)

type BYOKeyResponse struct {
	Provider     string   `json:"provider"`
	MaskedID     string   `json:"masked_id"`
	Hash         string   `json:"hash"`
	Banned       bool     `json:"banned"`
	BannedBy     string   `json:"banned_by,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	BannedAt     string   `json:"banned_at,omitempty"`
	PIIScans     int64    `json:"pii_scans"`
	CostRequests int64    `json:"cost_requests"`
	SpendUSD     float64  `json:"spend_usd"`
	Sources      []string `json:"sources"`
}

type byoKeyAgg struct {
	provider     string
	maskedID     string
	hash         string
	piiScans     int64
	costRequests int64
	spendUSD     float64
	sources      map[string]struct{}
	banned       *apikeys.BYOBan
}

type nameCountRow struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type costKeyRow struct {
	KeyID    string  `json:"key_id"`
	Requests int64   `json:"requests"`
	SpendUSD float64 `json:"spend_usd"`
}

type piiRecentRow struct {
	Time     int64  `json:"time"`
	Provider string `json:"provider"`
	KeyID    string `json:"key_id"`
}

type costRecentRow struct {
	Time  int64  `json:"time"`
	KeyID string `json:"key_id"`
}

func (h *handler) handleListBYOKeys(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	rows, err := h.collectBYOKeys(r.Context(), provider)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *handler) collectBYOKeys(ctx context.Context, providerFilter string) ([]BYOKeyResponse, error) {
	providerFilter = strings.TrimSpace(providerFilter)
	today := time.Now().UTC().Format("2006-01-02")
	dayStart, dayEnd := utcDayBounds(today)

	aggs := make(map[string]*byoKeyAgg)

	addSeen := func(provider, maskedID, source string, piiScans, costRequests int64, spendUSD float64) {
		maskedID = strings.TrimSpace(maskedID)
		if maskedID == "" || !apikeys.IsBYOMaskedKeyID(maskedID) {
			return
		}
		lookup, provider, err := apikeys.BYOKeyLookup(provider, maskedID)
		if err != nil {
			return
		}
		if providerFilter != "" && provider != providerFilter {
			return
		}
		row := aggs[lookup]
		if row == nil {
			hash, _ := apikeys.ParseCredentialHashFromMaskedID(maskedID)
			row = &byoKeyAgg{
				provider: provider,
				maskedID: maskedID,
				hash:     hash,
				sources:  make(map[string]struct{}),
			}
			aggs[lookup] = row
		}
		row.piiScans += piiScans
		row.costRequests += costRequests
		row.spendUSD += spendUSD
		row.sources[source] = struct{}{}
	}

	if h.deps.PIISummary != nil {
		stats := h.deps.PIISummary()
		for _, row := range decodeRows[nameCountRow](stats["top_keys"]) {
			addSeen("", row.Name, "pii", row.Count, 0, 0)
		}
		for _, row := range decodeRows[piiRecentRow](stats["recent"]) {
			if row.Time > 0 && dayStart > 0 && (row.Time < dayStart || row.Time >= dayEnd) {
				continue
			}
			addSeen(row.Provider, row.KeyID, "pii", 1, 0, 0)
		}
	}

	if h.deps.CostSummary != nil {
		stats := h.deps.CostSummary()
		for _, row := range decodeRows[costKeyRow](stats["by_key"]) {
			addSeen("", row.KeyID, "cost", 0, row.Requests, row.SpendUSD)
		}
		for _, row := range decodeRows[costRecentRow](stats["recent"]) {
			if row.Time > 0 && dayStart > 0 && (row.Time < dayStart || row.Time >= dayEnd) {
				continue
			}
			addSeen("", row.KeyID, "cost", 0, 1, 0)
		}
	}

	bans, err := h.deps.APIKeyStore.ListBYOBans(ctx, providerFilter)
	if err != nil {
		return nil, err
	}
	for _, ban := range bans {
		lookup := ban.Provider + ":" + ban.Hash
		row := aggs[lookup]
		if row == nil {
			row = &byoKeyAgg{
				provider: ban.Provider,
				maskedID: ban.MaskedID,
				hash:     ban.Hash,
				sources:  make(map[string]struct{}),
			}
			aggs[lookup] = row
		}
		row.banned = ban
		row.sources["ban"] = struct{}{}
		if row.maskedID == "" {
			row.maskedID = ban.MaskedID
		}
	}

	out := make([]BYOKeyResponse, 0, len(aggs))
	for _, row := range aggs {
		resp := BYOKeyResponse{
			Provider:     row.provider,
			MaskedID:     row.maskedID,
			Hash:         row.hash,
			PIIScans:     row.piiScans,
			CostRequests: row.costRequests,
			SpendUSD:     row.spendUSD,
			Sources:      sourceList(row.sources),
		}
		if row.banned != nil {
			resp.Banned = true
			resp.BannedBy = row.banned.BannedBy
			resp.Reason = row.banned.Reason
			resp.BannedAt = row.banned.CreatedAt.Format(time.RFC3339)
		}
		out = append(out, resp)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Banned != out[j].Banned {
			return out[i].Banned && !out[j].Banned
		}
		if out[i].PIIScans != out[j].PIIScans {
			return out[i].PIIScans > out[j].PIIScans
		}
		if out[i].SpendUSD != out[j].SpendUSD {
			return out[i].SpendUSD > out[j].SpendUSD
		}
		return out[i].MaskedID < out[j].MaskedID
	})

	return out, nil
}

func decodeRows[T any](raw any) []T {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var rows []T
	if json.Unmarshal(b, &rows) != nil {
		return nil
	}
	return rows
}

func sourceList(sources map[string]struct{}) []string {
	out := make([]string, 0, len(sources))
	for source := range sources {
		out = append(out, source)
	}
	sort.Strings(out)
	return out
}
