// Package piiloglint statically analyzes Go source files for logging statements
// that leak raw request/response bodies, credentials or token prefixes, or
// per-request caller identifiers (user IDs, client IPs) into logs.
package piiloglint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Rule names emitted by the linter.
const (
	RuleRawBody        = "pii-log-raw-body"
	RuleCredential     = "pii-log-credential"
	RuleUserIdentifier = "pii-log-user-identifier"
	RuleUnusedIgnore   = "pii-log-unused-ignore"
)

// Finding describes a single PII/secret logging violation.
type Finding struct {
	File    string
	Line    int
	Rule    string
	Message string
}

// LintTree walks dir recursively and lints all non-test Go files.
// It returns the findings, the number of Go files scanned, and any error
// encountered accessing or walking dir.
func LintTree(dir string) ([]Finding, int, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, 0, err
	}
	if !info.IsDir() {
		return nil, 0, fmt.Errorf("%s is not a directory", dir)
	}
	var out []Finding
	filesScanned := 0
	walkErr := filepath.Walk(dir, func(path string, fi os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		if fi.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		filesScanned++
		out = append(out, LintFile(path)...)
		return nil
	})
	return out, filesScanned, walkErr
}

// LintFile parses and lints a single Go file on disk.
func LintFile(path string) []Finding {
	src, err := os.ReadFile(path)
	if err != nil {
		return []Finding{{
			File:    path,
			Line:    1,
			Rule:    "read-error",
			Message: err.Error(),
		}}
	}
	findings, err := LintSource(path, src)
	if err != nil {
		return []Finding{{
			File:    path,
			Line:    1,
			Rule:    "parse-error",
			Message: err.Error(),
		}}
	}
	return findings
}

// LintSource parses and lints Go source bytes with the given filename.
func LintSource(filename string, src []byte) ([]Finding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	ignores := collectIgnoreDirectives(fset, src, f.Comments)
	var findings []Finding

	// Inspect each top-level function (and package-level func literals)
	// independently so local taint and scalar-type tracking stay scoped.
	ast.Inspect(f, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if fn.Body != nil {
				scalars := collectScalarVars(fn.Type, fn.Body)
				findings = append(findings, lintBlock(fset, filename, fn.Body, ignores, scalars)...)
			}
			return false
		case *ast.FuncLit:
			if fn.Body != nil {
				scalars := collectScalarVars(fn.Type, fn.Body)
				findings = append(findings, lintBlock(fset, filename, fn.Body, ignores, scalars)...)
			}
			return false
		}
		return true
	})

	for _, dirLine := range ignores.unusedDirectiveLines() {
		findings = append(findings, Finding{
			File:    filename,
			Line:    dirLine,
			Rule:    RuleUnusedIgnore,
			Message: "piiloglint:ignore directive did not suppress any finding",
		})
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Rule < findings[j].Rule
	})

	return findings, nil
}

type taintKind uint8

const (
	taintNone taintKind = iota
	taintRawBody
	taintCredential
	taintUserID
)

func lintBlock(
	fset *token.FileSet,
	filename string,
	body *ast.BlockStmt,
	ignores *ignoreTracker,
	scalars map[string]bool,
) []Finding {
	tainted := collectLocalTaints(body, scalars)
	var out []Finding
	seen := make(map[findingKey]bool)

	addFinding := func(callLine int, pos token.Pos, rule, msg string) {
		line := lineFor(fset, pos)
		if ignores.suppress(callLine, line) {
			return
		}
		k := findingKey{line: line, rule: rule, msg: msg}
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, Finding{
			File:    filename,
			Line:    line,
			Rule:    rule,
			Message: msg,
		})
	}

	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		kind, msgIdx, argsStart, ok := classifyLogCall(call)
		if !ok {
			return true
		}
		callLine := lineFor(fset, call.Pos())
		emit := func(pos token.Pos, rule, msg string) {
			addFinding(callLine, pos, rule, msg)
		}

		// 1. Inspect the message/format argument itself (P1-2, P1-4).
		var fmtStr string
		hasFmtLit := false
		if msgIdx >= 0 && msgIdx < len(call.Args) {
			msgArg := call.Args[msgIdx]
			if s, isLit := stringLiteralValue(msgArg); isLit {
				fmtStr = s
				hasFmtLit = true
				if kind == logPrintf {
					for _, hit := range checkFormatString(fmtStr) {
						emit(msgArg.Pos(), hit.rule, hit.msg)
					}
				}
			} else {
				// Dynamic message/format expression (e.g. fmt.Sprintf(...) or "body: " + string(body)).
				for _, hit := range inspectExpr(msgArg, tainted, scalars) {
					emit(msgArg.Pos(), hit.rule, hit.msg)
				}
				inspectNestedSprintf(msgArg, tainted, scalars, emit)
			}
		}

		// 2. Inspect logged value arguments.
		if argsStart >= len(call.Args) {
			return true
		}
		args := call.Args[argsStart:]
		switch kind {
		case logSlog:
			lintSlogArgs(fset, call, args, tainted, scalars, emit)
		case logPrintf:
			lintPrintfArgs(fmtStr, hasFmtLit, args, tainted, scalars, emit)
		default:
			for _, arg := range args {
				for _, hit := range inspectExpr(arg, tainted, scalars) {
					emit(arg.Pos(), hit.rule, hit.msg)
				}
				inspectNestedSprintf(arg, tainted, scalars, emit)
			}
		}
		return true
	})

	return out
}

type findingKey struct {
	line int
	rule string
	msg  string
}

type ruleHit struct {
	rule string
	msg  string
}

func lintPrintfArgs(
	fmtStr string,
	hasFmtLit bool,
	args []ast.Expr,
	tainted map[string]taintKind,
	scalars map[string]bool,
	addFinding func(token.Pos, string, string),
) {
	verbs := parseFormatVerbs(fmtStr)
	fmtHasBodyKeyword := hasFmtLit && containsBodyKeyword(fmtStr)

	for i, arg := range args {
		var verb formatVerb
		hasVerb := hasFmtLit && i < len(verbs)
		if hasVerb {
			verb = verbs[i]
		}

		// Pure numeric/boolean/type verbs (%d, %t, %f, %T, etc.) cannot render raw strings/PII.
		if hasVerb && verb.isSafeScalar() {
			continue
		}

		// %+v / %#v body/chunk dump guard (P1-4).
		if hasVerb && verb.isDetailedStruct() && !isSafeWrapperCall(arg) {
			if fmtHasBodyKeyword || exprHasBodyKeyword(arg) {
				addFinding(arg.Pos(), RuleRawBody, "logging body/chunk with "+verb.raw+" without redact.LogPreview")
				continue
			}
		}

		for _, hit := range inspectExpr(arg, tainted, scalars) {
			addFinding(arg.Pos(), hit.rule, hit.msg)
		}
		inspectNestedSprintf(arg, tainted, scalars, addFinding)
	}
}

// inspectNestedSprintf checks fmt.Sprintf calls embedded inside a log argument
// (including dynamic message arguments like logger.Info(fmt.Sprintf(...))),
// skipping calls already protected by an approved safe wrapper such as
// redact.LogPreview(ctx, fmt.Sprintf("%+v", chunkData), 200).
func inspectNestedSprintf(
	expr ast.Expr,
	tainted map[string]taintKind,
	scalars map[string]bool,
	addFinding func(token.Pos, string, string),
) {
	var walk func(e ast.Expr)
	walk = func(e ast.Expr) {
		if e == nil || isSafeWrapperCall(e) {
			return
		}
		switch x := e.(type) {
		case *ast.CallExpr:
			if isFmtSprintfCall(x) && len(x.Args) > 0 {
				if fmtStr, ok := stringLiteralValue(x.Args[0]); ok {
					for _, hit := range checkFormatString(fmtStr) {
						addFinding(x.Args[0].Pos(), hit.rule, hit.msg)
					}
					if len(x.Args) > 1 {
						lintPrintfArgs(fmtStr, true, x.Args[1:], tainted, scalars, addFinding)
					}
					return
				}
			}
			for _, a := range x.Args {
				walk(a)
			}
		case *ast.BinaryExpr:
			if !isBoolBinaryOp(x.Op) {
				walk(x.X)
				walk(x.Y)
			}
		case *ast.ParenExpr:
			walk(x.X)
		}
	}
	walk(expr)
}

func isFmtSprintfCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "fmt" && sel.Sel.Name == "Sprintf"
}

func lintSlogArgs(
	fset *token.FileSet,
	call *ast.CallExpr,
	args []ast.Expr,
	tainted map[string]taintKind,
	scalars map[string]bool,
	addFinding func(token.Pos, string, string),
) {
	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Typed slog.Attr constructors: slog.String, slog.Any, slog.Group, etc.
		if attrCall, ok := arg.(*ast.CallExpr); ok {
			if attrName, isAttr := slogAttrConstructorName(attrCall); isAttr {
				switch attrName {
				case "Int", "Int64", "Uint64", "Float64", "Bool", "Time", "Duration":
					// Scalar numeric/boolean/time attributes cannot leak raw strings/bodies/keys.
					continue
				case "Group":
					if len(attrCall.Args) > 1 {
						lintSlogArgs(fset, call, attrCall.Args[1:], tainted, scalars, addFinding)
					}
					continue
				default:
					if len(attrCall.Args) >= 2 {
						keyName, _ := stringLiteralValue(attrCall.Args[0])
						valExpr := attrCall.Args[1]
						checkSlogKV(keyName, valExpr, attrCall.Pos(), tainted, scalars, addFinding)
					}
					continue
				}
			}
		}

		// Alternating key, value pairs: "key", val
		if keyName, ok := stringLiteralValue(arg); ok && i+1 < len(args) {
			valExpr := args[i+1]
			checkSlogKV(keyName, valExpr, valExpr.Pos(), tainted, scalars, addFinding)
			i++
			continue
		}

		// Variadic or non-literal slog argument (e.g. attrs...) — still inspect it.
		for _, hit := range inspectExpr(arg, tainted, scalars) {
			addFinding(arg.Pos(), hit.rule, hit.msg)
		}
		inspectNestedSprintf(arg, tainted, scalars, addFinding)
	}
}

func checkSlogKV(
	keyName string,
	valExpr ast.Expr,
	pos token.Pos,
	tainted map[string]taintKind,
	scalars map[string]bool,
	addFinding func(token.Pos, string, string),
) {
	if isSafeWrapperCall(valExpr) || isBoolExpr(valExpr) {
		return
	}
	if ident, ok := valExpr.(*ast.Ident); ok && scalars[ident.Name] {
		return
	}
	hits := inspectExpr(valExpr, tainted, scalars)
	for _, h := range hits {
		addFinding(pos, h.rule, h.msg)
	}
	inspectNestedSprintf(valExpr, tainted, scalars, addFinding)
	if len(hits) > 0 {
		return
	}
	if isNonSensitiveConstant(valExpr) {
		return
	}
	if h, banned := checkBannedSlogKey(keyName); banned {
		addFinding(pos, h.rule, h.msg)
	}
}

// collectScalarVars collects identifiers declared with numeric, boolean, or
// duration types (from function parameters, var specs, and obvious scalar
// short assignments) so count variables named e.g. `prompt int` or
// `messages := len(xs)` do not trigger false positives.
func collectScalarVars(fnType *ast.FuncType, body *ast.BlockStmt) map[string]bool {
	scalars := make(map[string]bool)
	if fnType != nil && fnType.Params != nil {
		for _, field := range fnType.Params.List {
			if isScalarTypeExpr(field.Type) {
				for _, name := range field.Names {
					scalars[name.Name] = true
				}
			}
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.ValueSpec:
			if isScalarTypeExpr(s.Type) {
				for _, name := range s.Names {
					scalars[name.Name] = true
				}
			}
		case *ast.AssignStmt:
			if s.Tok != token.DEFINE {
				return true
			}
			for i, lhs := range s.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || i >= len(s.Rhs) {
					continue
				}
				if isScalarValueExpr(s.Rhs[i]) {
					scalars[ident.Name] = true
				}
			}
		}
		return true
	})
	return scalars
}

func isScalarTypeExpr(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		switch t.Name {
		case "int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
			"float32", "float64", "bool":
			return true
		}
	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok && pkg.Name == "time" && t.Sel.Name == "Duration" {
			return true
		}
	}
	return false
}

func isScalarValueExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Kind == token.INT || e.Kind == token.FLOAT
	case *ast.Ident:
		return e.Name == "true" || e.Name == "false"
	case *ast.BinaryExpr:
		return isBoolBinaryOp(e.Op)
	case *ast.CallExpr:
		if ident, ok := e.Fun.(*ast.Ident); ok {
			switch ident.Name {
			case "len", "cap", "int", "int64", "uint64", "float64":
				return true
			}
		}
	}
	return false
}

// collectLocalTaints scans assignments in a function body to record local
// variables derived directly from unsanitized sensitive values (e.g.
// `token := auth[7:]; tokenID := fmt.Sprintf("token:%s", token[:8])` or
// `excerpt := string(msg.Payload[:200])`).
//
// Taint only propagates through value-preserving transformations (copies,
// slices, string/byte conversions, fmt.Sprintf, string trimming, prefix/truncate
// helpers, and direct sensitive source calls) — never through arbitrary
// functions like json.Unmarshal or store lookups whose return values are
// errors or metadata structs.
func collectLocalTaints(body *ast.BlockStmt, scalars map[string]bool) map[string]taintKind {
	tainted := make(map[string]taintKind)
	for range 2 {
		ast.Inspect(body, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range stmt.Lhs {
					ident, ok := lhs.(*ast.Ident)
					if !ok || ident.Name == "_" || scalars[ident.Name] {
						continue
					}
					if i >= len(stmt.Rhs) {
						continue
					}
					if k := taintFromDerivation(stmt.Rhs[i], tainted, scalars); k != taintNone {
						tainted[ident.Name] = k
					}
				}
			case *ast.ValueSpec:
				for i, ident := range stmt.Names {
					if ident.Name == "_" || scalars[ident.Name] || i >= len(stmt.Values) {
						continue
					}
					if k := taintFromDerivation(stmt.Values[i], tainted, scalars); k != taintNone {
						tainted[ident.Name] = k
					}
				}
			}
			return true
		})
	}
	return tainted
}

func taintFromDerivation(expr ast.Expr, tainted map[string]taintKind, scalars map[string]bool) taintKind {
	if expr == nil || isSafeWrapperCall(expr) || isBoolExpr(expr) {
		return taintNone
	}
	switch x := expr.(type) {
	case *ast.Ident:
		if h, ok := checkIdent(x.Name, tainted, scalars); ok {
			return ruleToTaint(h.rule)
		}
	case *ast.SelectorExpr:
		if h, ok := checkSelector(x); ok {
			return ruleToTaint(h.rule)
		}
	case *ast.SliceExpr:
		return taintFromDerivation(x.X, tainted, scalars)
	case *ast.ParenExpr:
		return taintFromDerivation(x.X, tainted, scalars)
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			if k := taintFromDerivation(x.X, tainted, scalars); k != taintNone {
				return k
			}
			return taintFromDerivation(x.Y, tainted, scalars)
		}
	case *ast.CallExpr:
		if h, ok := checkSensitiveCall(x); ok {
			return ruleToTaint(h.rule)
		}
		if isTaintPropagatingCall(x) {
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "String" || sel.Sel.Name == "Bytes" {
					if k := taintFromDerivation(sel.X, tainted, scalars); k != taintNone {
						return k
					}
				}
			}
			for _, arg := range x.Args {
				if k := taintFromDerivation(arg, tainted, scalars); k != taintNone {
					return k
				}
			}
		}
	}
	return taintNone
}

func isTaintPropagatingCall(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		switch fn.Name {
		case "string", "prefix", "truncate", "chunkPreview":
			return true
		}
	case *ast.ArrayType:
		// []byte(x)
		return true
	case *ast.SelectorExpr:
		if pkg, ok := fn.X.(*ast.Ident); ok {
			switch pkg.Name {
			case "fmt":
				return fn.Sel.Name == "Sprintf" || fn.Sel.Name == "Sprint"
			case "strings", "bytes":
				switch fn.Sel.Name {
				case "TrimSpace", "TrimPrefix", "TrimSuffix", "Trim", "ToLower", "ToUpper", "Join", "Clone", "CutPrefix", "CutSuffix", "Replace", "ReplaceAll":
					return true
				}
			}
		}
		return fn.Sel.Name == "String" || fn.Sel.Name == "Bytes"
	}
	return false
}

func ruleToTaint(rule string) taintKind {
	switch rule {
	case RuleCredential:
		return taintCredential
	case RuleRawBody:
		return taintRawBody
	case RuleUserIdentifier:
		return taintUserID
	default:
		return taintNone
	}
}

type logCallKind uint8

const (
	logNone logCallKind = iota
	logPrintf
	logPrint
	logSlog
)

func classifyLogCall(call *ast.CallExpr) (kind logCallKind, msgIdx int, argsStart int, ok bool) {
	// Repo-local log wrapper functions (cmd/llm-proxy/main.go).
	if ident, isIdent := call.Fun.(*ast.Ident); isIdent {
		switch ident.Name {
		case "logProxyError", "logProxyWarn":
			return logSlog, 0, 1, true
		}
		return logNone, -1, 0, false
	}

	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return logNone, -1, 0, false
	}
	method := sel.Sel.Name

	if pkgIdent, isIdent := sel.X.(*ast.Ident); isIdent {
		switch pkgIdent.Name {
		case "log":
			switch method {
			case "Printf", "Fatalf", "Panicf":
				return logPrintf, 0, 1, true
			case "Print", "Println", "Fatal", "Fatalln", "Panic", "Panicln":
				return logPrint, -1, 0, true
			}
		case "fmt":
			switch method {
			case "Printf":
				return logPrintf, 0, 1, true
			case "Print", "Println":
				return logPrint, -1, 0, true
			case "Fprintf":
				if len(call.Args) >= 2 && isStdOutputExpr(call.Args[0]) {
					return logPrintf, 1, 2, true
				}
			case "Fprint", "Fprintln":
				if len(call.Args) >= 1 && isStdOutputExpr(call.Args[0]) {
					return logPrint, -1, 1, true
				}
			}
		case "proxylog":
			switch method {
			case "Proxy", "Upstream":
				return logPrintf, 0, 1, true
			case "SlogProxy", "SlogUpstream":
				return logSlog, 2, 3, true
			}
		case "slog":
			switch method {
			case "Info", "Warn", "Error", "Debug":
				return logSlog, 0, 1, true
			case "InfoContext", "WarnContext", "ErrorContext", "DebugContext":
				return logSlog, 1, 2, true
			case "Log", "LogAttrs":
				return logSlog, 2, 3, true
			}
		}
	}

	// Receiver-based *slog.Logger or *log.Logger calls (e.g. logger.Info, s.logger.Error, t.log.Warn).
	if looksLikeLoggerExpr(sel.X) {
		switch method {
		case "Info", "Warn", "Error", "Debug":
			return logSlog, 0, 1, true
		case "InfoContext", "WarnContext", "ErrorContext", "DebugContext":
			return logSlog, 1, 2, true
		case "Log", "LogAttrs":
			return logSlog, 2, 3, true
		case "Printf", "Fatalf", "Panicf":
			return logPrintf, 0, 1, true
		case "Print", "Println", "Fatal", "Fatalln", "Panic", "Panicln":
			return logPrint, -1, 0, true
		}
	}

	return logNone, -1, 0, false
}

func isStdOutputExpr(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "os" && (sel.Sel.Name == "Stdout" || sel.Sel.Name == "Stderr")
}

func looksLikeLoggerName(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "log", "logger", "slogger", "stdlog":
		return true
	}
	return strings.HasSuffix(name, "Logger") || strings.HasSuffix(name, "Log")
}

func looksLikeLoggerExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return looksLikeLoggerName(e.Name)
	case *ast.SelectorExpr:
		return looksLikeLoggerName(e.Sel.Name)
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			return looksLikeLoggerName(sel.Sel.Name) ||
				(sel.Sel.Name == "Default" && looksLikeLoggerExpr(sel.X)) ||
				(sel.Sel.Name == "With" && looksLikeLoggerExpr(sel.X))
		}
	}
	return false
}

func slogAttrConstructorName(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || ident.Name != "slog" {
		return "", false
	}
	switch sel.Sel.Name {
	case "String", "Any", "Int", "Int64", "Uint64", "Float64", "Bool", "Time", "Duration", "Group":
		return sel.Sel.Name, true
	}
	return "", false
}

// isSafeWrapperCall reports whether expr is a call to an approved sanitizer or
// metadata-only helper whose return value is safe to log.
func isSafeWrapperCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	var name string
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		name = fn.Name
	case *ast.SelectorExpr:
		name = fn.Sel.Name
	default:
		return false
	}
	switch name {
	case "LogPreview",
		"len", "cap",
		"Valid", // utf8.Valid, json.Valid
		"Sum256", "Sum224", "Sum512",
		"piiWireBodyLogAttrs",
		"AllowedEntityCounts",
		"RedactKey",
		"CredentialHashSuffix",
		"MaskKeyID",
		"MaskProviderCredential",
		"hashUserIDForLog",
		"keyIDHashSuffix",
		"updateFieldNames",
		"formatKeyCostLimit":
		return true
	}
	return false
}

func isNonSensitiveConstant(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		return e.Name == "true" || e.Name == "false" || e.Name == "nil"
	}
	return false
}

func isBoolBinaryOp(op token.Token) bool {
	switch op {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ, token.LAND, token.LOR:
		return true
	}
	return false
}

func isBoolExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BinaryExpr:
		return isBoolBinaryOp(e.Op)
	case *ast.UnaryExpr:
		return e.Op == token.NOT
	case *ast.ParenExpr:
		return isBoolExpr(e.X)
	}
	return false
}

// inspectExpr walks an argument expression looking for unsanitized sensitive
// references, stopping whenever it hits an approved safe wrapper or boolean
// comparison (e.g. `apiKey != ""`).
func inspectExpr(expr ast.Expr, tainted map[string]taintKind, scalars map[string]bool) []ruleHit {
	var hits []ruleHit

	var walk func(e ast.Expr)
	walk = func(e ast.Expr) {
		if e == nil || isSafeWrapperCall(e) || isBoolExpr(e) {
			return
		}

		switch x := e.(type) {
		case *ast.Ident:
			if h, ok := checkIdent(x.Name, tainted, scalars); ok {
				hits = append(hits, h)
			}

		case *ast.SelectorExpr:
			if h, ok := checkSelector(x); ok {
				hits = append(hits, h)
			}

		case *ast.CallExpr:
			if h, ok := checkSensitiveCall(x); ok {
				hits = append(hits, h)
				return
			}
			// Handle method calls on sensitive receivers, e.g. body.String(), respBody.Bytes().
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "String" || sel.Sel.Name == "Bytes" {
					walk(sel.X)
				}
			}
			for _, arg := range x.Args {
				walk(arg)
			}

		case *ast.SliceExpr:
			// Inspect the sliced base (e.g. token[:8], msg.Payload[:200]);
			// index expressions (Low/High/Max) are lengths/offsets, not values.
			walk(x.X)

		case *ast.IndexExpr:
			walk(x.X)

		case *ast.BinaryExpr:
			walk(x.X)
			walk(x.Y)

		case *ast.UnaryExpr:
			walk(x.X)

		case *ast.ParenExpr:
			walk(x.X)

		case *ast.TypeAssertExpr:
			walk(x.X)

		case *ast.StarExpr:
			walk(x.X)

		case *ast.CompositeLit:
			for _, elt := range x.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					walk(kv.Value)
				} else {
					walk(elt)
				}
			}
		}
	}

	walk(expr)
	return hits
}

var (
	rawBodyIdents = map[string]bool{
		"body":          true,
		"bodybytes":     true,
		"reqbody":       true,
		"respbody":      true,
		"requestbody":   true,
		"responsebody":  true,
		"rawbody":       true,
		"upstreambody":  true,
		"chunkdata":     true,
		"responsefield": true,
		"eventfield":    true,
		"payload":       true,
		"payloadbuf":    true,
		"prompt":        true,
		"messages":      true,
		"decompressed":  true,
		"excerpt":       true,
		"rawresponse":   true,
		"rawrequest":    true,
	}

	rawBodyCompoundSubstrings = []string{
		"chunkdata",
		"responsefield",
		"eventfield",
		"bodybytes",
		"reqbody",
		"respbody",
		"requestbody",
		"responsebody",
		"rawbody",
	}

	rawBodyFields = map[string]bool{
		"payload":          true,
		"body":             true,
		"bodybytes":        true,
		"reqbody":          true,
		"respbody":         true,
		"requestbody":      true,
		"responsebody":     true,
		"chunkdata":        true,
		"responsefield":    true,
		"eventfield":       true,
		"prompt":           true,
		"messages":         true,
		"detectedentities": true,
		"allowedentities":  true,
	}

	// Keywords checked on %+v / %#v dumps (restores and extends the original lint-pii-logs gate).
	detailedDumpBodyKeywords = []string{
		"chunkdata",
		"responsefield",
		"eventfield",
		"bodybytes",
		"reqbody",
		"respbody",
		"requestbody",
		"responsebody",
		"payload",
		"prompt",
		"messages",
		"rawbody",
		"body",
		"chunk",
	}

	credentialIdents = map[string]bool{
		"apikey":              true,
		"actualkey":           true,
		"inboundkey":          true,
		"proxykey":            true,
		"rawkey":              true,
		"newkey":              true,
		"providerkey":         true,
		"presentedcredential": true,
		"providercredential":  true,
		"credential":          true,
		"token":               true,
		"tokenid":             true,
		"bearertoken":         true,
		"accesstoken":         true,
		"idtoken":             true,
		"auth":                true,
		"authheader":          true,
		"authorization":       true,
		"secret":              true,
		"password":            true,
		"keyprefix":           true,
		"tokenprefix":         true,
	}

	credentialFields = map[string]bool{
		"actualkey":     true,
		"apikey":        true,
		"createdkey":    true,
		"secret":        true,
		"password":      true,
		"token":         true,
		"authorization": true,
	}

	pkKeyReceivers = map[string]bool{
		"key":      true,
		"rec":      true,
		"record":   true,
		"k":        true,
		"apikey":   true,
		"existing": true,
		"created":  true,
	}

	userIDIdents = map[string]bool{
		"userid":     true,
		"uservalue":  true,
		"ipaddr":     true,
		"clientip":   true,
		"forwarded":  true,
		"realip":     true,
		"agentlabel": true,
		"callerid":   true,
	}

	userIDFields = map[string]bool{
		"userid":    true,
		"clientip":  true,
		"ipaddress": true,
	}
)

func hasRawBodyCompoundSubstring(lower string) bool {
	if strings.HasPrefix(lower, "max") || strings.HasPrefix(lower, "min") ||
		strings.HasSuffix(lower, "limit") || strings.HasSuffix(lower, "count") ||
		strings.HasSuffix(lower, "size") || strings.HasSuffix(lower, "len") ||
		strings.HasSuffix(lower, "length") {
		return false
	}
	for _, sub := range rawBodyCompoundSubstrings {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

func containsBodyKeyword(s string) bool {
	lower := strings.ToLower(s)
	for _, kw := range detailedDumpBodyKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func exprHasBodyKeyword(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && isSafeWrapperCall(call) {
			return false
		}
		switch x := n.(type) {
		case *ast.Ident:
			if containsBodyKeyword(x.Name) {
				found = true
				return false
			}
		case *ast.SelectorExpr:
			if containsBodyKeyword(x.Sel.Name) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func checkIdent(name string, tainted map[string]taintKind, scalars map[string]bool) (ruleHit, bool) {
	if scalars[name] {
		return ruleHit{}, false
	}
	lower := strings.ToLower(name)
	if credentialIdents[lower] {
		return ruleHit{
			rule: RuleCredential,
			msg:  "logging raw credential/token variable " + name + " (use apikeys.RedactKey or apikeys.CredentialHashSuffix, or omit)",
		}, true
	}
	if rawBodyIdents[lower] || hasRawBodyCompoundSubstring(lower) {
		return ruleHit{
			rule: RuleRawBody,
			msg:  "logging raw body/payload variable " + name + " without redact.LogPreview",
		}, true
	}
	if userIDIdents[lower] {
		return ruleHit{
			rule: RuleUserIdentifier,
			msg:  "logging raw user identifier/IP variable " + name + " (hash or omit)",
		}, true
	}
	if k, ok := tainted[name]; ok && k != taintNone {
		switch k {
		case taintCredential:
			return ruleHit{
				rule: RuleCredential,
				msg:  "logging variable " + name + " derived from a credential/token",
			}, true
		case taintRawBody:
			return ruleHit{
				rule: RuleRawBody,
				msg:  "logging variable " + name + " derived from raw body/payload without redact.LogPreview",
			}, true
		case taintUserID:
			return ruleHit{
				rule: RuleUserIdentifier,
				msg:  "logging variable " + name + " derived from a user identifier/IP (hash or omit)",
			}, true
		}
	}
	return ruleHit{}, false
}

func checkSelector(sel *ast.SelectorExpr) (ruleHit, bool) {
	field := strings.ToLower(sel.Sel.Name)

	// key.PK / rec.PK / record.PK holds the full proxy API key.
	if field == "pk" {
		if recvName := lastIdentName(sel.X); pkKeyReceivers[strings.ToLower(recvName)] {
			return ruleHit{
				rule: RuleCredential,
				msg:  "logging raw proxy API key field " + recvName + ".PK (use apikeys.RedactKey or apikeys.CredentialHashSuffix)",
			}, true
		}
	}

	if credentialFields[field] {
		return ruleHit{
			rule: RuleCredential,
			msg:  "logging raw credential field ." + sel.Sel.Name + " (redact, hash, or omit)",
		}, true
	}
	if rawBodyFields[field] || hasRawBodyCompoundSubstring(field) {
		return ruleHit{
			rule: RuleRawBody,
			msg:  "logging raw body/payload field ." + sel.Sel.Name + " without redact.LogPreview",
		}, true
	}
	if userIDFields[field] {
		return ruleHit{
			rule: RuleUserIdentifier,
			msg:  "logging raw user identifier/IP field ." + sel.Sel.Name + " (hash or omit)",
		}, true
	}
	return ruleHit{}, false
}

func checkSensitiveCall(call *ast.CallExpr) (ruleHit, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		if ident, ok := call.Fun.(*ast.Ident); ok {
			return checkSensitiveCallName(ident.Name)
		}
		return ruleHit{}, false
	}

	// Header.Get("<sensitive>") or Query().Get("llm_user_id")
	if sel.Sel.Name == "Get" && len(call.Args) == 1 {
		if key, ok := stringLiteralValue(call.Args[0]); ok {
			switch strings.ToLower(key) {
			case "authorization", "x-api-key", "x-goog-api-key":
				return ruleHit{
					rule: RuleCredential,
					msg:  "logging sensitive auth header " + key,
				}, true
			case "x-user-id", "x-forwarded-for", "x-real-ip", "llm_user_id":
				return ruleHit{
					rule: RuleUserIdentifier,
					msg:  "logging raw caller identifier from " + key + " (hash or omit)",
				}, true
			}
		}
	}

	return checkSensitiveCallName(sel.Sel.Name)
}

func checkSensitiveCallName(name string) (ruleHit, bool) {
	switch name {
	case "extractRateLimitAPIKey", "extractInboundProxyKey", "mantleProxyKeyFromRequest":
		return ruleHit{
			rule: RuleCredential,
			msg:  "logging raw credential returned by " + name + " (redact or hash)",
		}, true
	case "ExtractUserIDFromRequest", "ExtractIPAddressFromRequest", "RequestUserID", "UserIDFromRequest", "TrustedClientIP":
		return ruleHit{
			rule: RuleUserIdentifier,
			msg:  "logging raw user identifier/IP returned by " + name + " (hash or omit)",
		}, true
	}
	return ruleHit{}, false
}

var (
	reFormatKeyPrefix = regexp.MustCompile(`(?i)\b(key_prefix|token_prefix|api_key|actual_key|bearer_token)\s*=\s*%`)
	reFormatUserID    = regexp.MustCompile(`(?i)\b(user|user_id|client_ip|ip_addr)\s*=\s*%`)
)

func checkFormatString(fmtStr string) []ruleHit {
	var hits []ruleHit
	if m := reFormatKeyPrefix.FindStringSubmatch(fmtStr); len(m) > 1 {
		hits = append(hits, ruleHit{
			rule: RuleCredential,
			msg:  "format string logs credential/prefix field " + m[1] + "= (use key_hash= with a hash instead)",
		})
	}
	if m := reFormatUserID.FindStringSubmatch(fmtStr); len(m) > 1 {
		hits = append(hits, ruleHit{
			rule: RuleUserIdentifier,
			msg:  "format string logs raw caller identifier field " + m[1] + "= (use user_hash= or omit)",
		})
	}
	return hits
}

func checkBannedSlogKey(key string) (ruleHit, bool) {
	switch strings.ToLower(key) {
	case "key_prefix", "token_prefix", "api_key", "actual_key", "authorization", "bearer_token", "secret", "password":
		return ruleHit{
			rule: RuleCredential,
			msg:  "slog attribute " + strconv.Quote(key) + " logs a credential or key prefix",
		}, true
	case "user_id", "client_ip", "ip_address":
		return ruleHit{
			rule: RuleUserIdentifier,
			msg:  "slog attribute " + strconv.Quote(key) + " logs a raw user identifier or client IP (hash or omit)",
		}, true
	case "payload", "request_body", "response_body", "prompt":
		return ruleHit{
			rule: RuleRawBody,
			msg:  "slog attribute " + strconv.Quote(key) + " logs raw payload/body without redact.LogPreview",
		}, true
	}
	return ruleHit{}, false
}

type formatVerb struct {
	raw   string
	flags string
	verb  byte
}

func (v formatVerb) isDetailedStruct() bool {
	return v.verb == 'v' && (strings.ContainsRune(v.flags, '+') || strings.ContainsRune(v.flags, '#'))
}

func (v formatVerb) isSafeScalar() bool {
	switch v.verb {
	case 'd', 'b', 'o', 'O', 't', 'f', 'F', 'e', 'E', 'g', 'G', 'T', 'p':
		return true
	}
	return false
}

// parseFormatVerbs extracts positional printf verbs from fmtStr.
// If explicit argument indexing (e.g. %[1]s) or dynamic width (*) is present,
// it returns nil so callers fall back to inspecting every argument.
func parseFormatVerbs(fmtStr string) []formatVerb {
	var verbs []formatVerb
	for i := 0; i < len(fmtStr); i++ {
		if fmtStr[i] != '%' {
			continue
		}
		start := i
		i++
		if i >= len(fmtStr) {
			break
		}
		if fmtStr[i] == '%' {
			continue
		}
		flagStart := i
		for i < len(fmtStr) {
			c := fmtStr[i]
			if c == '+' || c == '-' || c == '#' || c == ' ' || c == '0' {
				i++
				continue
			}
			break
		}
		flags := fmtStr[flagStart:i]
		for i < len(fmtStr) {
			c := fmtStr[i]
			if c == '[' || c == '*' {
				return nil
			}
			if (c >= '0' && c <= '9') || c == '.' {
				i++
				continue
			}
			break
		}
		if i >= len(fmtStr) {
			break
		}
		verbs = append(verbs, formatVerb{
			raw:   fmtStr[start : i+1],
			flags: flags,
			verb:  fmtStr[i],
		})
	}
	return verbs
}

func lastIdentName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	default:
		return ""
	}
}

func stringLiteralValue(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func lineFor(fset *token.FileSet, pos token.Pos) int {
	file := fset.File(pos)
	if file == nil {
		return 1
	}
	return file.Line(pos)
}

type ignoreTracker struct {
	// inlineLineToDirective maps line -> directive line for inline comments.
	// An inline comment suppresses ONLY findings on that exact line.
	inlineLineToDirective map[int]int
	// standaloneTargetToDirective maps (dirLine+1) -> directive line for
	// standalone preceding comments. A standalone comment suppresses findings
	// on dirLine+1 AND any finding inside a log call starting on dirLine+1.
	standaloneTargetToDirective map[int]int
	directiveLines              []int
	used                        map[int]bool
}

func (t *ignoreTracker) suppress(callLine, findingLine int) bool {
	if t == nil {
		return false
	}
	if dirLine, ok := t.inlineLineToDirective[findingLine]; ok {
		t.used[dirLine] = true
		return true
	}
	if dirLine, ok := t.standaloneTargetToDirective[findingLine]; ok {
		t.used[dirLine] = true
		return true
	}
	if dirLine, ok := t.standaloneTargetToDirective[callLine]; ok {
		t.used[dirLine] = true
		return true
	}
	return false
}

func (t *ignoreTracker) unusedDirectiveLines() []int {
	if t == nil {
		return nil
	}
	var unused []int
	for _, dl := range t.directiveLines {
		if !t.used[dl] {
			unused = append(unused, dl)
		}
	}
	return unused
}

// collectIgnoreDirectives parses `//piiloglint:ignore <reason>` comments.
//
// Rules:
//   - The comment text (after stripping // or /* */ and leading spaces) MUST
//     begin with `piiloglint:ignore` followed by whitespace and a non-empty
//     reason. Arbitrary prose mentioning "piiloglint:ignore" does NOT match.
//   - An inline directive (non-whitespace code precedes the comment on the same
//     line) applies ONLY to that exact line.
//   - A standalone directive (only whitespace precedes the comment on the same
//     line) applies ONLY to the immediately following line / statement (line+1).
func collectIgnoreDirectives(fset *token.FileSet, src []byte, groups []*ast.CommentGroup) *ignoreTracker {
	tr := &ignoreTracker{
		inlineLineToDirective:       make(map[int]int),
		standaloneTargetToDirective: make(map[int]int),
		used:                        make(map[int]bool),
	}
	const directive = "piiloglint:ignore"

	for _, g := range groups {
		for _, c := range g.List {
			var text string
			if strings.HasPrefix(c.Text, "//") {
				text = strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
			} else if strings.HasPrefix(c.Text, "/*") {
				text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(c.Text, "/*"), "*/"))
			} else {
				continue
			}
			if !strings.HasPrefix(text, directive) {
				continue
			}
			rest := text[len(directive):]
			if len(rest) == 0 || (rest[0] != ' ' && rest[0] != '\t') {
				continue
			}
			if strings.TrimSpace(rest) == "" {
				continue
			}

			pos := fset.Position(c.Pos())
			dirLine := pos.Line
			tr.directiveLines = append(tr.directiveLines, dirLine)
			if isInlineComment(src, pos.Offset) {
				tr.inlineLineToDirective[dirLine] = dirLine
			} else {
				tr.standaloneTargetToDirective[dirLine+1] = dirLine
			}
		}
	}
	return tr
}

// isInlineComment reports whether non-whitespace characters precede offset on
// the same source line.
func isInlineComment(src []byte, offset int) bool {
	if offset <= 0 || offset > len(src) {
		return false
	}
	for i := offset - 1; i >= 0; i-- {
		if src[i] == '\n' {
			return false
		}
		if src[i] != ' ' && src[i] != '\t' && src[i] != '\r' {
			return true
		}
	}
	return false
}
