package piiloglint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLintSource_DetectsViolations(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantRule string
	}{
		{
			name: "log.Printf raw userID",
			src: `package p
import "log"
func f(userID string) {
	log.Printf("User ID from X-User-ID header: %s", userID)
}`,
			wantRule: RuleUserIdentifier,
		},
		{
			name: "log.Printf raw ipAddr",
			src: `package p
import "log"
func f(ipAddr string) {
	log.Printf("User ID fallback to IP address: %s", ipAddr)
}`,
			wantRule: RuleUserIdentifier,
		},
		{
			name: "log.Printf bearer token prefix via tokenID",
			src: `package p
import (
	"fmt"
	"log"
)
func f(auth string) {
	token := auth[7:]
	tokenID := fmt.Sprintf("token:%s", token[:8])
	log.Printf("User ID from Authorization header: %s", tokenID)
}`,
			wantRule: RuleCredential,
		},
		{
			name: "log.Printf bearer token prefix renamed var (taint)",
			src: `package p
import (
	"fmt"
	"log"
)
func f(auth string) {
	t := auth[7:]
	caller := fmt.Sprintf("token:%s", t[:8])
	log.Printf("caller: %s", caller)
}`,
			wantRule: RuleCredential,
		},
		{
			name: "multi-line log.Printf Bedrock exception payload with %s",
			src: `package p
import "log"
type msg struct { Payload []byte }
func f(m msg) {
	log.Printf("Bedrock eventstream non-event message_type=%s event_type=%s payload=%s",
		"exception", "validationException", string(m.Payload[:min(200, len(m.Payload))]))
}`,
			wantRule: RuleRawBody,
		},
		{
			name: "log.Printf %+v chunkData",
			src: `package p
import "log"
func f(chunkData map[string]any) {
	log.Printf("chunk: %+v", chunkData)
}`,
			wantRule: RuleRawBody,
		},
		{
			name: "log.Printf %s bodyBytes without LogPreview",
			src: `package p
import "log"
func f(bodyBytes []byte) {
	log.Printf("body: %s", string(bodyBytes))
}`,
			wantRule: RuleRawBody,
		},
		{
			name: "log.Printf OpenAI extracted userValue",
			src: `package p
import "log"
func f(userValue string) {
	log.Printf("OpenAI: Extracted user ID: %s", userValue)
}`,
			wantRule: RuleUserIdentifier,
		},
		{
			name: "ratelimit log with raw user and prefix(apiKey)",
			src: `package p
import "log"
func prefix(s string) string { return s[:12] }
func f(userID, apiKey string) {
	log.Printf("ratelimit: allow provider=%s user=%s key_prefix=%s", "openai", userID, prefix(apiKey))
}`,
			wantRule: RuleCredential,
		},
		{
			name: "proxylog.Proxy costlimit with prefix(rec.PK)",
			src: `package p
import "github.com/Instawork/llm-proxy/internal/proxylog"
type recT struct { PK string }
func prefix(s string) string { return s[:12] }
func f(rec *recT) {
	proxylog.Proxy("costlimit: block provider=%s key_prefix=%s", "openai", prefix(rec.PK))
}`,
			wantRule: RuleCredential,
		},
		{
			name: "receiver logger logging raw key.PK",
			src: `package p
import "log/slog"
type keyT struct { PK string }
type deps struct { Logger *slog.Logger }
type h struct { deps deps }
func (x *h) f(key *keyT, err error) {
	x.deps.Logger.Error("cleanup failed", "error", err, "key", key.PK)
}`,
			wantRule: RuleCredential,
		},
		{
			name: "slog.Info logging raw payload via slog.String",
			src: `package p
import "log/slog"
func f(payload []byte) {
	slog.Info("upstream error", slog.String("detail", string(payload)))
}`,
			wantRule: RuleRawBody,
		},
		{
			name: "slog.Warn with banned user_id attribute key",
			src: `package p
import "log/slog"
func f(id string) {
	slog.Warn("throttled", "user_id", id)
}`,
			wantRule: RuleUserIdentifier,
		},
		{
			name: "proxylog.SlogProxy logging raw Authorization header",
			src: `package p
import (
	"log/slog"
	"net/http"
	"github.com/Instawork/llm-proxy/internal/proxylog"
)
func f(r *http.Request) {
	proxylog.SlogProxy(slog.Default(), slog.LevelWarn, "bad auth", "header", r.Header.Get("Authorization"))
}`,
			wantRule: RuleCredential,
		},
		{
			name: "log.Println logging raw responseBody",
			src: `package p
import "log"
func f(responseBody string) {
	log.Println("resp:", responseBody)
}`,
			wantRule: RuleRawBody,
		},
		{
			name: "package-level func literal logging userID",
			src: `package p
import "log"
var handler = func(userID string) {
	log.Printf("user: %s", userID)
}`,
			wantRule: RuleUserIdentifier,
		},
		{
			name: "ignore comment without reason does not suppress",
			src: `package p
import "log"
func f(userID string) {
	//piiloglint:ignore
	log.Printf("user: %s", userID)
}`,
			wantRule: RuleUserIdentifier,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings, err := LintSource("test.go", []byte(tc.src))
			if err != nil {
				t.Fatalf("LintSource error: %v", err)
			}
			found := false
			for _, f := range findings {
				if f.Rule == tc.wantRule {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected finding with rule %q, got %+v", tc.wantRule, findings)
			}
		})
	}
}

func TestLintSource_AllowsSafePatterns(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{
			name: "Bedrock payload routed through redact.LogPreview",
			src: `package p
import (
	"context"
	"log"
	"github.com/Instawork/llm-proxy/internal/redact"
)
type msg struct { Payload []byte }
func f(m msg) {
	log.Printf("Bedrock eventstream non-event message_type=%s event_type=%s payload=%s",
		"exception", "validationException", redact.LogPreview(context.Background(), string(m.Payload), 200))
}`,
		},
		{
			name: "chunkData wrapped in redact.LogPreview with fmt.Sprintf",
			src: `package p
import (
	"context"
	"fmt"
	"log"
	"github.com/Instawork/llm-proxy/internal/redact"
)
func f(chunkData map[string]any) {
	log.Printf("chunk: %s", redact.LogPreview(context.Background(), fmt.Sprintf("%+v", chunkData), 200))
}`,
		},
		{
			name: "ratelimit and costlimit logging hashed user and key",
			src: `package p
import (
	"log"
	"github.com/Instawork/llm-proxy/internal/proxylog"
)
func hashUserIDForLog(s string) string { return s }
func hashKeyForLog(s string) string { return s }
type recT struct { PK string }
func f(userID, apiKey string, rec *recT) {
	log.Printf("ratelimit: allow provider=%s user_hash=%s key_hash=%s", "openai", hashUserIDForLog(userID), hashKeyForLog(apiKey))
	proxylog.Proxy("costlimit: block provider=%s key_hash=%s", "openai", hashKeyForLog(rec.PK))
}`,
		},
		{
			name: "admin logging redacted key",
			src: `package p
import (
	"log/slog"
	"github.com/Instawork/llm-proxy/internal/apikeys"
)
type keyT struct { PK string }
func f(logger *slog.Logger, key *keyT, err error) {
	logger.Error("cleanup failed", "error", err, "key", apikeys.RedactKey(key.PK))
}`,
		},
		{
			name: "slog.Int body_bytes count and len(body)",
			src: `package p
import "log/slog"
func f(logger *slog.Logger, body []byte, bodyBytes int) {
	logger.Info("pii_redact: ok", slog.Int("body_bytes", bodyBytes), slog.Int("len", len(body)))
}`,
		},
		{
			name: "generic circuit breaker key attribute is not a credential",
			src: `package p
import "log/slog"
func f(logger *slog.Logger, key string, err error) {
	logger.Warn("circuit: GetState error", "key", key, "error", err)
}`,
		},
		{
			name: "unmarshal error from bodyBytes is not tainted",
			src: `package p
import (
	"encoding/json"
	"github.com/Instawork/llm-proxy/internal/proxylog"
)
func f(bodyBytes []byte) {
	var v map[string]any
	if err := json.Unmarshal(bodyBytes, &v); err != nil {
		proxylog.Proxy("parse failed: %v", err)
	}
}`,
		},
		{
			name: "non-log fmt.Sprintf returning token prefix",
			src: `package p
import "fmt"
func f(token string) string {
	return fmt.Sprintf("token:%s", token[:8])
}`,
		},
		{
			name: "ignore comment with reason suppresses line",
			src: `package p
import "log/slog"
func f(logger *slog.Logger, clientIP string) {
	logger.Info("audit", "client_ip", clientIP) //piiloglint:ignore intentional admin audit log
}`,
		},
		{
			name: "leading ignore comment with reason suppresses next line",
			src: `package p
import "log/slog"
type notif struct { Body string }
func f(logger *slog.Logger, n notif) {
	//piiloglint:ignore dev-only log sender
	logger.Info("notify", "body", n.Body)
}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings, err := LintSource("test.go", []byte(tc.src))
			if err != nil {
				t.Fatalf("LintSource error: %v", err)
			}
			if len(findings) != 0 {
				t.Errorf("expected 0 findings, got %+v", findings)
			}
		})
	}
}

func TestLintTree_RepoClean(t *testing.T) {
	root := findRepoRoot(t)
	var findings []Finding
	for _, dir := range []string{"cmd", "internal"} {
		findings = append(findings, LintTree(filepath.Join(root, dir))...)
	}
	for _, f := range findings {
		t.Errorf("%s:%d: [%s] %s", f.File, f.Line, f.Rule, f.Message)
	}
}

func TestLintTree_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "foo_test.go")
	if err := os.WriteFile(testFile, []byte(`package p
import "log"
func TestX(userID string) { log.Printf("%s", userID) }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if findings := LintTree(dir); len(findings) != 0 {
		t.Fatalf("expected _test.go files to be skipped, got %+v", findings)
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
