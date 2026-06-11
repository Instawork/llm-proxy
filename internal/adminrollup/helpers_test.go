package adminrollup

import (
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/stretchr/testify/require"
)

func TestConfigFromYAML(t *testing.T) {
	admin := config.AdminDashboardConfig{Enabled: true}
	admin.Rollups.Enabled = true
	admin.Rollups.Backend = "redis"
	admin.Rollups.RetentionDays = 90
	admin.Rollups.HistoryDays = 30

	cfg := ConfigFromYAML(admin)
	require.True(t, cfg.Enabled)
	require.Equal(t, "redis", cfg.Backend)
	require.Equal(t, 90, cfg.RetentionDays)
	require.Equal(t, 30, cfg.HistoryDays)

	// Disabled dashboard forces rollups off even when rollups.enabled is true.
	admin.Enabled = false
	require.False(t, ConfigFromYAML(admin).Enabled)
}

func TestChartDaysSorted(t *testing.T) {
	rows := []map[string]interface{}{
		{"day": "2026-06-11"},
		{"day": "2026-06-09"},
		{"no_day": 1}, // skipped
		{"day": "2026-06-10"},
	}
	require.Equal(t, []string{"2026-06-09", "2026-06-10", "2026-06-11"}, ChartDays(rows))
}

func TestFloatField(t *testing.T) {
	row := map[string]interface{}{
		"f": float64(1.5),
		"i": 2,
		"l": int64(3),
		"s": "nope",
	}
	require.Equal(t, 1.5, FloatField(row, "f"))
	require.Equal(t, 2.0, FloatField(row, "i"))
	require.Equal(t, 3.0, FloatField(row, "l"))
	require.Equal(t, 0.0, FloatField(row, "s"))
	require.Equal(t, 0.0, FloatField(row, "missing"))
}

func TestNormalizeDay(t *testing.T) {
	require.Equal(t, "2026-06-11", NormalizeDay("  2026-06-11 "))
}

func TestHistoryDaysNilSafe(t *testing.T) {
	var s *Store
	require.Equal(t, 0, s.HistoryDays())
}
