package piiloglint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintSource_DetectsViolations(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantRule string
		wantLine int
	}{
		{
			name: "log.Printf raw userID",
			src: `package p
import "log"
func f(userID string) {
	log.Printf("User ID from X-User-ID header: %s", userID)
}`,
			wantRule: RuleUserIdentifier,
			wantLine: 4,
		},
		{
			name: "log.Printf raw ipAddr",
			src: `package p
import "log"
func f(ipAddr string) {
	log.Printf("User ID fallback to IP address: %s", ipAddr)
}`,
			wantRule: RuleUserIdentifier,
			wantLine: 4,
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
			wantLine: 9,
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
			wantLine: 9,
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
			wantLine: 6,
		},
		{
			name: "log.Printf %+v chunkData",
			src: `package p
import "log"
func f(chunkData map[string]any) {
	log.Printf("chunk: %+v", chunkData)
}`,
			wantRule: RuleRawBody,
			wantLine: 4,
		},
		{
			name: "log.Printf %+v arbitrary struct with body keyword in format string",
			src: `package p
import "log"
func f(obj any) {
	log.Printf("response body: %+v", obj)
}`,
			wantRule: RuleRawBody,
			wantLine: 4,
		},
		{
			name: "log.Printf %#v with chunk field",
			src: `package p
import "log"
type evt struct { RawChunk any }
func f(e evt) {
	log.Printf("parsed: %#v", e.RawChunk)
}`,
			wantRule: RuleRawBody,
			wantLine: 5,
		},
		{
			name: "log.Printf %s bodyBytes without LogPreview",
			src: `package p
import "log"
func f(bodyBytes []byte) {
	log.Printf("body: %s", string(bodyBytes))
}`,
			wantRule: RuleRawBody,
			wantLine: 4,
		},
		{
			name: "log.Printf OpenAI extracted userValue",
			src: `package p
import "log"
func f(userValue string) {
	log.Printf("OpenAI: Extracted user ID: %s", userValue)
}`,
			wantRule: RuleUserIdentifier,
			wantLine: 4,
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
			wantLine: 5,
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
			wantLine: 6,
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
			wantLine: 7,
		},
		{
			name: "slog.Info logging raw payload via slog.String",
			src: `package p
import "log/slog"
func f(payload []byte) {
	slog.Info("upstream error", slog.String("detail", string(payload)))
}`,
			wantRule: RuleRawBody,
			wantLine: 4,
		},
		{
			name: "slog.Warn with banned user_id attribute key",
			src: `package p
import "log/slog"
func f(id string) {
	slog.Warn("throttled", "user_id", id)
}`,
			wantRule: RuleUserIdentifier,
			wantLine: 4,
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
			wantLine: 8,
		},
		{
			name: "log.Println logging raw responseBody",
			src: `package p
import "log"
func f(responseBody string) {
	log.Println("resp:", responseBody)
}`,
			wantRule: RuleRawBody,
			wantLine: 4,
		},
		{
			name: "package-level func literal logging userID",
			src: `package p
import "log"
var handler = func(userID string) {
	log.Printf("user: %s", userID)
}`,
			wantRule: RuleUserIdentifier,
			wantLine: 4,
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
			wantLine: 5,
		},
		{
			name: "P1-2: logger.Info message arg with fmt.Sprintf credential leak",
			src: `package p
import (
	"fmt"
	"log/slog"
)
func f(logger *slog.Logger, apiKey string) {
	logger.Info(fmt.Sprintf("key=%s", apiKey))
}`,
			wantRule: RuleCredential,
			wantLine: 7,
		},
		{
			name: "P1-2: logger.Info message arg with string concat body leak",
			src: `package p
import "log/slog"
func f(logger *slog.Logger, body []byte) {
	logger.Info("bad body: " + string(body))
}`,
			wantRule: RuleRawBody,
			wantLine: 4,
		},
		{
			name: "P1-2: log.Printf dynamic format arg with body concat",
			src: `package p
import "log"
func f(body []byte) {
	log.Printf("bad body: " + string(body))
}`,
			wantRule: RuleRawBody,
			wantLine: 4,
		},
		{
			name: "P1-2: logger.Info message arg with fmt.Sprintf %+v chunk dump",
			src: `package p
import (
	"fmt"
	"log/slog"
)
func f(logger *slog.Logger, obj any) {
	logger.Info(fmt.Sprintf("chunk=%+v", obj))
}`,
			wantRule: RuleRawBody,
			wantLine: 7,
		},
		{
			name: "P1-3: prose comment mentioning piiloglint:ignore does not suppress",
			src: `package p
import "log/slog"
func f(logger *slog.Logger, apiKey string) {
	// TODO remove piiloglint:ignore directives someday
	logger.Info("oops", "key", apiKey)
}`,
			wantRule: RuleCredential,
			wantLine: 5,
		},
		{
			name: "P1-5: logProxyError wrapper caught",
			src: `package p
func logProxyError(msg string, args ...any) {}
func f(apiKey string) {
	logProxyError("bad auth", "key", apiKey)
}`,
			wantRule: RuleCredential,
			wantLine: 4,
		},
		{
			name: "P1-5: logProxyWarn wrapper caught",
			src: `package p
func logProxyWarn(msg string, args ...any) {}
func f(body []byte) {
	logProxyWarn("bad payload", "detail", string(body))
}`,
			wantRule: RuleRawBody,
			wantLine: 4,
		},
		{
			name: "P1-5: fmt.Printf credential leak caught",
			src: `package p
import "fmt"
func f(apiKey string) {
	fmt.Printf("key=%s\n", apiKey)
}`,
			wantRule: RuleCredential,
			wantLine: 4,
		},
		{
			name: "P1-5: fmt.Println body leak caught",
			src: `package p
import "fmt"
func f(body []byte) {
	fmt.Println(string(body))
}`,
			wantRule: RuleRawBody,
			wantLine: 4,
		},
		{
			name: "P1-5: fmt.Fprintf to os.Stderr caught",
			src: `package p
import (
	"fmt"
	"os"
)
func f(apiKey string) {
	fmt.Fprintf(os.Stderr, "failed for %s\n", apiKey)
}`,
			wantRule: RuleCredential,
			wantLine: 7,
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
				if f.Rule == tc.wantRule && (tc.wantLine == 0 || f.Line == tc.wantLine) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected finding rule=%q line=%d, got %+v", tc.wantRule, tc.wantLine, findings)
			}
		})
	}
}

func TestLintSource_IgnoreScoping(t *testing.T) {
	t.Run("inline comment does not spill onto next line in multi-line call", func(t *testing.T) {
		src := `package p
import "log/slog"
type keyT struct { PK string }
func f(logger *slog.Logger, clientIP string, record *keyT) {
	logger.Info(
		"admin: share link resolved",
		"client_ip", clientIP, //piiloglint:ignore intentional audit log
		"raw_key", record.PK,
	)
}`
		findings, err := LintSource("test.go", []byte(src))
		if err != nil {
			t.Fatalf("LintSource error: %v", err)
		}
		if len(findings) != 1 {
			t.Fatalf("expected exactly 1 finding on line 8, got %+v", findings)
		}
		if findings[0].Line != 8 || findings[0].Rule != RuleCredential {
			t.Errorf("expected credential finding on line 8, got %+v", findings[0])
		}
	})

	t.Run("inline comment on clean line emits unused-ignore and does not suppress next statement", func(t *testing.T) {
		src := `package p
import "log/slog"
func f(logger *slog.Logger, apiKey string) {
	logger.Info("ok") //piiloglint:ignore bogus
	logger.Info("leaked", "k", apiKey)
}`
		findings, err := LintSource("test.go", []byte(src))
		if err != nil {
			t.Fatalf("LintSource error: %v", err)
		}
		var sawUnused, sawCred bool
		for _, f := range findings {
			if f.Line == 4 && f.Rule == RuleUnusedIgnore {
				sawUnused = true
			}
			if f.Line == 5 && f.Rule == RuleCredential {
				sawCred = true
			}
		}
		if !sawUnused || !sawCred {
			t.Errorf("expected unused-ignore on line 4 and credential on line 5, got %+v", findings)
		}
	})

	t.Run("standalone comment applies only to next statement, not following statement", func(t *testing.T) {
		src := `package p
import "log/slog"
type notif struct { Body string }
func f(logger *slog.Logger, n notif, apiKey string) {
	//piiloglint:ignore dev-only notification log
	logger.Info(
		"notify",
		"body", n.Body,
	)
	logger.Info("oops", "key", apiKey)
}`
		findings, err := LintSource("test.go", []byte(src))
		if err != nil {
			t.Fatalf("LintSource error: %v", err)
		}
		if len(findings) != 1 {
			t.Fatalf("expected exactly 1 finding on line 10, got %+v", findings)
		}
		if findings[0].Line != 10 || findings[0].Rule != RuleCredential {
			t.Errorf("expected credential finding on line 10, got %+v", findings[0])
		}
	})

	t.Run("standalone comment separated by blank line does not suppress and reports unused-ignore", func(t *testing.T) {
		src := `package p
import "log/slog"
func f(logger *slog.Logger, apiKey string) {
	//piiloglint:ignore stale comment

	logger.Info("oops", "key", apiKey)
}`
		findings, err := LintSource("test.go", []byte(src))
		if err != nil {
			t.Fatalf("LintSource error: %v", err)
		}
		var sawUnused, sawCred bool
		for _, f := range findings {
			if f.Line == 4 && f.Rule == RuleUnusedIgnore {
				sawUnused = true
			}
			if f.Line == 6 && f.Rule == RuleCredential {
				sawCred = true
			}
		}
		if !sawUnused || !sawCred {
			t.Errorf("expected unused-ignore on line 4 and credential on line 6, got %+v", findings)
		}
	})

	t.Run("internal/admin/handlers.go:862 inline ignore does not spill onto line 863", func(t *testing.T) {
		root := findRepoRoot(t)
		raw, err := os.ReadFile(filepath.Join(root, "internal/admin/handlers.go"))
		if err != nil {
			t.Fatalf("read handlers.go: %v", err)
		}
		const target = `"created_by", link.CreatedBy,`
		if !strings.Contains(string(raw), target) {
			t.Fatalf("expected %q in internal/admin/handlers.go", target)
		}
		mutated := strings.Replace(string(raw), target, `"raw_key", record.PK,`, 1)
		findings, err := LintSource("internal/admin/handlers.go", []byte(mutated))
		if err != nil {
			t.Fatalf("LintSource error: %v", err)
		}
		if len(findings) != 1 || findings[0].Rule != RuleCredential {
			t.Fatalf("expected inline ignore on line 862 NOT to suppress leak on line 863; got %+v", findings)
		}
	})
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
func keyIDHashSuffix(s string) string { return s }
type recT struct { PK string }
func f(userID, apiKey string, rec *recT) {
	log.Printf("ratelimit: allow provider=%s user_hash=%s key_hash=%s", "openai", hashUserIDForLog(userID), keyIDHashSuffix(apiKey))
	proxylog.Proxy("costlimit: block provider=%s key_hash=%s", "openai", keyIDHashSuffix(rec.PK))
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
			name: "boolean comparison on credential (apiKey != \"\") is not a leak",
			src: `package p
import "log/slog"
func f(logger *slog.Logger, apiKey string) {
	logger.Info("auth check", "has_key", apiKey != "", "empty", apiKey == "")
}`,
		},
		{
			name: "int variables named prompt/messages and %d formatting are not leaks",
			src: `package p
import (
	"log"
	"log/slog"
)
func f(logger *slog.Logger, prompt int, body []byte) {
	messages := len(body)
	logger.Info("usage", "prompt_tokens", prompt, "message_count", messages)
	log.Printf("usage prompt=%d messages=%d", prompt, messages)
}`,
		},
		{
			name: "non-logger receiver named catalog is not flagged",
			src: `package p
type cat struct{}
func (cat) Info(msg string, args ...any) {}
func f(catalog cat, body []byte) {
	catalog.Info("loaded", "body", body)
}`,
		},
		{
			name: "fmt.Fprintf to bytes.Buffer is not a log call",
			src: `package p
import (
	"bytes"
	"fmt"
)
func f(apiKey string) string {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Bearer %s", apiKey)
	return buf.String()
}`,
		},
		{
			name: "benign %+v config struct without body keyword",
			src: `package p
import "log"
type timeouts struct { Connect int }
func f(t timeouts) {
	log.Printf("timeouts loaded: %+v", t)
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
	totalFiles := 0
	for _, dir := range []string{"cmd", "internal"} {
		dirFindings, n, err := LintTree(filepath.Join(root, dir))
		if err != nil {
			t.Fatalf("LintTree(%s) error: %v", dir, err)
		}
		totalFiles += n
		findings = append(findings, dirFindings...)
	}
	if totalFiles == 0 {
		t.Fatal("expected at least one Go file to be linted")
	}
	for _, f := range findings {
		t.Errorf("%s:%d: [%s] %s", f.File, f.Line, f.Rule, f.Message)
	}
}

func TestLintTree_NonexistentDirReturnsError(t *testing.T) {
	_, _, err := LintTree(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for nonexistent directory, got nil")
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
	findings, n, err := LintTree(dir)
	if err != nil {
		t.Fatalf("LintTree error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 non-test files scanned, got %d", n)
	}
	if len(findings) != 0 {
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
