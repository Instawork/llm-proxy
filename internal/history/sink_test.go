package history

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

type fakeS3 struct {
	mu   sync.Mutex
	keys []string
	body [][]byte
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, *in.Key)
	f.body = append(f.body, b)
	return &s3.PutObjectOutput{}, nil
}

func newTestSink(maxRecs int, w chunkWriter) *Sink {
	return newSink(w, Config{
		MaxRecords: maxRecs,
		MaxBytes:   1 << 30,
		MaxAge:     time.Hour,
		Role:       "sidecar",
		InstanceID: "i-test",
		Gzip:       false,
		GzipSet:    true,
	})
}

func TestSink_TimerFlush(t *testing.T) {
	f := &fakeS3{}
	s := newSink(&s3Writer{client: f, bucket: "b", prefix: "llm-proxy"}, Config{
		MaxRecords: 1000,
		MaxBytes:   1 << 30,
		MaxAge:     50 * time.Millisecond,
		Gzip:       false,
		GzipSet:    true,
		Role:       "sidecar",
		InstanceID: "i-test",
	})
	s.Emit(StreamCost, map[string]any{"a": 1})
	require.Len(t, f.keys, 0)
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.keys) >= 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestSink_FlushOnRecordThreshold_S3(t *testing.T) {
	f := &fakeS3{}
	s := newTestSink(2, &s3Writer{client: f, bucket: "b", prefix: "llm-proxy"})
	s.Emit(StreamCost, map[string]any{"a": 1})
	s.Emit(StreamCost, map[string]any{"a": 2})
	require.Len(t, f.keys, 1)
	require.Contains(t, f.keys[0], "/cost/dt=")
	require.Contains(t, f.keys[0], "/hour=")
	require.Equal(t, 2, bytes.Count(f.body[0], []byte("\n")))
}

func TestSink_LocalBackend(t *testing.T) {
	dir := t.TempDir()
	s := newTestSink(1, &fileWriter{dir: dir})
	s.Emit(StreamPII, map[string]any{"b": 2})
	got, err := filepath.Glob(filepath.Join(dir, "pii", "dt=*", "hour=*", "*.jsonl"))
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestSink_CloseFlushesAllStreams(t *testing.T) {
	f := &fakeS3{}
	s := newTestSink(1000, &s3Writer{client: f, bucket: "b", prefix: "llm-proxy"})
	s.Emit(StreamCost, map[string]any{"a": 1})
	s.Emit(StreamPII, map[string]any{"b": 2})
	require.Len(t, f.keys, 0)
	require.NoError(t, s.Close())
	require.Len(t, f.keys, 2)
}

func TestSink_GzipChunk(t *testing.T) {
	f := &fakeS3{}
	s := newSink(&s3Writer{client: f, bucket: "b", prefix: "p"}, Config{
		MaxRecords: 1,
		MaxBytes:   1 << 20,
		MaxAge:     time.Hour,
		Gzip:       true,
		GzipSet:    true,
		Role:       "global",
		InstanceID: "abc",
	})
	s.Emit(StreamUsage, map[string]any{"x": 1})
	require.Len(t, f.keys, 1)
	require.Contains(t, f.keys[0], ".jsonl.gz")
	r, err := gzip.NewReader(bytes.NewReader(f.body[0]))
	require.NoError(t, err)
	raw, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"x"`)
}

func TestSink_ObjectNameUniqueAcrossInstances(t *testing.T) {
	f1 := &fakeS3{}
	f2 := &fakeS3{}
	s1 := newTestSink(1, &s3Writer{client: f1, bucket: "b", prefix: "p"})
	s2 := newTestSink(1, &s3Writer{client: f2, bucket: "b", prefix: "p"})
	s2.role = "global"
	s2.instanceID = "other"
	s1.Emit(StreamCost, map[string]any{"n": 1})
	s2.Emit(StreamCost, map[string]any{"n": 2})
	require.NotEqual(t, f1.keys[0], f2.keys[0])
}

func TestSink_NilEmitNoPanic(t *testing.T) {
	var s *Sink
	s.Emit(StreamCost, map[string]any{"a": 1})
	require.NoError(t, s.Close())
}
