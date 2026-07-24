package history

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultMaxRecords = 1000
	defaultMaxBytes   = 8 << 20 // 8 MiB
	defaultMaxAge     = 5 * time.Minute
	uploadTimeout     = 30 * time.Second
)

// Stream names for row history.
const (
	StreamCost      = "cost"
	StreamPII       = "pii"
	StreamIDGate    = "id_gate"
	StreamUsage     = "usage"
	StreamRateLimit = "ratelimit"
)

type streamBuf struct {
	lines [][]byte
	bytes int
}

// Sink buffers events per stream and writes gzipped JSONL chunks to the
// configured backend (local file or S3).
type Sink struct {
	writer   chunkWriter
	gzip     bool
	maxRecs  int
	maxBytes int
	maxAge   time.Duration
	logger   *slog.Logger

	role       string
	instanceID string
	seq        atomic.Uint64

	mu      sync.Mutex
	streams map[string]*streamBuf

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Emit serializes one event onto the named stream's buffer and uploads
// inline if a size threshold trips.
func (s *Sink) Emit(stream string, event any) {
	if s == nil {
		return
	}
	line, err := json.Marshal(event)
	if err != nil {
		s.logger.Debug("history: marshal failed", "stream", stream, "error", err)
		return
	}
	s.mu.Lock()
	sb := s.streams[stream]
	if sb == nil {
		sb = &streamBuf{}
		s.streams[stream] = sb
	}
	sb.lines = append(sb.lines, line)
	sb.bytes += len(line) + 1
	full := len(sb.lines) >= s.maxRecs || sb.bytes >= s.maxBytes
	batch := takeLocked(sb, full)
	s.mu.Unlock()

	if batch != nil {
		// Bound the inline upload the same way FlushAll does: Emit runs on
		// caller goroutines (often request paths), and an S3 endpoint that
		// stops responding must not pin them indefinitely.
		ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
		defer cancel()
		_ = s.upload(ctx, stream, batch)
	}
}

func takeLocked(sb *streamBuf, do bool) [][]byte {
	if !do || len(sb.lines) == 0 {
		return nil
	}
	b := sb.lines
	sb.lines, sb.bytes = nil, 0
	return b
}

// FlushAll uploads every stream's buffer.
func (s *Sink) FlushAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	pending := make(map[string][][]byte)
	for name, sb := range s.streams {
		if b := takeLocked(sb, true); b != nil {
			pending[name] = b
		}
	}
	s.mu.Unlock()
	for name, batch := range pending {
		ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
		_ = s.upload(ctx, name, batch)
		cancel()
	}
}

// Close stops the timer and flushes all remaining buffers.
func (s *Sink) Close() error {
	if s == nil {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	s.FlushAll()
	return nil
}

func (s *Sink) upload(ctx context.Context, stream string, lines [][]byte) error {
	var raw bytes.Buffer
	for _, l := range lines {
		raw.Write(l)
		raw.WriteByte('\n')
	}
	body := raw.Bytes()
	if s.gzip {
		var gz bytes.Buffer
		w := gzip.NewWriter(&gz)
		if _, err := w.Write(body); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		body = gz.Bytes()
	}
	name := s.objectName(time.Now().UTC())
	if err := s.writer.WriteChunk(ctx, stream, name, body); err != nil {
		s.logger.Warn("history: chunk write failed", "stream", stream, "name", name, "records", len(lines), "error", err)
		return err
	}
	s.logger.Debug("history: wrote chunk", "stream", stream, "name", name, "records", len(lines), "bytes", len(body))
	return nil
}

func (s *Sink) objectName(now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	seq := s.seq.Add(1)
	name := fmt.Sprintf("%s-%s-%s-%06d-%s.jsonl",
		now.UTC().Format("20060102T150405.000Z"),
		s.role, s.instanceID, seq, hex.EncodeToString(b[:]))
	if s.gzip {
		name += ".gz"
	}
	return name
}

func (s *Sink) startFlushLoop() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(s.maxAge)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.FlushAll()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// NewWithWriter constructs a sink with an injected chunk writer (tests).
func NewWithWriter(writer chunkWriter, cfg Config) *Sink {
	return newSink(writer, cfg)
}

func newSink(writer chunkWriter, cfg Config) *Sink {
	maxRecs := cfg.MaxRecords
	if maxRecs <= 0 {
		maxRecs = defaultMaxRecords
	}
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = defaultMaxAge
	}
	gzipOn := cfg.Gzip
	if !cfg.GzipSet {
		gzipOn = true
	}
	role := cfg.Role
	if role == "" {
		role = "sidecar"
	}
	s := &Sink{
		writer:     writer,
		gzip:       gzipOn,
		maxRecs:    maxRecs,
		maxBytes:   maxBytes,
		maxAge:     maxAge,
		logger:     cfg.Logger,
		role:       role,
		instanceID: ResolveInstanceID(cfg.InstanceID),
		streams:    make(map[string]*streamBuf),
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	s.startFlushLoop()
	return s
}
