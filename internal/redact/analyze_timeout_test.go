package redact

import (
	"testing"
	"time"
)

func TestComputeAnalyzeTimeout_BaseOnly(t *testing.T) {
	got := ComputeAnalyzeTimeout(50_000, AnalyzeTimeoutConfig{Base: 8 * time.Second})
	if got != 8*time.Second {
		t.Fatalf("got %v, want 8s", got)
	}
}

func TestComputeAnalyzeTimeout_ScalesWithBodySize(t *testing.T) {
	// 434339 bytes ≈ 4 × 100KiB chunks → 8s + 4×2s = 16s
	got := ComputeAnalyzeTimeout(434_339, AnalyzeTimeoutConfig{
		Base:      8 * time.Second,
		Per100KiB: 2 * time.Second,
	})
	if got != 16*time.Second {
		t.Fatalf("got %v, want 16s", got)
	}
}

func TestComputeAnalyzeTimeout_CapsAtMax(t *testing.T) {
	got := ComputeAnalyzeTimeout(2_000_000, AnalyzeTimeoutConfig{
		Base:      8 * time.Second,
		Per100KiB: 2 * time.Second,
		Max:       20 * time.Second,
	})
	if got != 20*time.Second {
		t.Fatalf("got %v, want 20s cap", got)
	}
}

func TestComputeAnalyzeTimeout_DefaultMaxWhenUnset(t *testing.T) {
	got := ComputeAnalyzeTimeout(2_000_000, AnalyzeTimeoutConfig{
		Base:      8 * time.Second,
		Per100KiB: 2 * time.Second,
	})
	if got != defaultAnalyzeTimeoutMax {
		t.Fatalf("got %v, want default max %v", got, defaultAnalyzeTimeoutMax)
	}
}

func TestAnalyzeTimeoutFromContext(t *testing.T) {
	ctx := WithAnalyzeTimeout(t.Context(), 12*time.Second)
	if got := AnalyzeTimeoutFromContext(ctx, 3*time.Second); got != 12*time.Second {
		t.Fatalf("got %v, want 12s from context", got)
	}
	if got := AnalyzeTimeoutFromContext(t.Context(), 3*time.Second); got != 3*time.Second {
		t.Fatalf("got %v, want 3s fallback", got)
	}
}
