package fake

import (
	"io"
	"math/rand"
	"sync"
)

type Outcome int

const (
	OutcomeSuccess Outcome = iota
	Outcome503
	Outcome429
	Outcome500
	OutcomeConnError
)

type Chaos struct {
	enabled     bool
	failureRate float64
	mu          sync.Mutex
	rng         *rand.Rand
}

func NewChaos(enabled bool, failureRate float64, seed int64) *Chaos {
	if seed == 0 {
		seed = rand.Int63()
	}
	return &Chaos{
		enabled:     enabled,
		failureRate: clampRate(failureRate),
		rng:         rand.New(rand.NewSource(seed)),
	}
}

func (c *Chaos) Seed() int64 {
	if c == nil || c.rng == nil {
		return 0
	}
	return c.rng.Int63()
}

func (c *Chaos) Pick(requestRate float64) Outcome {
	rate := c.failureRate
	if requestRate >= 0 {
		rate = clampRate(requestRate)
	}
	if !c.enabled || rate <= 0 {
		return OutcomeSuccess
	}
	c.mu.Lock()
	roll := c.rng.Float64()
	sub := c.rng.Float64()
	c.mu.Unlock()
	if roll >= rate {
		return OutcomeSuccess
	}
	switch {
	case sub < 0.40:
		return Outcome503
	case sub < 0.70:
		return Outcome429
	case sub < 0.90:
		return Outcome500
	default:
		return OutcomeConnError
	}
}

func clampRate(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

var errFakeConn = io.ErrUnexpectedEOF
