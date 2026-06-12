package adminrollup

// Delta is one instance's additive contribution since its last flush.
type Delta struct {
	Totals     map[string]float64
	Dimensions map[string]map[string]float64 // dim name -> member -> delta
}

func (d Delta) empty() bool {
	if len(d.Totals) > 0 {
		return false
	}
	for _, m := range d.Dimensions {
		if len(m) > 0 {
			return false
		}
	}
	return true
}

// TopNCaps limits high-cardinality dimension rows on read. Zero or negative
// means uncapped (exact).
type TopNCaps struct {
	ByKey  int
	ByUser int
}
