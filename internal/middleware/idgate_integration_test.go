package middleware

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/ocr"
	"github.com/Instawork/llm-proxy/internal/redact"
)

type idGateTestCase struct {
	File             string   `json:"file"`
	ExpectBlock      bool     `json:"expect_block"`
	ExpectedEntities []string `json:"expected_entities"`
}

type idGateManifest struct {
	Cases []idGateTestCase `json:"cases"`
}

func idGateTestdataDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Join(filepath.Dir(file), "..", "..", "ocr_sidecar", "testdata")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("testdata dir missing (%s): %v", dir, err)
	}
	return dir
}

func loadIDGateManifest(t *testing.T) idGateManifest {
	t.Helper()
	dir := idGateTestdataDir(t)
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest idGateManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return manifest
}

func requireOCRSidecar(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping ID gate integration test in -short mode")
	}
	if os.Getenv("LLM_PROXY_ID_GATE_INTEGRATION") != "1" {
		t.Skip("Skipping ID gate integration test; set LLM_PROXY_ID_GATE_INTEGRATION=1 " +
			"(and `docker compose up -d ocr-sidecar`)")
	}
	target := os.Getenv("OCR_SIDECAR_URL")
	if target == "" {
		target = "http://localhost:8010"
	}
	dialReachable(t, target)
	return target
}

func requireIDGateStack(t *testing.T) (ocrURL, presidioURL string) {
	t.Helper()
	ocrURL = requireOCRSidecar(t)
	if os.Getenv("LLM_PROXY_PII_INTEGRATION") != "1" {
		t.Skip("Skipping full ID gate integration; set LLM_PROXY_PII_INTEGRATION=1 " +
			"(and `make test-pii-up`)")
	}
	presidioURL = os.Getenv("PRESIDIO_ANALYZER_URL")
	if presidioURL == "" {
		presidioURL = "http://localhost:5004"
	}
	dialReachable(t, presidioURL)
	return ocrURL, presidioURL
}

func dialReachable(t *testing.T, target string) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("invalid URL %q: %v", target, err)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		t.Skipf("cannot reach %s: %v", target, err)
	}
	_ = conn.Close()
}

func chatBodyWithImagePNG(t *testing.T, png []byte) []byte {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString(png)
	body := fmt.Sprintf(`{"messages":[{"role":"user","content":[{"type":"text","text":"scan this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,%s"}}]}]}`, b64)
	return []byte(body)
}

func TestIntegration_IDGate_TestdataManifest(t *testing.T) {
	ocrURL, presidioURL := requireIDGateStack(t)
	dir := idGateTestdataDir(t)
	manifest := loadIDGateManifest(t)

	ocrClient := ocr.New(ocrURL, 60*time.Second)
	redactor, err := redact.New(redact.Config{
		AnalyzerURL:    presidioURL,
		Timeout:        15 * time.Second,
		EntityTypes:    redact.DefaultGovIDEntityTypes,
		ScoreThreshold: 0.01,
	})
	if err != nil {
		t.Fatalf("redact.New: %v", err)
	}

	handler := IDGateMiddleware(ocrClient, redactor, IDGateConfig{
		ScoreThreshold: 0.4,
		EntityTypes:    redact.DefaultGovIDEntityTypes,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, tc := range manifest.Cases {
		tc := tc
		t.Run(tc.File, func(t *testing.T) {
			png, err := os.ReadFile(filepath.Join(dir, tc.File))
			if err != nil {
				t.Fatalf("read image: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions",
				strings.NewReader(string(chatBodyWithImagePNG(t, png))))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if tc.ExpectBlock {
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status=%d want 422 body=%q", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d want 200 body=%q", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestIntegration_OCRSidecar_TestdataExtractsText(t *testing.T) {
	ocrURL := requireOCRSidecar(t)
	dir := idGateTestdataDir(t)
	manifest := loadIDGateManifest(t)
	client := ocr.New(ocrURL, 60*time.Second)

	for _, tc := range manifest.Cases {
		tc := tc
		t.Run(tc.File, func(t *testing.T) {
			png, err := os.ReadFile(filepath.Join(dir, tc.File))
			if err != nil {
				t.Fatalf("read image: %v", err)
			}
			text, err := client.ExtractText(t.Context(), png, tc.File)
			if err != nil {
				t.Fatalf("ExtractText: %v", err)
			}
			if strings.TrimSpace(text) == "" {
				t.Fatal("expected non-empty OCR text")
			}
			t.Logf("ocr text: %q", text)
		})
	}
}
