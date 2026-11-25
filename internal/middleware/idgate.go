package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/idgatestats"
	"github.com/Instawork/llm-proxy/internal/observability"
	"github.com/Instawork/llm-proxy/internal/proxylog"
	"github.com/Instawork/llm-proxy/internal/redact"
)

const (
	idGateBlockMessage       = "Security Block: Upload contains a government identity document."
	defaultIDGateCacheTTL    = 5 * time.Minute
	defaultIDGateCacheMax    = 1024
	defaultIDGateMaxScanTime = 60 * time.Second
)

// imageScanResult is the outcome of OCR + analysis for one embedded image.
type imageScanResult struct {
	index      int
	imageBytes int
	blocked    bool
	entityType string
	score      float64
	stage      string
	err        error
}

type uniqueImage struct {
	firstIndex int
	digest     [32]byte
	raw        []byte
}

// idGateVerdict caches only the non-PII classification outcome for an image
// SHA-256 digest — never raw image bytes or extracted OCR text.
type idGateVerdict struct {
	blocked    bool
	entityType string
	score      float64
}

type idGateCacheEntry struct {
	verdict idGateVerdict
	expires time.Time
}

type idGateInflightCall struct {
	done    chan struct{}
	verdict idGateVerdict
	stage   string
	err     error
}

type idGateScanCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	entries    map[[32]byte]idGateCacheEntry
	inflight   map[[32]byte]*idGateInflightCall
}

func newIDGateScanCache(ttl time.Duration, maxEntries int) *idGateScanCache {
	if maxEntries <= 0 {
		maxEntries = defaultIDGateCacheMax
	}
	return &idGateScanCache{
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    make(map[[32]byte]idGateCacheEntry),
		inflight:   make(map[[32]byte]*idGateInflightCall),
	}
}

func (c *idGateScanCache) get(digest [32]byte) (idGateVerdict, bool) {
	if c.ttl <= 0 {
		return idGateVerdict{}, false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[digest]
	if !ok {
		return idGateVerdict{}, false
	}
	if now.After(entry.expires) {
		delete(c.entries, digest)
		return idGateVerdict{}, false
	}
	return entry.verdict, true
}

func (c *idGateScanCache) putLocked(digest [32]byte, v idGateVerdict) {
	if c.ttl <= 0 {
		return
	}
	now := time.Now()
	if len(c.entries) >= c.maxEntries {
		for k, entry := range c.entries {
			if now.After(entry.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= c.maxEntries {
			for k := range c.entries {
				delete(c.entries, k)
				break
			}
		}
	}
	c.entries[digest] = idGateCacheEntry{
		verdict: v,
		expires: now.Add(c.ttl),
	}
}

// do returns a cached verdict if present, joins an identical in-flight scan,
// or runs fn as the leader for digest. Errors are never cached; if a leader is
// canceled by its own request context while a follower is still alive, the
// follower retries rather than inheriting the leader's cancellation.
func (c *idGateScanCache) do(
	ctx context.Context,
	digest [32]byte,
	fn func() (idGateVerdict, string, error),
) (idGateVerdict, string, bool, error) {
	for {
		c.mu.Lock()
		if c.ttl > 0 {
			if entry, ok := c.entries[digest]; ok {
				if time.Now().After(entry.expires) {
					delete(c.entries, digest)
				} else {
					c.mu.Unlock()
					return entry.verdict, "", true, nil
				}
			}
		}
		if existing, ok := c.inflight[digest]; ok {
			c.mu.Unlock()
			select {
			case <-existing.done:
				if existing.err != nil && errors.Is(existing.err, context.Canceled) && ctx.Err() == nil {
					continue
				}
				return existing.verdict, existing.stage, existing.err == nil, existing.err
			case <-ctx.Done():
				return idGateVerdict{}, "ocr", false, ctx.Err()
			}
		}

		call := &idGateInflightCall{done: make(chan struct{})}
		c.inflight[digest] = call
		c.mu.Unlock()

		verdict, stage, err := fn()

		c.mu.Lock()
		call.verdict = verdict
		call.stage = stage
		call.err = err
		if err == nil {
			c.putLocked(digest, verdict)
		}
		if c.inflight[digest] == call {
			delete(c.inflight, digest)
		}
		close(call.done)
		c.mu.Unlock()

		return verdict, stage, false, err
	}
}

// OCRTextExtractor extracts text from a raw image payload.
type OCRTextExtractor interface {
	ExtractText(ctx context.Context, img []byte, filename string) (string, error)
}

// IDSpanAnalyzer returns Presidio spans for OCR'd text scoped to entity types.
type IDSpanAnalyzer interface {
	AnalyzeEntities(ctx context.Context, text string, entityTypes []string) ([]redact.Span, error)
}

// IDGateConfig controls the government-ID security gate middleware.
type IDGateConfig struct {
	FailClosed     bool
	MaxBodyBytes   int
	MaxImageBytes  int
	ScoreThreshold float64
	EntityTypes    []string
	// ImageConcurrency caps how many embedded images in a single request are
	// OCR'd + analyzed in parallel. <=0 defaults to 4.
	ImageConcurrency int
	// MaxInFlight caps concurrent OCR+analyze operations across ALL requests
	// served by this middleware instance. <=0 leaves cross-request concurrency
	// unbounded. Setting this near the OCR fleet's concurrency sweet spot lets
	// excess burst traffic queue in Go (where expired contexts abort before
	// sending work) instead of overloading the OCR sidecar.
	MaxInFlight int
	// ScanTimeout is the per-batch deadline budget for scanning images in a
	// request (typically matched to the OCR client timeout). Multi-image
	// requests scale linearly by batch count up to max(ScanTimeout, 60s).
	// <=0 disables the extra middleware deadline and relies on caller/client
	// deadlines.
	ScanTimeout time.Duration
	// MaxImageDimension downscales JPEG/PNG images whose width or height
	// exceeds this many pixels before sending them to OCR (never mutating the
	// upstream LLM request body). 0 defaults to 1600; <0 disables downscaling.
	MaxImageDimension int
	// CacheTTL caches content-addressed (SHA-256) scan verdicts (clear/blocked
	// only, never errors or raw bytes/text). 0 defaults to 5m; <0 disables
	// cross-request caching.
	CacheTTL time.Duration
	// CacheMaxEntries bounds the in-memory verdict cache. <=0 defaults to 1024.
	CacheMaxEntries int
	Logger          *slog.Logger
	Metrics         observability.MetricsSink
	Recorder        *idgatestats.Recorder
}

// IDGateMiddleware OCRs embedded chat images and blocks requests when Presidio
// detects a government identity document above the configured threshold.
func IDGateMiddleware(ocrClient OCRTextExtractor, analyzer IDSpanAnalyzer, cfg IDGateConfig) func(http.Handler) http.Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	maxBodyBytes := cfg.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = 1024 * 1024
	}
	maxImageBytes := cfg.MaxImageBytes
	if maxImageBytes <= 0 {
		maxImageBytes = 10 * 1024 * 1024
	}
	scoreThreshold := cfg.ScoreThreshold
	if scoreThreshold <= 0 {
		scoreThreshold = 0.4
	}
	entityTypes := cfg.EntityTypes
	if len(entityTypes) == 0 {
		entityTypes = redact.DefaultGovIDEntityTypes
	}
	imageConcurrency := cfg.ImageConcurrency
	if imageConcurrency <= 0 {
		imageConcurrency = 4
	}
	maxImageDim := cfg.MaxImageDimension
	if maxImageDim == 0 {
		maxImageDim = defaultIDGateMaxImageDim
	}
	cacheTTL := cfg.CacheTTL
	if cacheTTL == 0 {
		cacheTTL = defaultIDGateCacheTTL
	}
	scanCache := newIDGateScanCache(cacheTTL, cfg.CacheMaxEntries)

	var globalSem chan struct{}
	if cfg.MaxInFlight > 0 {
		globalSem = make(chan struct{}, cfg.MaxInFlight)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !shouldRedactRequest(r) {
				next.ServeHTTP(w, r)
				return
			}

			body, oversize, err := readBoundedBody(r, maxBodyBytes)
			if err != nil {
				logger.Warn(proxylog.ProxyMsg("id_gate: read body failed"),
					slog.String("path", r.URL.Path),
					slog.String("error", err.Error()))
				next.ServeHTTP(w, r)
				return
			}
			if oversize || len(body) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			images, err := extractImagesFromBody(body, maxImageBytes)
			if err != nil {
				logger.Warn(proxylog.ProxyMsg("id_gate: parse body failed; passing through"),
					slog.String("path", r.URL.Path),
					slog.String("error", err.Error()))
				next.ServeHTTP(w, r)
				return
			}
			if len(images) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			gateStart := time.Now()
			provider := getProviderFromPath(r.URL.Path)
			keyID := idGateDisplayKeyID(r)

			unique := deduplicateImages(images)

			// Fast-path cache check before spawning goroutines or acquiring
			// semaphores. A cached block still short-circuits immediately.
			var toScan []uniqueImage
			var cacheHits atomic.Int64
			for _, u := range unique {
				if verdict, ok := scanCache.get(u.digest); ok {
					cacheHits.Add(1)
					if verdict.blocked {
						gateDuration := time.Since(gateStart)
						logger.Warn(proxylog.ProxyMsg("id_gate: blocked government identity document"),
							slog.String("path", r.URL.Path),
							slog.String("provider", provider),
							slog.Int("image_index", u.firstIndex),
							slog.String("entity_type", verdict.entityType),
							slog.Float64("score", verdict.score),
							slog.Bool("cache_hit", true),
							slog.Duration("duration", gateDuration))
						emitIDGateBlocked(cfg.Metrics, provider, verdict.entityType)
						if cfg.Recorder != nil {
							cfg.Recorder.RecordBlocked(
								provider, keyID, verdict.entityType, verdict.score, u.firstIndex, len(images), gateDuration,
							)
						}
						proxylog.ProxyHTTPError(w, idGateBlockMessage, http.StatusUnprocessableEntity)
						return
					}
					continue
				}
				toScan = append(toScan, u)
			}

			concurrency := min(imageConcurrency, len(toScan))
			if len(toScan) > 0 {
				// Scan remaining unique images concurrently (bounded both
				// per-request and process-wide). A government-ID hit is
				// decisive and short-circuits the rest; errors are collected
				// and only acted on if no image blocks (a block always wins
				// over an error so a failing sibling can't mask a positive).
				var scanCtx context.Context
				var cancelScan context.CancelFunc
				if cfg.ScanTimeout > 0 {
					scanBudget := computeIDGateScanTimeout(len(toScan), concurrency, cfg.ScanTimeout)
					scanCtx, cancelScan = context.WithTimeout(r.Context(), scanBudget)
				} else {
					scanCtx, cancelScan = context.WithCancel(r.Context())
				}
				defer cancelScan()

				reqSem := make(chan struct{}, concurrency)
				results := make(chan imageScanResult, len(toScan))
				var wg sync.WaitGroup

				for _, u := range toScan {
					wg.Add(1)
					go func(u uniqueImage) {
						defer wg.Done()

						select {
						case reqSem <- struct{}{}:
							defer func() { <-reqSem }()
						case <-scanCtx.Done():
							results <- imageScanResult{
								index:      u.firstIndex,
								imageBytes: len(u.raw),
								stage:      "ocr",
								err:        fmt.Errorf("id_gate: request concurrency wait timed out: %w", scanCtx.Err()),
							}
							return
						}

						verdict, stage, hit, scanErr := scanCache.do(scanCtx, u.digest, func() (idGateVerdict, string, error) {
							if globalSem != nil {
								select {
								case globalSem <- struct{}{}:
									defer func() { <-globalSem }()
								case <-scanCtx.Done():
									return idGateVerdict{}, "ocr", fmt.Errorf("id_gate: ocr queue wait timed out: %w", scanCtx.Err())
								}
							}
							if scanCtx.Err() != nil {
								return idGateVerdict{}, "ocr", scanCtx.Err()
							}

							ocrBytes := downscaleImageForOCR(u.raw, maxImageDim)
							text, ocrErr := ocrClient.ExtractText(scanCtx, ocrBytes, imageFilename(u.firstIndex))
							if ocrErr != nil {
								return idGateVerdict{}, "ocr", ocrErr
							}
							if strings.TrimSpace(text) == "" {
								return idGateVerdict{}, "", nil
							}

							spans, analyzeErr := analyzer.AnalyzeEntities(scanCtx, text, entityTypes)
							if analyzeErr != nil {
								return idGateVerdict{}, "analyze", analyzeErr
							}

							if blocked, entityType, score := govIDHit(spans, entityTypes, scoreThreshold); blocked {
								return idGateVerdict{
									blocked:    true,
									entityType: entityType,
									score:      score,
								}, "", nil
							}
							return idGateVerdict{}, "", nil
						})
						if hit {
							cacheHits.Add(1)
						}
						if scanErr != nil {
							results <- imageScanResult{
								index:      u.firstIndex,
								imageBytes: len(u.raw),
								stage:      stage,
								err:        scanErr,
							}
							return
						}
						if verdict.blocked {
							results <- imageScanResult{
								index:      u.firstIndex,
								imageBytes: len(u.raw),
								blocked:    true,
								entityType: verdict.entityType,
								score:      verdict.score,
							}
							cancelScan()
						}
					}(u)
				}

				go func() {
					wg.Wait()
					close(results)
				}()

				var firstErr imageScanResult
				var haveErr bool
				for res := range results {
					if res.blocked {
						gateDuration := time.Since(gateStart)
						logger.Warn(proxylog.ProxyMsg("id_gate: blocked government identity document"),
							slog.String("path", r.URL.Path),
							slog.String("provider", provider),
							slog.Int("image_index", res.index),
							slog.String("entity_type", res.entityType),
							slog.Float64("score", res.score),
							slog.Duration("duration", gateDuration))
						emitIDGateBlocked(cfg.Metrics, provider, res.entityType)
						if cfg.Recorder != nil {
							cfg.Recorder.RecordBlocked(
								provider, keyID, res.entityType, res.score, res.index, len(images), gateDuration,
							)
						}
						proxylog.ProxyHTTPError(w, idGateBlockMessage, http.StatusUnprocessableEntity)
						return
					}
					if res.err != nil && !haveErr {
						firstErr = res
						haveErr = true
					}
				}

				if haveErr {
					gateDuration := time.Since(gateStart)
					if cfg.FailClosed {
						logger.Error(proxylog.ProxyMsg("id_gate: scan failed; FailClosed -> 503"),
							slog.String("path", r.URL.Path),
							slog.String("provider", provider),
							slog.Int("image_index", firstErr.index),
							slog.Int("image_count", len(images)),
							slog.Int("image_bytes", firstErr.imageBytes),
							slog.String("stage", firstErr.stage),
							slog.String("error", firstErr.err.Error()),
							slog.Duration("duration", gateDuration))
						emitIDGateScanFailed(cfg.Metrics, provider, firstErr.stage, true)
						if cfg.Recorder != nil {
							cfg.Recorder.RecordScanFailed(provider, keyID, firstErr.stage, true, len(images), gateDuration)
						}
						proxylog.ProxyHTTPError(w, "service temporarily unavailable", http.StatusServiceUnavailable)
						return
					}
					logger.Warn(proxylog.ProxyMsg("id_gate: scan failed; passing through (fail_open)"),
						slog.String("path", r.URL.Path),
						slog.String("provider", provider),
						slog.Int("image_index", firstErr.index),
						slog.Int("image_count", len(images)),
						slog.Int("image_bytes", firstErr.imageBytes),
						slog.String("stage", firstErr.stage),
						slog.String("error", firstErr.err.Error()),
						slog.Duration("duration", gateDuration))
					emitIDGateScanFailed(cfg.Metrics, provider, firstErr.stage, false)
					if cfg.Recorder != nil {
						cfg.Recorder.RecordScanFailed(provider, keyID, firstErr.stage, false, len(images), gateDuration)
					}
					next.ServeHTTP(w, r)
					return
				}
			}

			gateDuration := time.Since(gateStart)
			logger.Info("id_gate: clear",
				slog.String("path", r.URL.Path),
				slog.String("provider", provider),
				slog.Int("images_scanned", len(images)),
				slog.Int("unique_images", len(unique)),
				slog.Int64("cache_hits", cacheHits.Load()),
				slog.Int("image_concurrency", concurrency),
				slog.Duration("duration", gateDuration))
			emitIDGateScanned(cfg.Metrics, provider, len(images), gateDuration)
			if cfg.Recorder != nil {
				cfg.Recorder.RecordClear(provider, keyID, len(images), gateDuration)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func deduplicateImages(images [][]byte) []uniqueImage {
	seen := make(map[[32]byte]struct{}, len(images))
	out := make([]uniqueImage, 0, len(images))
	for i, img := range images {
		digest := sha256.Sum256(img)
		if _, dup := seen[digest]; dup {
			continue
		}
		seen[digest] = struct{}{}
		out = append(out, uniqueImage{
			firstIndex: i,
			digest:     digest,
			raw:        img,
		})
	}
	return out
}

func computeIDGateScanTimeout(imageCount, concurrency int, base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	batches := max(1, (imageCount+concurrency-1)/concurrency)
	budget := base * time.Duration(batches)
	maxBudget := max(base, defaultIDGateMaxScanTime)
	if budget > maxBudget {
		return maxBudget
	}
	return budget
}

func idGateDisplayKeyID(r *http.Request) string {
	keyRecord, _ := apikeys.FromContext(r.Context())
	if keyRecord != nil && keyRecord.PK != "" {
		return MaskKeyID(keyRecord.PK)
	}
	return InboundCredentialID(r.Context())
}

func govIDHit(spans []redact.Span, entityTypes []string, threshold float64) (bool, string, float64) {
	allowed := make(map[string]struct{}, len(entityTypes))
	for _, e := range entityTypes {
		allowed[e] = struct{}{}
	}
	for _, span := range spans {
		if _, ok := allowed[span.EntityType]; !ok {
			continue
		}
		if span.Score >= threshold {
			return true, span.EntityType, span.Score
		}
	}
	return false, "", 0
}

func extractImagesFromBody(body []byte, maxImageBytes int) ([][]byte, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	var images [][]byte
	collectImages(root, maxImageBytes, &images)
	return images, nil
}

func collectImages(v any, maxImageBytes int, out *[][]byte) {
	switch val := v.(type) {
	case map[string]any:
		if imageURL, ok := val["image_url"].(map[string]any); ok {
			if url, ok := imageURL["url"].(string); ok {
				if img := decodeDataURL(url, maxImageBytes); img != nil {
					*out = append(*out, img)
				}
			}
		}
		if source, ok := val["source"].(map[string]any); ok {
			if typ, _ := source["type"].(string); typ == "base64" {
				if data, ok := source["data"].(string); ok {
					if img := decodeBase64Image(data, maxImageBytes); img != nil {
						*out = append(*out, img)
					}
				}
			}
		}
		if inline, ok := val["inlineData"].(map[string]any); ok {
			if data, ok := inline["data"].(string); ok {
				if img := decodeBase64Image(data, maxImageBytes); img != nil {
					*out = append(*out, img)
				}
			}
		}
		for _, child := range val {
			collectImages(child, maxImageBytes, out)
		}
	case []any:
		for _, item := range val {
			collectImages(item, maxImageBytes, out)
		}
	}
}

func decodeDataURL(raw string, maxImageBytes int) []byte {
	const prefix = "data:"
	if !strings.HasPrefix(raw, prefix) {
		return nil
	}
	comma := strings.Index(raw, ",")
	if comma < 0 {
		return nil
	}
	meta := raw[len(prefix):comma]
	if !strings.Contains(meta, ";base64") {
		return nil
	}
	return decodeBase64Image(raw[comma+1:], maxImageBytes)
}

func decodeBase64Image(encoded string, maxImageBytes int) []byte {
	if encoded == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			return nil
		}
	}
	if len(decoded) == 0 || len(decoded) > maxImageBytes {
		return nil
	}
	return decoded
}

func imageFilename(index int) string {
	return fmt.Sprintf("image-%d.bin", index)
}
