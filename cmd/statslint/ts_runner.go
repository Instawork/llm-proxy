package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func lintTypeScriptRepo(webDir string) ([]Finding, error) {
	script := filepath.Join(webDir, "scripts", "statslint.ts")
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("missing %s: %w", script, err)
	}
	scriptAbs, err := filepath.Abs(script)
	if err != nil {
		return nil, err
	}
	buildDir, err := filepath.Abs(filepath.Join(webDir, "scripts", ".statslint-build"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return nil, err
	}

	tsc := resolveTSC(webDir)
	outJS := filepath.Join(buildDir, "statslint.js")
	cmd := exec.Command(
		tsc,
		"--target", "ES2022",
		"--module", "NodeNext",
		"--moduleResolution", "NodeNext",
		"--esModuleInterop",
		"--skipLibCheck",
		"--outDir", buildDir,
		scriptAbs,
	)
	cmd.Dir = filepath.Dir(scriptAbs)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tsc statslint: %v\n%s", err, out)
	}

	webAbs, err := filepath.Abs(webDir)
	if err != nil {
		return nil, err
	}
	node := exec.Command("node", outJS, webAbs)
	var buf bytes.Buffer
	node.Stdout = &buf
	node.Stderr = &buf
	if err := node.Run(); err != nil {
		return nil, fmt.Errorf("node statslint: %v\n%s", err, buf.Bytes())
	}

	return parseTSFindings(buf.Bytes()), nil
}

func resolveTSC(webDir string) string {
	candidates := []string{
		filepath.Join(webDir, "node_modules", ".bin", "tsc"),
		filepath.Join(webDir, "node_modules", "typescript", "bin", "tsc"),
	}
	for _, c := range candidates {
		if abs, err := filepath.Abs(c); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}
	if p, err := exec.LookPath("tsc"); err == nil {
		return p
	}
	return "tsc"
}

func parseTSFindings(raw []byte) []Finding {
	var out []Finding
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		// severity|file|line|rule|message
		parts := bytes.SplitN(line, []byte("|"), 5)
		if len(parts) != 5 {
			continue
		}
		lineNo := 0
		fmt.Sscanf(string(parts[2]), "%d", &lineNo)
		out = append(out, Finding{
			Severity: string(parts[0]),
			File:     string(parts[1]),
			Line:     lineNo,
			Rule:     string(parts[3]),
			Message:  string(parts[4]),
		})
	}
	return out
}
