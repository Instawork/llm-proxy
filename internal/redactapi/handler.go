package redactapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/proxylog"
	"github.com/Instawork/llm-proxy/internal/redact"
)

const defaultMaxBodyBytes = 1024 * 1024

// Redactor is the subset of redact.Redactor used by POST /redact.
type Redactor interface {
	Redact(ctx context.Context, text string) (redact.Result, error)
}

// ProxyKeyLookup validates inbound iw-* bearer tokens.
type ProxyKeyLookup interface {
	LookupProxyKey(ctx context.Context, bearer string) (*apikeys.APIKey, error)
}

// Config controls POST /redact runtime behaviour.
type Config struct {
	MaxBodyBytes         int
	AllowUnauthenticated bool
}

// Handler serves POST /redact.
type Handler struct {
	redactor Redactor
	keys     ProxyKeyLookup
	cfg      Config
	logger   *slog.Logger
}

// NewHandler constructs the /redact HTTP handler.
func NewHandler(redactor Redactor, keys ProxyKeyLookup, cfg Config, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	maxBytes := cfg.MaxBodyBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBodyBytes
	}
	cfg.MaxBodyBytes = maxBytes
	return &Handler{redactor: redactor, keys: keys, cfg: cfg, logger: logger}
}

type jsonRequest struct {
	Text string `json:"text"`
}

type jsonResponse struct {
	Text     string         `json:"text"`
	Entities map[string]int `json:"entities"`
	Changed  bool           `json:"changed"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.redactor == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "redaction unavailable")
		return
	}

	if !h.cfg.AllowUnauthenticated {
		if h.keys == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "api key store unavailable")
			return
		}

		bearer := extractCredential(r)
		if bearer == "" || !apikeys.HasKeyPrefix(bearer) {
			writeJSONError(w, http.StatusUnauthorized, "missing or invalid API key")
			return
		}
		if _, err := h.keys.LookupProxyKey(r.Context(), bearer); err != nil {
			writeJSONError(w, http.StatusUnauthorized, "invalid API key")
			return
		}
	}

	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode == "" {
		mode = "text"
	}
	if mode != "text" && mode != "json" {
		writeJSONError(w, http.StatusBadRequest, "mode must be text or json")
		return
	}

	body, err := readBoundedBody(r, h.cfg.MaxBodyBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeJSONError(w, http.StatusBadRequest, "body exceeds max_body_bytes")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	text, err := parseInputBody(mode, body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if text == "" {
		writeJSONError(w, http.StatusBadRequest, "empty text")
		return
	}

	result, redactErr := h.redactor.Redact(r.Context(), text)
	if redactErr != nil {
		h.logger.Warn(proxylog.ProxyMsg("redact_api: redactor failed"),
			slog.String("reason", redactFailureReason(redactErr)))
		writeJSONError(w, http.StatusServiceUnavailable, "redaction failed")
		return
	}

	switch mode {
	case "json":
		changed := result.Text != text
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(jsonResponse{
			Text:     result.Text,
			Entities: result.EntityCounts,
			Changed:  changed,
		})
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, result.Text)
	}
}

var errBodyTooLarge = errors.New("body too large")

func readBoundedBody(r *http.Request, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = defaultMaxBodyBytes
	}
	limited := io.LimitReader(r.Body, int64(maxBytes)+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > maxBytes {
		return nil, errBodyTooLarge
	}
	return body, nil
}

func parseInputBody(mode string, body []byte) (string, error) {
	if mode == "json" {
		var req jsonRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return "", errors.New("invalid JSON body; expected {\"text\":\"...\"}")
		}
		return req.Text, nil
	}
	return string(body), nil
}

func extractCredential(r *http.Request) string {
	const bearerPrefix = "Bearer "
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, bearerPrefix) {
		return strings.TrimPrefix(auth, bearerPrefix)
	}
	if k := r.Header.Get("x-api-key"); k != "" {
		return k
	}
	return r.URL.Query().Get("key")
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	proxylog.WriteProxyJSONError(w, status, msg)
}

// redactFailureReason maps Presidio errors to stable log labels without
// echoing request excerpts that may contain PII.
func redactFailureReason(err error) string {
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "analyze_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "analyze_canceled"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "analyze call failed"):
		return "analyze_call_failed"
	case strings.Contains(msg, "analyze returned"):
		if code := parseAnalyzeStatusCode(msg); code > 0 {
			return "analyze_http_" + strconv.Itoa(code)
		}
		return "analyze_http_error"
	case strings.Contains(msg, "decode response"):
		return "analyze_decode_failed"
	default:
		return "redact_failed"
	}
}

func parseAnalyzeStatusCode(msg string) int {
	const prefix = "analyze returned "
	idx := strings.Index(msg, prefix)
	if idx < 0 {
		return 0
	}
	rest := msg[idx+len(prefix):]
	end := strings.IndexByte(rest, ':')
	if end < 0 {
		end = len(rest)
	}
	code, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
	if err != nil {
		return 0
	}
	return code
}
