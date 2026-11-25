// Command piiloglint statically checks Go sources for logging statements that
// leak raw request/response bodies, credentials/token prefixes, or per-request
// caller identifiers (user IDs, client IPs).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Instawork/llm-proxy/internal/piiloglint"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()

	var findings []piiloglint.Finding
	for _, dir := range []string{"cmd", "internal"} {
		findings = append(findings, piiloglint.LintTree(filepath.Join(*root, dir))...)
	}

	if len(findings) == 0 {
		fmt.Println("piiloglint: ok")
		return
	}
	for _, f := range findings {
		fmt.Printf("%s:%d: [%s] %s\n", f.File, f.Line, f.Rule, f.Message)
	}
	fmt.Printf("piiloglint: %d error(s)\n", len(findings))
	os.Exit(1)
}
