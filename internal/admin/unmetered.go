package admin

import (
	"sort"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
)

type unmeteredEndpointResponse struct {
	Endpoint string `json:"endpoint"`
	Requests int64  `json:"requests"`
}

// unmeteredResponse describes provider requests that produced no token usage
// and therefore no spend: how many, and which endpoints they hit.
type unmeteredResponse struct {
	Source    string                      `json:"source"`
	Requests  int64                       `json:"requests"`
	Endpoints []unmeteredEndpointResponse `json:"endpoints"`
}

func unmeteredSource(snap map[string]interface{}) string {
	if asString(snap["backend"]) == "redis" {
		return "redis"
	}
	return "memory"
}

func unmeteredFromCounts(source string, counts map[string]int64) unmeteredResponse {
	resp := unmeteredResponse{Source: source, Endpoints: []unmeteredEndpointResponse{}}
	for endpoint, n := range counts {
		resp.Requests += n
		resp.Endpoints = append(resp.Endpoints, unmeteredEndpointResponse{Endpoint: endpoint, Requests: n})
	}
	sortUnmeteredEndpoints(resp.Endpoints)
	return resp
}

func sortUnmeteredEndpoints(rows []unmeteredEndpointResponse) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Requests != rows[j].Requests {
			return rows[i].Requests > rows[j].Requests
		}
		return rows[i].Endpoint < rows[j].Endpoint
	})
}

// unmeteredForKey reads one key's endpoint counts from the unmetered snapshot.
func unmeteredForKey(snap map[string]interface{}, masked string) unmeteredResponse {
	byKey, _ := snap["by_key"].(map[string]map[string]int64)
	return unmeteredFromCounts(unmeteredSource(snap), byKey[masked])
}

// unmeteredFleet reads the fleet-wide totals, including requests that carried no proxy key.
func unmeteredFleet(snap map[string]interface{}) unmeteredResponse {
	resp := unmeteredFromCounts(unmeteredSource(snap), adminrollup.NameCountMapFromSnap(snap["by_endpoint"]))
	resp.Requests = adminrollup.SnapInt64(snap["requests_today"])
	return resp
}

// sumUnmetered folds per-key responses into one, for the viewer's own keys.
func sumUnmetered(source string, rows []unmeteredResponse) unmeteredResponse {
	counts := map[string]int64{}
	for _, row := range rows {
		for _, e := range row.Endpoints {
			counts[e.Endpoint] += e.Requests
		}
	}
	return unmeteredFromCounts(source, counts)
}
