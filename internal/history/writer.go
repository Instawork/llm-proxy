package history

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// chunkWriter persists one finished JSONL chunk for a stream.
type chunkWriter interface {
	WriteChunk(ctx context.Context, stream, objectName string, body []byte) error
}

type fileWriter struct {
	dir string
}

func (w *fileWriter) WriteChunk(_ context.Context, stream, name string, body []byte) error {
	now := time.Now().UTC()
	p := filepath.Join(w.dir, filepath.FromSlash(partitionPath(stream, now)))
	if err := os.MkdirAll(p, 0o755); err != nil {
		return fmt.Errorf("history file: mkdir: %w", err)
	}
	path := filepath.Join(p, name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("history file: write: %w", err)
	}
	return nil
}

type putClient interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type s3Writer struct {
	client putClient
	bucket string
	prefix string
}

func (w *s3Writer) WriteChunk(ctx context.Context, stream, name string, body []byte) error {
	now := time.Now().UTC()
	key := fmt.Sprintf("%s/%s/%s", stringsTrimSlash(w.prefix), partitionPath(stream, now), name)
	_, err := w.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(w.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/x-ndjson"),
	})
	if err != nil {
		return fmt.Errorf("history s3: put: %w", err)
	}
	return nil
}

func partitionPath(stream string, now time.Time) string {
	u := now.UTC()
	return fmt.Sprintf("%s/dt=%s/hour=%s", stream, u.Format("2006-01-02"), u.Format("15"))
}

func stringsTrimSlash(s string) string {
	return strings.Trim(strings.TrimSpace(s), "/")
}
