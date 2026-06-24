package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	reAWSKey       = regexp.MustCompile(`AKIA[0-9A-Z]{16}`)
	rePrivateKey   = regexp.MustCompile(`-----BEGIN (?:RSA |EC )?PRIVATE KEY-----`)
	reProviderKey  = regexp.MustCompile(`\b(sk-(?:ant|iw)-[A-Za-z0-9_-]{8,}|AIza[0-9A-Za-z_-]{20,})\b`)
	reAssignSecret = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|token|bearer)\s*[:=]\s*['"][^'"]{8,}['"]`)
)

func lintGoFile(path string) []Finding {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return []Finding{{
			File: path, Line: 1, Rule: "parse-error", Message: err.Error(), Severity: "error",
		}}
	}

	ctx := fileContext(path)
	var out []Finding
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ImportSpec:
			out = append(out, lintGoImport(fset, path, ctx, x)...)
		case *ast.CallExpr:
			out = append(out, lintGoCall(fset, path, ctx, x)...)
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				out = append(out, lintGoStringLit(fset, path, ctx, x)...)
			}
		case *ast.CompositeLit:
			out = append(out, lintGoComposite(fset, path, ctx, x)...)
		}
		return true
	})
	return out
}

type goFileContext struct {
	path     string
	isTest   bool
	inCmd    bool
	inInteg  bool
	relLower string
}

func fileContext(path string) goFileContext {
	base := filepath.Base(path)
	rel := filepath.ToSlash(path)
	return goFileContext{
		path:     path,
		isTest:   strings.HasSuffix(base, "_test.go"),
		inCmd:    strings.Contains(rel, "/cmd/"),
		inInteg:  strings.Contains(rel, "/integration/"),
		relLower: strings.ToLower(rel),
	}
}

func (c goFileContext) severity(defaultSev string) string {
	if c.isTest || c.inInteg {
		return "warn"
	}
	return defaultSev
}

func lintGoImport(fset *token.FileSet, path string, ctx goFileContext, spec *ast.ImportSpec) []Finding {
	if spec.Path == nil {
		return nil
	}
	imp := strings.Trim(spec.Path.Value, `"`)
	line := lineFor(fset, spec.Pos())

	switch imp {
	case "math/rand":
		if isCryptoSensitivePath(ctx.relLower) {
			return []Finding{{
				File: path, Line: line, Rule: "sec-math-rand-crypto",
				Message:  "math/rand is not cryptographically secure; use crypto/rand for secrets/tokens",
				Severity: ctx.severity("error"),
			}}
		}
	case "crypto/md5", "crypto/sha1":
		return []Finding{{
			File: path, Line: line, Rule: "sec-weak-hash",
			Message:  imp + " is weak for security-sensitive hashing",
			Severity: ctx.severity("warn"),
		}}
	}
	return nil
}

func isCryptoSensitivePath(rel string) bool {
	for _, frag := range []string{
		"/auth", "/session", "/apikeys/", "/middleware/", "/admin/auth",
		"/token", "/oauth", "/credential",
	} {
		if strings.Contains(rel, frag) {
			return true
		}
	}
	return false
}

func lintGoCall(fset *token.FileSet, path string, ctx goFileContext, call *ast.CallExpr) []Finding {
	line := lineFor(fset, call.Pos())
	name := callName(call)

	switch name {
	case "Command", "CommandContext":
		if shell, ok := shellInvocation(call); ok {
			return []Finding{{
				File: path, Line: line, Rule: "sec-exec-shell",
				Message:  "shell invocation (" + shell + ") with dynamic args can enable command injection",
				Severity: ctx.severity("warn"),
			}}
		}
	case "WriteFile":
		if perm, ok := worldWritablePerm(call); ok {
			return []Finding{{
				File: path, Line: line, Rule: "sec-world-writable-file",
				Message:  "os.WriteFile mode " + perm + " is world-writable",
				Severity: ctx.severity("error"),
			}}
		}
	case "HTML":
		if isTemplateHTML(call) && !allStringLiterals(call.Args) {
			return []Finding{{
				File: path, Line: line, Rule: "sec-template-html-dynamic",
				Message:  "template.HTML with non-literal input can enable XSS",
				Severity: ctx.severity("error"),
			}}
		}
	}

	// tls.Config{InsecureSkipVerify: true}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "InsecureSkipVerify" {
		if isTrueBool(call) {
			return []Finding{{
				File: path, Line: line, Rule: "sec-insecure-tls",
				Message:  "InsecureSkipVerify disables TLS peer validation",
				Severity: ctx.severity("error"),
			}}
		}
	}

	return nil
}

func lintGoComposite(fset *token.FileSet, path string, ctx goFileContext, lit *ast.CompositeLit) []Finding {
	var out []Finding
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		line := lineFor(fset, kv.Pos())
		switch key.Name {
		case "InsecureSkipVerify":
			if boolLit(kv.Value) {
				out = append(out, Finding{
					File: path, Line: line, Rule: "sec-insecure-tls",
					Message:  "InsecureSkipVerify disables TLS peer validation",
					Severity: ctx.severity("error"),
				})
			}
		case "AllowOriginFunc", "AllowedOrigins":
			if stringSliceContainsWildcard(kv.Value) {
				out = append(out, Finding{
					File: path, Line: line, Rule: "sec-cors-wildcard",
					Message:  "CORS wildcard origin allows any site to call this API",
					Severity: ctx.severity("warn"),
				})
			}
		}
	}
	return out
}

func lintGoStringLit(fset *token.FileSet, path string, ctx goFileContext, lit *ast.BasicLit) []Finding {
	raw, err := strconvUnquote(lit.Value)
	if err != nil {
		return nil
	}
	if ctx.isTest && (strings.Contains(raw, "example.com") || strings.Contains(raw, "test")) {
		// test fixtures are lower priority
	}
	line := lineFor(fset, lit.Pos())

	if rePrivateKey.MatchString(raw) {
		return []Finding{{
			File: path, Line: line, Rule: "sec-hardcoded-private-key",
			Message:  "PEM private key embedded in source",
			Severity: "error",
		}}
	}
	if reAWSKey.MatchString(raw) {
		return []Finding{{
			File: path, Line: line, Rule: "sec-hardcoded-aws-key",
			Message:  "possible AWS access key in source",
			Severity: "error",
		}}
	}
	if reProviderKey.MatchString(raw) && !isBenignKeyLiteral(raw) && !isHelpExample(ctx, raw) {
		return []Finding{{
			File: path, Line: line, Rule: "sec-hardcoded-provider-key",
			Message:  "possible live provider API key in source",
			Severity: ctx.severity("error"),
		}}
	}
	if reAssignSecret.MatchString(raw) {
		return []Finding{{
			File: path, Line: line, Rule: "sec-hardcoded-credential",
			Message:  "possible hardcoded credential assignment",
			Severity: ctx.severity("error"),
		}}
	}
	return nil
}

func isBenignKeyLiteral(raw string) bool {
	lower := strings.ToLower(raw)
	for _, frag := range []string{
		"fake-dev", "fake-", "placeholder", "your_", "your-", "xxx", "...", "example",
		"api03-...", "not valid", "redact",
	} {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	return false
}

func isHelpExample(ctx goFileContext, raw string) bool {
	if !ctx.inCmd {
		return false
	}
	// CLI help examples like sk-iw-xxx, sk-ant-api03-...
	return strings.Contains(raw, "xxx") ||
		strings.Contains(raw, "...") ||
		strings.Contains(raw, "api03-") && strings.HasSuffix(strings.TrimSpace(raw), "...")
}

func strconvUnquote(s string) (string, error) {
	return strconv.Unquote(s)
}

func shellInvocation(call *ast.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	arg0, ok := call.Args[0].(*ast.BasicLit)
	if !ok || arg0.Kind != token.STRING {
		return "", false
	}
	name, _ := strconvUnquote(arg0.Value)
	switch name {
	case "sh", "bash", "zsh", "/bin/sh", "/bin/bash":
		if len(call.Args) >= 2 {
			if arg1, ok := call.Args[1].(*ast.BasicLit); ok {
				if v, _ := strconvUnquote(arg1.Value); v == "-c" {
					return name, true
				}
			}
		}
	}
	return "", false
}

func worldWritablePerm(call *ast.CallExpr) (string, bool) {
	if len(call.Args) < 3 {
		return "", false
	}
	lit, ok := call.Args[2].(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return "", false
	}
	v, _ := strconvUnquote(lit.Value)
	if v == "0777" || v == "0o777" || v == "0O777" {
		return v, true
	}
	return "", false
}

func isTemplateHTML(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "HTML"
}

func isTrueBool(call *ast.CallExpr) bool {
	return len(call.Args) > 0 && boolLit(call.Args[0])
}

func boolLit(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "true"
}

func stringSliceContainsWildcard(expr ast.Expr) bool {
	cl, ok := expr.(*ast.CompositeLit)
	if !ok {
		if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			v, _ := strconvUnquote(lit.Value)
			return v == "*"
		}
		return false
	}
	for _, elt := range cl.Elts {
		lit, ok := elt.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		v, _ := strconvUnquote(lit.Value)
		if v == "*" {
			return true
		}
	}
	return false
}

func allStringLiterals(args []ast.Expr) bool {
	if len(args) == 0 {
		return true
	}
	for _, a := range args {
		if _, ok := a.(*ast.BasicLit); !ok {
			return false
		}
	}
	return true
}

func lineFor(fset *token.FileSet, pos token.Pos) int {
	file := fset.File(pos)
	if file == nil {
		return 1
	}
	return file.Line(pos)
}

func callName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	default:
		return ""
	}
}
