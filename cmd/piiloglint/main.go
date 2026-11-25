// Command piiloglint statically checks Go sources for logging statements that
// leak raw request/response bodies, credentials/token prefixes, or per-request
// caller identifiers (user IDs, client IPs).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Instawork/llm-proxy/internal/piiloglint"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("piiloglint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	rootInfo, err := os.Stat(*root)
	if err != nil {
		fmt.Fprintf(stderr, "piiloglint: root %q: %v\n", *root, err)
		return 2
	}
	if !rootInfo.IsDir() {
		fmt.Fprintf(stderr, "piiloglint: root %q is not a directory\n", *root)
		return 2
	}

	var findings []piiloglint.Finding
	totalFiles := 0
	for _, dir := range []string{"cmd", "internal"} {
		target := filepath.Join(*root, dir)
		if _, err := os.Stat(target); os.IsNotExist(err) {
			continue
		}
		dirFindings, n, err := piiloglint.LintTree(target)
		if err != nil {
			fmt.Fprintf(stderr, "piiloglint: walk %q: %v\n", target, err)
			return 2
		}
		totalFiles += n
		findings = append(findings, dirFindings...)
	}

	if totalFiles == 0 {
		fmt.Fprintf(stderr, "piiloglint: no Go files found under %q (expected cmd/ or internal/)\n", *root)
		return 2
	}

	if len(findings) == 0 {
		fmt.Fprintln(stdout, "piiloglint: ok")
		return 0
	}
	for _, f := range findings {
		fmt.Fprintf(stdout, "%s:%d: [%s] %s\n", f.File, f.Line, f.Rule, f.Message)
	}
	fmt.Fprintf(stdout, "piiloglint: %d error(s)\n", len(findings))
	return 1
}
