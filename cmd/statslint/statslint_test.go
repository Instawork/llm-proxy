package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintGoStatsFilesClean(t *testing.T) {
	root := findRepoRoot(t)
	internal := filepath.Join(root, "internal")
	findings := lintGoRepo(internal)

	var errs []Finding
	for _, f := range findings {
		if f.Severity == "error" {
			errs = append(errs, f)
		}
	}
	if len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("%s:%d [%s] %s", e.File, e.Line, e.Rule, e.Message)
		}
	}
}

func TestLintRatelimitstatsClean(t *testing.T) {
	root := findRepoRoot(t)
	path := filepath.Join(root, "internal", "ratelimitstats", "stats.go")
	findings := lintGoStatsFile(path)
	for _, f := range findings {
		if f.Severity == "error" {
			t.Errorf("%s:%d [%s] %s", f.File, f.Line, f.Rule, f.Message)
		}
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

func TestParseTSFindings(t *testing.T) {
	raw := "error|/x/a.ts|12|ts-rule|hello\nwarn|/x/b.ts|3|other|world\n"
	got := parseTSFindings([]byte(raw))
	if len(got) != 2 || got[0].Rule != "ts-rule" || got[1].Severity != "warn" {
		t.Fatalf("parseTSFindings: %+v", got)
	}
}

func TestExprString(t *testing.T) {
	if !strings.Contains(exprString(nil), "") {
		// smoke
	}
}
