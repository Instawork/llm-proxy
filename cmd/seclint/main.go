// Command seclint statically checks Go and TypeScript sources for common
// security anti-patterns using AST analysis.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	root := flag.String("root", ".", "repository root")
	web := flag.String("web", "web", "path to web/ for TS linter (empty to skip)")
	warnOnly := flag.Bool("warn-only", false, "exit 0 on errors when set")
	flag.Parse()

	var findings []Finding
	for _, dir := range []string{"internal", "cmd", "integration"} {
		findings = append(findings, lintGoTree(filepath.Join(*root, dir))...)
	}

	if *web != "" {
		webPath := *web
		if !filepath.IsAbs(webPath) {
			webPath = filepath.Join(*root, webPath)
		}
		tsFindings, err := lintTypeScriptRepo(webPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "seclint: ts: %v\n", err)
			os.Exit(2)
		}
		findings = append(findings, tsFindings...)
	}

	errs, warns := 0, 0
	for _, f := range findings {
		tag := "warn"
		if f.Severity == "error" {
			tag = "error"
			errs++
		} else {
			warns++
		}
		fmt.Printf("%s: %s:%d: [%s] %s\n", tag, f.File, f.Line, f.Rule, f.Message)
	}

	if len(findings) == 0 {
		fmt.Println("seclint: ok")
		return
	}
	fmt.Printf("seclint: %d error(s), %d warning(s)\n", errs, warns)
	if errs > 0 && !*warnOnly {
		os.Exit(1)
	}
}

func lintGoTree(dir string) []Finding {
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	var out []Finding
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(path, string(filepath.Separator)+".statslint-build"+string(filepath.Separator)) {
			return nil
		}
		out = append(out, lintGoFile(path)...)
		return nil
	})
	return out
}
