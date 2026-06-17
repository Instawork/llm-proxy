package fuzz

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"strings"
)

const degradedSignal = "[LLM_PROXY_PROVIDER_DEGRADED]"

type CostRecord struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalCost    float64 `json:"total_cost"`
	IsEstimate   bool    `json:"is_estimate"`
	MatchedModel string  `json:"matched_model"`
}

func CountLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n, sc.Err()
}

func ReadNewRecords(path string, before int) ([]CostRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []CostRecord
	idx := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		idx++
		if idx <= before {
			continue
		}
		var rec CostRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

func ResetCostFile(path string) error {
	return os.WriteFile(path, nil, 0o644)
}

func RoundUpCost(v float64) float64 {
	return math.Ceil(v*10000) / 10000
}

func ExpectedOpenAICost(inputTokens, outputTokens int) float64 {
	const inputPerM = 0.15
	const outputPerM = 0.60
	in := RoundUpCost(float64(inputTokens) / 1_000_000 * inputPerM)
	out := RoundUpCost(float64(outputTokens) / 1_000_000 * outputPerM)
	return RoundUpCost(in + out)
}

func SumCost(recs []CostRecord) float64 {
	var total float64
	for _, r := range recs {
		total += r.TotalCost
	}
	return RoundUpCost(total)
}
