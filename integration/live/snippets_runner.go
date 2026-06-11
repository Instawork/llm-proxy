package live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type ShareContext struct {
	Provider string
	BaseURL  string
	Key      string
}

func ShareBaseURL(proxyBase, provider string) string {
	switch provider {
	case "openai":
		return strings.TrimRight(proxyBase, "/") + "/openai/v1"
	case "anthropic":
		return strings.TrimRight(proxyBase, "/") + "/anthropic"
	case "gemini":
		return strings.TrimRight(proxyBase, "/") + "/gemini"
	default:
		return strings.TrimRight(proxyBase, "/") + "/" + provider
	}
}

var snippetIDsByProvider = map[string][]string{
	"openai":    {"curl", "http", "go", "node", "python", "env"},
	"anthropic": {"curl", "http", "go", "node", "python", "env"},
	"gemini":    {"curl", "http", "go", "node", "python"},
}

func (r *Runner) runSnippets(ctx context.Context) []Result {
	root := r.cfg.SnippetsDir
	var out []Result

	providers := []struct {
		name    string
		keyEnv  string
		enabled bool
	}{
		{"openai", "OPENAI_API_KEY", r.cfg.OpenAIKey != ""},
		{"anthropic", "ANTHROPIC_API_KEY", r.cfg.AnthropicKey != ""},
		{"gemini", "GEMINI_API_KEY", r.cfg.GeminiKey != ""},
	}

	r.logf("snippets: ensuring dependencies")
	if err := InstallSnippetDeps(r.cfg.ModuleRoot, r.logf); err != nil {
		return []Result{r.emitOne(failResult("snippets", "deps", err.Error()))}
	}

	for _, p := range providers {
		if !p.enabled {
			r.logf("snippets: skip %s (%s not set)", p.name, p.keyEnv)
			out = append(out, r.emitOne(skipResult("snippets/"+p.name, "all", p.keyEnv+" not set")))
			continue
		}
		actualKey := r.providerActualKey(p.name)
		r.logf("snippets: creating temporary iw: key for %s via admin API", p.name)
		share, cleanup, err := r.createSnippetKey(ctx, p.name, actualKey)
		if err != nil {
			out = append(out, r.emitOne(failResult("snippets/"+p.name, "setup", err.Error())))
			continue
		}
		r.logf("snippets: %s ready — %s", p.name, snippetShareLine(share))
		for _, id := range snippetIDsByProvider[p.name] {
			res := r.verifySnippet(ctx, root, r.cfg.ModuleRoot, share, id)
			out = append(out, r.emitOne(res))
		}
		r.logf("snippets: deleting temporary iw: key for %s (%s)", p.name, maskProxyKey(share.Key))
		cleanup()
	}
	return out
}

func (r *Runner) emitOne(res Result) Result {
	if r.cfg.Verbose {
		PrintResult(res)
	}
	return res
}

func (r *Runner) providerActualKey(provider string) string {
	switch provider {
	case "openai":
		return r.cfg.OpenAIKey
	case "anthropic":
		return r.cfg.AnthropicKey
	case "gemini":
		return r.cfg.GeminiKey
	default:
		return ""
	}
}

func (r *Runner) createSnippetKey(ctx context.Context, provider, actualKey string) (ShareContext, func(), error) {
	key, err := r.admin.CreateKey(ctx, createKeyRequest{
		Provider:    provider,
		ActualKey:   actualKey,
		Description: "live-snippet-" + provider,
		Tags:        map[string]string{"suite": "snippets"},
	})
	if err != nil {
		return ShareContext{}, nil, err
	}
	share := ShareContext{
		Provider: provider,
		BaseURL:  ShareBaseURL(r.cfg.BaseURL, provider),
		Key:      key.Key,
	}
	cleanup := func() { _ = r.admin.DeleteKey(ctx, key.Key) }
	return share, cleanup, nil
}

func (r *Runner) verifySnippet(ctx context.Context, root, moduleRoot string, share ShareContext, id string) Result {
	suite := "snippets/" + share.Provider
	name := id
	start := time.Now()

	var err error
	var via string
	switch id {
	case "curl":
		r.logSnippetStart(share, id, snippetCurlDesc(share))
		err = runSnippetCurl(ctx, share, r.cfg.Timeout)
		via = "curl"
	case "http":
		script := filepath.Join(root, "python", "raw_"+share.Provider+".py")
		r.logSnippetStart(share, id, fmt.Sprintf("python %s (httpx)", script))
		err = runSnippetRawHTTP(ctx, root, share, r.cfg.Timeout)
		via = "httpx"
	case "go":
		rel := filepath.Join("snippets", "go", share.Provider)
		r.logSnippetStart(share, id, fmt.Sprintf("go run ./%s (cwd=%s, timeout=%s; first run may compile)",
			filepath.ToSlash(rel), moduleRoot, r.cfg.Timeout))
		err = runSnippetGo(ctx, moduleRoot, share, r.cfg.Timeout)
		via = "go run"
	case "node":
		script := filepath.Join(root, "node", share.Provider+".mjs")
		r.logSnippetStart(share, id, fmt.Sprintf("node %s", script))
		err = runSnippetNode(ctx, root, share, r.cfg.Timeout)
		via = "node"
	case "python":
		script := filepath.Join(root, "python", "snippet_"+share.Provider+".py")
		r.logSnippetStart(share, id, fmt.Sprintf("python %s", script))
		err = runSnippetPython(ctx, root, share, r.cfg.Timeout, false)
		via = "python"
	case "env":
		switch share.Provider {
		case "openai":
			pyScript := filepath.Join(root, "python", "openai_env.py")
			r.logSnippetStart(share, id, fmt.Sprintf("python %s (env tab; falls back to node on failure)", pyScript))
			err = runSnippetPython(ctx, root, share, r.cfg.Timeout, true)
			via = "python openai_env.py"
			if err != nil {
				nodeScript := filepath.Join(root, "node", "openai-env.mjs")
				r.logf("snippets/%s/env: python failed (%v); trying node %s", share.Provider, err, nodeScript)
				err = runSnippetNodeEnv(ctx, root, share, r.cfg.Timeout)
				via = "node openai-env.mjs"
			}
		case "anthropic":
			pyScript := filepath.Join(root, "python", "anthropic_env.py")
			r.logSnippetStart(share, id, fmt.Sprintf("python %s (env tab; falls back to node on failure)", pyScript))
			err = runSnippetPythonEnvAnthropic(ctx, root, share, r.cfg.Timeout)
			via = "python anthropic_env.py"
			if err != nil {
				nodeScript := filepath.Join(root, "node", "anthropic-env.mjs")
				r.logf("snippets/%s/env: python failed (%v); trying node %s", share.Provider, err, nodeScript)
				err = runSnippetNodeEnvAnthropic(ctx, root, share, r.cfg.Timeout)
				via = "node anthropic-env.mjs"
			}
		default:
			return skipResult(suite, name, "no env snippet for "+share.Provider)
		}
	default:
		return skipResult(suite, name, "unknown snippet id")
	}

	if err != nil {
		r.logf("snippets/%s/%s: failed via %s: %v", share.Provider, id, via, err)
		if isMissingTool(err) {
			return skipResult(suite, name, err.Error())
		}
		return failResult(suite, name, err.Error())
	}
	r.logf("snippets/%s/%s: ok via %s (%s)", share.Provider, id, via, elapsed(start))
	return passResult(suite, name, "via "+via, elapsed(start))
}

func (r *Runner) logSnippetStart(share ShareContext, id, command string) {
	r.logf("snippets/%s/%s: %s [%s]", share.Provider, id, command, snippetShareLine(share))
}

func snippetCurlDesc(share ShareContext) string {
	switch share.Provider {
	case "openai":
		return "curl POST " + share.BaseURL + "/chat/completions"
	case "anthropic":
		return "curl POST " + share.BaseURL + "/v1/messages"
	case "gemini":
		return "curl POST " + share.BaseURL + "/v1beta/models/gemini-2.5-flash:generateContent"
	default:
		return "curl"
	}
}

func runSnippetRawHTTP(ctx context.Context, root string, share ShareContext, timeout time.Duration) error {
	py, err := snippetPython(root)
	if err != nil {
		return err
	}
	script := filepath.Join(root, "python", "raw_"+share.Provider+".py")
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, script)
	cmd.Env = snippetEnv(share)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("httpx raw_%s.py: %w: %s", share.Provider, err, truncate(string(out), 400))
	}
	return nil
}

func runSnippetCurl(ctx context.Context, share ShareContext, timeout time.Duration) error {
	url, headers, body, err := curlRequest(share)
	if err != nil {
		return err
	}
	args := []string{"-sfS", "--max-time", fmt.Sprintf("%d", int(timeout.Seconds())), url}
	for k, v := range headers {
		args = append(args, "-H", k+": "+v)
	}
	args = append(args, "-d", body)
	cmd := exec.CommandContext(ctx, "curl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("curl: %w: %s", err, truncate(string(out), 400))
	}
	return nil
}

func curlRequest(share ShareContext) (string, map[string]string, string, error) {
	switch share.Provider {
	case "openai":
		body, _ := json.Marshal(map[string]any{
			"model":    "gpt-4o",
			"messages": []map[string]string{{"role": "user", "content": "Hello from the proxy!"}},
		})
		return share.BaseURL + "/chat/completions", map[string]string{
			"Authorization": "Bearer " + share.Key,
			"Content-Type":  "application/json",
		}, string(body), nil
	case "anthropic":
		body, _ := json.Marshal(map[string]any{
			"model":      "claude-sonnet-4-5",
			"max_tokens": 512,
			"messages":   []map[string]string{{"role": "user", "content": "Hello from the proxy!"}},
		})
		return share.BaseURL + "/v1/messages", map[string]string{
			"x-api-key":         share.Key,
			"anthropic-version": "2023-06-01",
			"Content-Type":      "application/json",
		}, string(body), nil
	case "gemini":
		body, _ := json.Marshal(map[string]any{
			"contents": []map[string]any{
				{"parts": []map[string]string{{"text": "Hello from the proxy!"}}},
			},
		})
		return share.BaseURL + "/v1beta/models/gemini-2.5-flash:generateContent", map[string]string{
			"x-goog-api-key": share.Key,
			"Content-Type":   "application/json",
		}, string(body), nil
	default:
		return "", nil, "", fmt.Errorf("unsupported provider %q", share.Provider)
	}
}

func runSnippetGo(ctx context.Context, moduleRoot string, share ShareContext, timeout time.Duration) error {
	rel := filepath.Join("snippets", "go", share.Provider)
	dir := filepath.Join(moduleRoot, rel)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("go snippet dir missing: %s", dir)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./"+filepath.ToSlash(rel))
	cmd.Dir = moduleRoot
	cmd.Env = snippetEnv(share)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("go run: %w: %s", err, truncate(stderr.String()+string(out), 400))
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return fmt.Errorf("go run: empty response")
	}
	return nil
}

func runSnippetNode(ctx context.Context, root string, share ShareContext, timeout time.Duration) error {
	script := filepath.Join(root, "node", share.Provider+".mjs")
	return runNodeScript(ctx, script, share, timeout)
}

func runSnippetNodeEnv(ctx context.Context, root string, share ShareContext, timeout time.Duration) error {
	if share.Provider != "openai" {
		return fmt.Errorf("node env snippet only for openai")
	}
	script := filepath.Join(root, "node", "openai-env.mjs")
	return runNodeScript(ctx, script, share, timeout)
}

func runSnippetNodeEnvAnthropic(ctx context.Context, root string, share ShareContext, timeout time.Duration) error {
	script := filepath.Join(root, "node", "anthropic-env.mjs")
	return runNodeScript(ctx, script, share, timeout)
}

func runNodeScript(ctx context.Context, script string, share ShareContext, timeout time.Duration) error {
	if _, err := exec.LookPath("node"); err != nil {
		return missingTool("node")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Env = snippetEnv(share)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("node %s: %w: %s", filepath.Base(script), err, truncate(string(out), 400))
	}
	return nil
}

func runSnippetPython(ctx context.Context, root string, share ShareContext, timeout time.Duration, envMode bool) error {
	py, err := snippetPython(root)
	if err != nil {
		return err
	}
	name := "snippet_" + share.Provider + ".py"
	if envMode {
		name = share.Provider + "_env.py"
	}
	script := filepath.Join(root, "python", name)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, script)
	cmd.Env = snippetEnv(share)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("python %s: %w: %s", name, err, truncate(string(out), 400))
	}
	return nil
}

func runSnippetPythonEnvAnthropic(ctx context.Context, root string, share ShareContext, timeout time.Duration) error {
	py, err := snippetPython(root)
	if err != nil {
		return err
	}
	script := filepath.Join(root, "python", "anthropic_env.py")
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, script)
	cmd.Env = snippetEnv(share)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("python anthropic_env.py: %w: %s", err, truncate(string(out), 400))
	}
	return nil
}

func snippetEnv(share ShareContext) []string {
	env := os.Environ()
	env = append(
		env,
		"PROXY_BASE_URL="+share.BaseURL,
		"PROXY_API_KEY="+share.Key,
	)
	return env
}

func snippetPython(root string) (string, error) {
	venvPy := filepath.Join(root, "python", ".venv", "bin", "python3")
	if _, err := os.Stat(venvPy); err == nil {
		return venvPy, nil
	}
	return lookPython()
}

func lookPython() (string, error) {
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", missingTool("python3")
}

type missingToolError string

func missingTool(name string) error {
	return missingToolError(name + " not found in PATH")
}

func (e missingToolError) Error() string { return string(e) }

func isMissingTool(err error) bool {
	_, ok := err.(missingToolError)
	return ok
}

func resolveSnippetsDir(dir string) string {
	if filepath.IsAbs(dir) {
		return dir
	}
	cwd, err := os.Getwd()
	if err != nil {
		return dir
	}
	return filepath.Join(cwd, dir)
}
