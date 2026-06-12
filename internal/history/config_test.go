package history

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/config"
)

func TestNew_BackendNone(t *testing.T) {
	sink, err := New(Config{Backend: "none"})
	require.NoError(t, err)
	require.Nil(t, sink)
}

func TestNew_BackendLocal(t *testing.T) {
	dir := t.TempDir()
	sink, err := New(Config{Backend: "local", LocalDir: dir})
	require.NoError(t, err)
	require.NotNil(t, sink)
	require.NoError(t, sink.Close())
}

func TestNew_BackendS3MissingConfig(t *testing.T) {
	_, err := New(Config{Backend: "s3"})
	require.Error(t, err)
}

func TestConfigFromYAML(t *testing.T) {
	gzip := true
	hc := config.HistoryConfig{
		Backend:       "s3",
		Role:          "global",
		InstanceID:    "x",
		MaxRecords:    100,
		MaxBytes:      4096,
		MaxAgeSeconds: 60,
		Gzip:          &gzip,
		S3:            &config.HistoryS3Config{Bucket: "b", Prefix: "p", Region: "us-east-1"},
	}
	cfg := ConfigFromYAML(hc, nil)
	require.Equal(t, "s3", cfg.Backend)
	require.Equal(t, "global", cfg.Role)
	require.Equal(t, 100, cfg.MaxRecords)
	require.True(t, cfg.Gzip)
	require.Equal(t, "b", cfg.S3Bucket)
}
