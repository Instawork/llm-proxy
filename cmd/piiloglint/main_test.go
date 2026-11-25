package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_NonexistentRootFailsNonZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-root", filepath.Join(t.TempDir(), "nonexistent")}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code for nonexistent root, got 0 (stdout=%q stderr=%q)", stdout.String(), stderr.String())
	}
	if stderr.Len() == 0 {
		t.Errorf("expected error message on stderr")
	}
}

func TestRun_EmptyRootFailsNonZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-root", t.TempDir()}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code when no Go files are found, got 0 (stdout=%q stderr=%q)", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "no Go files found") {
		t.Errorf("expected 'no Go files found' on stderr, got %q", stderr.String())
	}
}

func TestRun_CleanRepoExitsZero(t *testing.T) {
	root := t.TempDir()
	internalDir := filepath.Join(root, "internal", "sample")
	if err := os.MkdirAll(internalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(internalDir, "ok.go"), []byte(`package sample
import "log/slog"
func ok(l *slog.Logger) { l.Info("healthy", "status", 200) }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-root", root}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "piiloglint: ok") {
		t.Errorf("expected 'piiloglint: ok' on stdout, got %q", stdout.String())
	}
}

func TestRun_LeakExitsOneAndPrintsFinding(t *testing.T) {
	root := t.TempDir()
	internalDir := filepath.Join(root, "internal", "sample")
	if err := os.MkdirAll(internalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(internalDir, "leak.go"), []byte(`package sample
import "log"
func bad(apiKey string) { log.Printf("key=%s", apiKey) }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-root", root}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d (stdout=%q stderr=%q)", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "[pii-log-credential]") {
		t.Errorf("expected credential finding on stdout, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "piiloglint: 1 error(s)") {
		t.Errorf("expected summary count on stdout, got %q", stdout.String())
	}
}
