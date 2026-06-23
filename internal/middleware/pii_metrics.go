package middleware

import (
	"time"

	"github.com/Instawork/llm-proxy/internal/observability"
)

func emitPIIRedactionMetrics(
	metrics observability.MetricsSink,
	provider, outcome string,
	entityCounts map[string]int,
	duration time.Duration,
) {
	if metrics == nil {
		return
	}
	tags := []string{
		"provider:" + normalizeDogstatsdTag(provider),
		"outcome:" + normalizeDogstatsdTag(outcome),
	}
	_ = metrics.Incr("pii.redaction", tags, 1.0)
	if duration > 0 {
		_ = metrics.Distribution("pii.redaction.duration_ms", float64(duration.Milliseconds()), tags, 1.0)
	}

	if len(entityCounts) == 0 {
		return
	}
	for entityType, count := range entityCounts {
		if count <= 0 {
			continue
		}
		entityTags := []string{
			"provider:" + normalizeDogstatsdTag(provider),
			"entity_type:" + normalizeDogstatsdTag(entityType),
		}
		for i := 0; i < count; i++ {
			_ = metrics.Incr("pii.entity_detected", entityTags, 1.0)
		}
	}
}

func emitIDGateBlocked(metrics observability.MetricsSink, provider, entityType string) {
	if metrics == nil {
		return
	}
	_ = metrics.Incr("id_gate.blocked", []string{
		"provider:" + normalizeDogstatsdTag(provider),
		"entity_type:" + normalizeDogstatsdTag(entityType),
	}, 1.0)
}

func emitIDGateScanFailed(metrics observability.MetricsSink, provider, stage string, failClosed bool) {
	if metrics == nil {
		return
	}
	outcome := "fail_open"
	if failClosed {
		outcome = "fail_closed"
	}
	_ = metrics.Incr("id_gate.scan_failed", []string{
		"provider:" + normalizeDogstatsdTag(provider),
		"stage:" + normalizeDogstatsdTag(stage),
		"outcome:" + outcome,
	}, 1.0)
}

func emitIDGateScanned(metrics observability.MetricsSink, provider string, imageCount int, duration time.Duration) {
	if metrics == nil {
		return
	}
	tags := []string{"provider:" + normalizeDogstatsdTag(provider)}
	_ = metrics.Incr("id_gate.scanned", tags, 1.0)
	if duration > 0 {
		_ = metrics.Distribution("id_gate.scan.duration_ms", float64(duration.Milliseconds()), tags, 1.0)
	}
	_ = metrics.Distribution("id_gate.images_scanned", float64(imageCount), tags, 1.0)
}

func normalizeDogstatsdTag(v string) string {
	if v == "" {
		return "unknown"
	}
	const maxLen = 200
	if len(v) > maxLen {
		return v[:maxLen]
	}
	return v
}
