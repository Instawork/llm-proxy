package history

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/Instawork/llm-proxy/internal/config"
)

// Config holds resolved history sink settings.
type Config struct {
	Backend    string
	Role       string
	InstanceID string
	MaxRecords int
	MaxBytes   int
	MaxAge     time.Duration
	Gzip       bool
	GzipSet    bool
	Logger     *slog.Logger

	LocalDir   string
	S3Bucket   string
	S3Prefix   string
	S3Region   string
	S3Endpoint string
}

// ConfigFromYAML maps YAML history config into a Config.
func ConfigFromYAML(hc config.HistoryConfig, logger *slog.Logger) Config {
	cfg := Config{
		Backend:    strings.ToLower(strings.TrimSpace(hc.Backend)),
		Role:       strings.TrimSpace(hc.Role),
		InstanceID: strings.TrimSpace(hc.InstanceID),
		MaxRecords: hc.MaxRecords,
		MaxBytes:   hc.MaxBytes,
		Logger:     logger,
	}
	if hc.MaxAgeSeconds > 0 {
		cfg.MaxAge = time.Duration(hc.MaxAgeSeconds) * time.Second
	}
	if hc.Gzip != nil {
		cfg.Gzip = *hc.Gzip
		cfg.GzipSet = true
	}
	if hc.Local != nil {
		cfg.LocalDir = hc.Local.Dir
	}
	if hc.S3 != nil {
		cfg.S3Bucket = hc.S3.Bucket
		cfg.S3Prefix = hc.S3.Prefix
		cfg.S3Region = hc.S3.Region
		cfg.S3Endpoint = hc.S3.EndpointURL
		if cfg.S3Endpoint == "" {
			cfg.S3Endpoint = os.Getenv("AWS_ENDPOINT_URL")
		}
	}
	return cfg
}

// New builds a Sink from config. Returns (nil, nil) when backend is none/empty.
func New(cfg Config) (*Sink, error) {
	switch cfg.Backend {
	case "", "none":
		return nil, nil
	case "local":
		dir := cfg.LocalDir
		if dir == "" {
			dir = "logs/history"
		}
		return newSink(&fileWriter{dir: dir}, cfg), nil
	case "s3":
		if cfg.S3Bucket == "" || cfg.S3Region == "" {
			return nil, fmt.Errorf("history s3 backend requires bucket and region")
		}
		client, err := newS3Client(cfg)
		if err != nil {
			return nil, err
		}
		prefix := cfg.S3Prefix
		if prefix == "" {
			prefix = "llm-proxy"
		}
		return newSink(&s3Writer{client: client, bucket: cfg.S3Bucket, prefix: prefix}, cfg), nil
	default:
		return nil, fmt.Errorf("history: unknown backend %q", cfg.Backend)
	}
}

func newS3Client(cfg Config) (putClient, error) {
	startupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	awsCfg, err := awsconfig.LoadDefaultConfig(startupCtx, awsconfig.WithRegion(cfg.S3Region))
	if err != nil {
		return nil, fmt.Errorf("history s3: load aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
			o.UsePathStyle = true
		}
	})
	return client, nil
}

// StreamEnabled reports whether stream should receive events given the
// configured allow-list (empty means all standard streams).
func StreamEnabled(streams []string, stream string) bool {
	if len(streams) == 0 {
		return true
	}
	for _, s := range streams {
		if strings.EqualFold(strings.TrimSpace(s), stream) {
			return true
		}
	}
	return false
}
