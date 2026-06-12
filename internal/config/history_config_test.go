package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestHistoryConfig_Unmarshal(t *testing.T) {
	raw := `
features:
  history:
    backend: local
    role: sidecar
    streams: [cost, pii]
    max_records: 500
    max_age_seconds: 300
    gzip: true
    local:
      dir: logs/history
`
	var cfg YAMLConfig
	require.NoError(t, yaml.Unmarshal([]byte(raw), &cfg))
	require.Equal(t, "local", cfg.Features.History.Backend)
	require.Equal(t, "sidecar", cfg.Features.History.Role)
	require.Equal(t, 500, cfg.Features.History.MaxRecords)
	require.Equal(t, 300, cfg.Features.History.MaxAgeSeconds)
	require.NotNil(t, cfg.Features.History.Gzip)
	require.True(t, *cfg.Features.History.Gzip)
	require.Equal(t, "logs/history", cfg.Features.History.Local.Dir)
}
