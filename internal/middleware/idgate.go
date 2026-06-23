package middleware

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/observability"
	"github.com/Instawork/llm-proxy/internal/redact"
)

const idGateBlockMessage = "Security Block: Upload contains a government identity document."

// imageScanResult is the outcome of OCR + analysis for one embedded image.
type imageScanResult struct {
	index      int
	blocked    bool
	entityType string
	score      float64
	stage      string
	err        error
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
	// OCR'd + analyzed in parallel. <=0 defaults to 4. Throughput across the
	// OCR fleet is what makes this safe to raise; the sidecar bounds its own
	// per-process load.
	ImageConcurrency int
	Logger           *slog.Logger
	Metrics          observability.MetricsSink
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

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !shouldRedactRequest(r) {
				next.ServeHTTP(w, r)
				return
			}

			body, oversize, err := readBoundedBody(r, maxBodyBytes)
			if err != nil {
				logger.Warn("id_gate: read body failed",
					slog.String("path", r.URL.Path),
					slog.String("error", err.Error()))
				next.ServeHTTP(w, r)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))

			if oversize || len(body) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			images, err := extractImagesFromBody(body, maxImageBytes)
			if err != nil {
				logger.Warn("id_gate: parse body failed; passing through",
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

			// Scan all embedded images concurrently (bounded). A government-ID
			// hit is decisive and short-circuits the rest; errors are collected
			// and only acted on if no image blocks (a block always wins over an
			// error so a failing sibling can't mask a real positive).
			concurrency := imageConcurrency
			if concurrency > len(images) {
				concurrency = len(images)
			}

			scanCtx, cancelScan := context.WithCancel(r.Context())
			defer cancelScan()

			sem := make(chan struct{}, concurrency)
			results := make(chan imageScanResult, len(images))
			var wg sync.WaitGroup

			for i, img := range images {
				wg.Add(1)
				go func(index int, img []byte) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					if scanCtx.Err() != nil {
						return
					}

					text, ocrErr := ocrClient.ExtractText(scanCtx, img, imageFilename(index))
					if ocrErr != nil {
						results <- imageScanResult{index: index, stage: "ocr", err: ocrErr}
						return
					}
					if strings.TrimSpace(text) == "" {
						return
					}

					spans, analyzeErr := analyzer.AnalyzeEntities(scanCtx, text, entityTypes)
					if analyzeErr != nil {
						results <- imageScanResult{index: index, stage: "analyze", err: analyzeErr}
						return
					}

					if blocked, entityType, score := govIDHit(spans, entityTypes, scoreThreshold); blocked {
						results <- imageScanResult{
							index:      index,
							blocked:    true,
							entityType: entityType,
							score:      score,
						}
						cancelScan()
					}
				}(i, img)
			}

			go func() {
				wg.Wait()
				close(results)
			}()

			var firstErr imageScanResult
			var haveErr bool
			for res := range results {
				if res.blocked {
					logger.Warn("id_gate: blocked government identity document",
						slog.String("path", r.URL.Path),
						slog.String("provider", provider),
						slog.Int("image_index", res.index),
						slog.String("entity_type", res.entityType),
						slog.Float64("score", res.score),
						slog.Duration("duration", time.Since(gateStart)))
					emitIDGateBlocked(cfg.Metrics, provider, res.entityType)
					http.Error(w, idGateBlockMessage, http.StatusForbidden)
					return
				}
				if res.err != nil && !haveErr {
					firstErr = res
					haveErr = true
				}
			}

			if haveErr {
				if cfg.FailClosed {
					logger.Error("id_gate: scan failed; FailClosed -> 503",
						slog.String("path", r.URL.Path),
						slog.String("provider", provider),
						slog.Int("image_index", firstErr.index),
						slog.String("stage", firstErr.stage),
						slog.String("error", firstErr.err.Error()),
						slog.Duration("duration", time.Since(gateStart)))
					emitIDGateScanFailed(cfg.Metrics, provider, firstErr.stage, true)
					http.Error(w, "service temporarily unavailable", http.StatusServiceUnavailable)
					return
				}
				logger.Warn("id_gate: scan failed; passing through (fail_open)",
					slog.String("path", r.URL.Path),
					slog.String("provider", provider),
					slog.Int("image_index", firstErr.index),
					slog.String("stage", firstErr.stage),
					slog.String("error", firstErr.err.Error()))
				emitIDGateScanFailed(cfg.Metrics, provider, firstErr.stage, false)
				next.ServeHTTP(w, r)
				return
			}

			gateDuration := time.Since(gateStart)
			logger.Info("id_gate: clear",
				slog.String("path", r.URL.Path),
				slog.String("provider", provider),
				slog.Int("images_scanned", len(images)),
				slog.Int("image_concurrency", concurrency),
				slog.Duration("duration", gateDuration))
			emitIDGateScanned(cfg.Metrics, provider, len(images), gateDuration)
			next.ServeHTTP(w, r)
		})
	}
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
