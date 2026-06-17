package fuzz

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

type StatusHistogram map[string]int

type ScenarioResult struct {
	Name    string `json:"name"`
	Pass    bool   `json:"pass"`
	Detail  string `json:"detail"`
	Elapsed string `json:"elapsed"`
}

type Report struct {
	mu                sync.Mutex
	Seed              int64             `json:"seed"`
	Scenarios         []ScenarioResult  `json:"scenarios"`
	StatusHistogram   StatusHistogram   `json:"status_histogram"`
	CostLinesDelta    int               `json:"cost_lines_delta"`
	CostLinesExpected int               `json:"cost_lines_expected"`
	CostTotalObserved float64           `json:"cost_total_observed"`
	CostTotalExpected float64           `json:"cost_total_expected"`
	DegradedResponses int               `json:"degraded_responses"`
}

func (r *Report) RecordStatus(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%d", code)
	if code == 0 {
		key = "error"
	}
	if r.StatusHistogram == nil {
		r.StatusHistogram = StatusHistogram{}
	}
	r.StatusHistogram[key]++
}

func (r *Report) RecordError(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.StatusHistogram == nil {
		r.StatusHistogram = StatusHistogram{}
	}
	r.StatusHistogram[kind]++
}

func (r *Report) RecordDegraded() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.DegradedResponses++
}

func (r *Report) AddScenario(res ScenarioResult) {
	r.Scenarios = append(r.Scenarios, res)
}

func (r *Report) Print() {
	fmt.Printf("fuzz seed=%d\n", r.Seed)
	fmt.Printf("status histogram: %v\n", r.StatusHistogram)
	if r.CostLinesExpected > 0 || r.CostLinesDelta > 0 {
		fmt.Printf("cost lines: observed_delta=%d expected_delta=%d total_observed=%.6f total_expected=%.6f\n",
			r.CostLinesDelta, r.CostLinesExpected, r.CostTotalObserved, r.CostTotalExpected)
	}
	if r.DegradedResponses > 0 {
		fmt.Printf("degraded responses: %d\n", r.DegradedResponses)
	}
	for _, s := range r.Scenarios {
		status := "PASS"
		if !s.Pass {
			status = "FAIL"
		}
		fmt.Printf("[%s] %s (%s) %s\n", status, s.Name, s.Elapsed, s.Detail)
	}
}

func (r *Report) WriteJSON(path string) error {
	if path == "" {
		return nil
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func (r *Report) Failed() bool {
	for _, s := range r.Scenarios {
		if !s.Pass {
			return true
		}
	}
	return false
}
