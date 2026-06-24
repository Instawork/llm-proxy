package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLintGoTreeNoCriticalInStatsPackages(t *testing.T) {
	root := findRepoRoot(t)
	findings := lintGoTree(filepath.Join(root, "internal", "admin"))
	for _, f := range findings {
		if f.Severity == "error" && f.Rule == "sec-hardcoded-provider-key" {
			t.Errorf("%s:%d %s", f.File, f.Line, f.Message)
		}
	}
}

func TestShellInvocationDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.go")
	if err := os.WriteFile(path, []byte(`package p
import "os/exec"
func f(script string) { exec.Command("bash", "-c", script) }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	findings := lintGoFile(path)
	found := false
	for _, f := range findings {
		if f.Rule == "sec-exec-shell" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected sec-exec-shell finding")
	}
}

func TestParseTSFindings(t *testing.T) {
	raw := "error|/x/a.ts|12|sec-eval|hello\n"
	got := parseTSFindings([]byte(raw))
	if len(got) != 1 || got[0].Rule != "sec-eval" {
		t.Fatalf("got %+v", got)
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
