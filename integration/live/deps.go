package live

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// InstallSnippetDeps runs integration/scripts/install-snippet-deps.sh (node, python venv, go mod).
func InstallSnippetDeps(moduleRoot string, log func(string, ...any)) error {
	script := filepath.Join(moduleRoot, "scripts", "install-snippet-deps.sh")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("install script missing at %s: %w", script, err)
	}
	log("snippets: ensuring deps via %s", script)
	cmd := exec.Command("bash", script)
	cmd.Dir = moduleRoot
	cmd.Stderr = os.Stderr
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		msg := truncate(stdout.String(), 400)
		return fmt.Errorf("install-snippet-deps: %w: %s", err, msg)
	}
	return nil
}
