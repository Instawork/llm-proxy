// Package piiloglint statically analyzes Go source files for logging statements
// that leak raw request/response bodies, credentials or token prefixes, or
// per-request caller identifiers (user IDs, client IPs) into logs.
package piiloglint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Rule names emitted by the linter.
const (
	RuleRawBody        = "pii-log-raw-body"
	RuleCredential     = "pii-log-credential"
	RuleUserIdentifier = "pii-log-user-identifier"
)

// Finding describes a single PII/secret logging violation.
type Finding struct {
	File    string
	Line    int
	Rule    string
	Message string
}

// LintTree walks dir recursively and lints all non-test Go files.
func LintTree(dir string) []Finding {
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	var out []Finding
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		out = append(out, LintFile(path)...)
		return nil
	})
	return out
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

	ignoredLines := collectIgnoredLines(fset, f.Comments)
	var findings []Finding

	// Inspect each top-level function (and package-level func literals)
	// independently so local taint tracking stays scoped to the enclosing function.
	ast.Inspect(f, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if fn.Body != nil {
				findings = append(findings, lintBlock(fset, filename, fn.Body, ignoredLines)...)
			}
			return false
		case *ast.FuncLit:
			if fn.Body != nil {
				findings = append(findings, lintBlock(fset, filename, fn.Body, ignoredLines)...)
			}
			return false
		}
		return true
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

func lintBlock(fset *token.FileSet, filename string, body *ast.BlockStmt, ignoredLines map[int]bool) []Finding {
	tainted := collectLocalTaints(body)
	var out []Finding
	seen := make(map[findingKey]bool)

	addFinding := func(pos token.Pos, rule, msg string) {
		line := lineFor(fset, pos)
		if ignoredLines[line] {
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
		if ignoredLines[callLine] {
			return true
		}

		// 1. Check format string for banned label patterns (e.g. key_prefix=%s, user=%s).
		if kind == logPrintf && msgIdx >= 0 && msgIdx < len(call.Args) {
			if fmtStr, ok := stringLiteralValue(call.Args[msgIdx]); ok {
				for _, hit := range checkFormatString(fmtStr) {
					addFinding(call.Pos(), hit.rule, hit.msg)
				}
			}
		}

		// 2. Inspect logged arguments.
		if argsStart >= len(call.Args) {
			return true
		}
		args := call.Args[argsStart:]
		if kind == logSlog {
			lintSlogArgs(fset, call, args, tainted, ignoredLines, addFinding)
		} else {
			for _, arg := range args {
				if ignoredLines[lineFor(fset, arg.Pos())] {
					continue
				}
				for _, hit := range inspectExpr(arg, tainted) {
					addFinding(arg.Pos(), hit.rule, hit.msg)
				}
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

func lintSlogArgs(
	fset *token.FileSet,
	call *ast.CallExpr,
	args []ast.Expr,
	tainted map[string]taintKind,
	ignoredLines map[int]bool,
	addFinding func(token.Pos, string, string),
) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if ignoredLines[lineFor(fset, arg.Pos())] {
			continue
		}

		// Typed slog.Attr constructors: slog.String, slog.Any, slog.Group, etc.
		if attrCall, ok := arg.(*ast.CallExpr); ok {
			if attrName, isAttr := slogAttrConstructorName(attrCall); isAttr {
				switch attrName {
				case "Int", "Int64", "Uint64", "Float64", "Bool", "Time", "Duration":
					// Scalar numeric/boolean/time attributes cannot leak raw strings/bodies/keys.
					continue
				case "Group":
					if len(attrCall.Args) > 1 {
						lintSlogArgs(fset, call, attrCall.Args[1:], tainted, ignoredLines, addFinding)
					}
					continue
				default:
					if len(attrCall.Args) >= 2 {
						keyName, _ := stringLiteralValue(attrCall.Args[0])
						valExpr := attrCall.Args[1]
						checkSlogKV(keyName, valExpr, attrCall.Pos(), tainted, addFinding)
					}
					continue
				}
			}
		}

		// Alternating key, value pairs: "key", val
		if keyName, ok := stringLiteralValue(arg); ok && i+1 < len(args) {
			valExpr := args[i+1]
			if !ignoredLines[lineFor(fset, valExpr.Pos())] {
				checkSlogKV(keyName, valExpr, valExpr.Pos(), tainted, addFinding)
			}
			i++
			continue
		}

		// Variadic or non-literal slog argument (e.g. attrs...) — still inspect it.
		for _, hit := range inspectExpr(arg, tainted) {
			addFinding(arg.Pos(), hit.rule, hit.msg)
		}
	}
}

func checkSlogKV(
	keyName string,
	valExpr ast.Expr,
	pos token.Pos,
	tainted map[string]taintKind,
	addFinding func(token.Pos, string, string),
) {
	if isSafeWrapperCall(valExpr) {
		return
	}
	hits := inspectExpr(valExpr, tainted)
	for _, h := range hits {
		addFinding(pos, h.rule, h.msg)
	}
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
func collectLocalTaints(body *ast.BlockStmt) map[string]taintKind {
	tainted := make(map[string]taintKind)
	for range 2 {
		ast.Inspect(body, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range stmt.Lhs {
					ident, ok := lhs.(*ast.Ident)
					if !ok || ident.Name == "_" {
						continue
					}
					if i >= len(stmt.Rhs) {
						continue
					}
					if k := taintFromDerivation(stmt.Rhs[i], tainted); k != taintNone {
						tainted[ident.Name] = k
					}
				}
			case *ast.ValueSpec:
				for i, ident := range stmt.Names {
					if ident.Name == "_" || i >= len(stmt.Values) {
						continue
					}
					if k := taintFromDerivation(stmt.Values[i], tainted); k != taintNone {
						tainted[ident.Name] = k
					}
				}
			}
			return true
		})
	}
	return tainted
}

func taintFromDerivation(expr ast.Expr, tainted map[string]taintKind) taintKind {
	if expr == nil || isSafeWrapperCall(expr) {
		return taintNone
	}
	switch x := expr.(type) {
	case *ast.Ident:
		if h, ok := checkIdent(x.Name, tainted); ok {
			return ruleToTaint(h.rule)
		}
	case *ast.SelectorExpr:
		if h, ok := checkSelector(x); ok {
			return ruleToTaint(h.rule)
		}
	case *ast.SliceExpr:
		return taintFromDerivation(x.X, tainted)
	case *ast.ParenExpr:
		return taintFromDerivation(x.X, tainted)
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			if k := taintFromDerivation(x.X, tainted); k != taintNone {
				return k
			}
			return taintFromDerivation(x.Y, tainted)
		}
	case *ast.CallExpr:
		if h, ok := checkSensitiveCall(x); ok {
			return ruleToTaint(h.rule)
		}
		if isTaintPropagatingCall(x) {
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "String" || sel.Sel.Name == "Bytes" {
					if k := taintFromDerivation(sel.X, tainted); k != taintNone {
						return k
					}
				}
			}
			for _, arg := range x.Args {
				if k := taintFromDerivation(arg, tainted); k != taintNone {
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
				case "TrimSpace", "TrimPrefix", "TrimSuffix", "Trim", "ToLower", "ToUpper", "Join", "Clone":
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

func looksLikeLoggerExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return strings.Contains(strings.ToLower(e.Name), "log")
	case *ast.SelectorExpr:
		return strings.Contains(strings.ToLower(e.Sel.Name), "log")
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			return strings.Contains(strings.ToLower(sel.Sel.Name), "log") ||
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
		"hashKeyForLog", "hashUserIDForLog", "hashForLog", "logKeyHash", "logUserHash",
		"keyIDHashSuffix",
		"updateFieldNames":
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

// inspectExpr walks an argument expression looking for unsanitized sensitive
// references, stopping whenever it hits an approved safe wrapper.
func inspectExpr(expr ast.Expr, tainted map[string]taintKind) []ruleHit {
	var hits []ruleHit

	var walk func(e ast.Expr)
	walk = func(e ast.Expr) {
		if e == nil {
			return
		}
		if isSafeWrapperCall(e) {
			return
		}

		switch x := e.(type) {
		case *ast.Ident:
			if h, ok := checkIdent(x.Name, tainted); ok {
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

	rawBodyFields = map[string]bool{
		"payload":          true,
		"body":             true,
		"bodybytes":        true,
		"reqbody":          true,
		"respbody":         true,
		"requestbody":      true,
		"responsebody":     true,
		"prompt":           true,
		"messages":         true,
		"detectedentities": true,
		"allowedentities":  true,
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

func checkIdent(name string, tainted map[string]taintKind) (ruleHit, bool) {
	lower := strings.ToLower(name)
	if credentialIdents[lower] {
		return ruleHit{
			rule: RuleCredential,
			msg:  "logging raw credential/token variable " + name + " (use apikeys.RedactKey or apikeys.CredentialHashSuffix, or omit)",
		}, true
	}
	if rawBodyIdents[lower] {
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
	if rawBodyFields[field] {
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

// collectIgnoredLines parses `//piiloglint:ignore <reason>` comments.
// A non-empty reason is required so suppressions are always auditable.
// A directive applies to the line it appears on (inline comment) as well as
// the immediately following line (leading comment).
func collectIgnoredLines(fset *token.FileSet, groups []*ast.CommentGroup) map[int]bool {
	ignored := make(map[int]bool)
	for _, g := range groups {
		for _, c := range g.List {
			text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
			if strings.HasPrefix(text, "/*") {
				text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "/*"), "*/"))
			}
			const directive = "piiloglint:ignore"
			idx := strings.Index(text, directive)
			if idx < 0 {
				continue
			}
			reason := strings.TrimSpace(text[idx+len(directive):])
			if reason == "" {
				continue
			}
			line := lineFor(fset, c.Pos())
			ignored[line] = true
			ignored[line+1] = true
		}
	}
	return ignored
}
