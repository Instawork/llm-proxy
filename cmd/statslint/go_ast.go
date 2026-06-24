package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

type Finding struct {
	File     string
	Line     int
	Rule     string
	Message  string
	Severity string // error | warn
}

func lintGoStatsFile(path string) []Finding {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return []Finding{{
			File: path, Line: 1, Rule: "parse-error", Message: err.Error(), Severity: "error",
		}}
	}

	var out []Finding
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "Snapshot" {
			continue
		}
		if !isRecorderReceiver(fn.Recv) {
			continue
		}
		out = append(out, lintRecorderSnapshot(fset, path, fn)...)
	}
	return out
}

func isRecorderReceiver(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) != 1 {
		return false
	}
	star, ok := recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "Recorder"
}

func lintRecorderSnapshot(fset *token.FileSet, path string, fn *ast.FuncDecl) []Finding {
	body := fn.Body
	if body == nil {
		return nil
	}

	info := analyzeSnapshotBody(body)

	var out []Finding
	fnLine := lineFor(fset, fn.Pos())

	if !info.hasTodayUTC {
		out = append(out, Finding{
			File: path, Line: fnLine, Rule: "stats-snapshot-utc-today",
			Message:  "Snapshot() should define today := time.Now().UTC().Format(...)",
			Severity: "error",
		})
	}
	if !info.hasLocalActive {
		out = append(out, Finding{
			File: path, Line: fnLine, Rule: "stats-local-active",
			Message:  "Snapshot() should set localActive from bucketDay == today",
			Severity: "error",
		})
	}
	if info.snapDayUsesStaleKey {
		out = append(out, Finding{
			File: path, Line: lineFor(fset, token.Pos(info.snapDayLine)), Rule: "stats-snapshot-stale-day-key",
			Message:  `snap["day"] must use UTC today, not r.dayKey/dayKey`,
			Severity: "error",
		})
	}
	for _, pos := range info.mergeTodayStaleArgs {
		out = append(out, Finding{
			File: path, Line: lineFor(fset, token.Pos(pos)), Rule: "stats-merge-today-stale-day",
			Message:  "MergeToday(..., day, ...) must pass UTC today, not dayKey",
			Severity: "error",
		})
	}
	if info.hasMergeToday && !info.hasPostMergeRestore {
		out = append(out, Finding{
			File: path, Line: fnLine, Rule: "stats-post-merge-restore",
			Message:  "after MergeToday, restore in-process totals with if localActive { mergeLocal... }",
			Severity: "error",
		})
	}
	if info.recentOutsideLocalActive {
		out = append(out, Finding{
			File: path, Line: lineFor(fset, token.Pos(info.recentLine)), Rule: "stats-recent-without-local-active",
			Message:  "copy recent/recent_blocks/recent_events only when localActive",
			Severity: "error",
		})
	}
	if info.hasQueueToday && !info.hasQueueDelta {
		out = append(out, Finding{
			File: path, Line: lineFor(fset, token.Pos(info.queueTodayLine)), Rule: "stats-legacy-queue-today",
			Message:  "prefer QueueDelta + MergeToday for fleet rollups (QueueToday is last-write-wins)",
			Severity: "warn",
		})
	}
	if info.hasMergeHistory && !info.hasMergeHourly {
		out = append(out, Finding{
			File: path, Line: fnLine, Rule: "stats-missing-merge-hourly",
			Message:  "Snapshot() calls MergeHistory but not MergeHourly — add r.MergeHourly(...) so the Today trend chart uses Redis instead of a browser sparkline",
			Severity: "error",
		})
	}
	return out
}

type snapshotFacts struct {
	hasTodayUTC              bool
	hasLocalActive           bool
	hasMergeToday            bool
	hasPostMergeRestore      bool
	hasMergeHistory          bool
	hasMergeHourly           bool
	hasQueueToday            bool
	hasQueueDelta            bool
	queueTodayLine           int
	snapDayUsesStaleKey      bool
	snapDayLine              int
	recentOutsideLocalActive bool
	recentLine               int
	mergeTodayStaleArgs      []int
}

func analyzeSnapshotBody(body *ast.BlockStmt) snapshotFacts {
	info := snapshotFacts{}
	localDepth := 0

	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.IfStmt:
			if isLocalActiveIf(x) {
				localDepth++
				ast.Inspect(x.Body, func(inner ast.Node) bool {
					switch y := inner.(type) {
					case *ast.CallExpr:
						if isMergeLocalCall(callName(y)) {
							info.hasPostMergeRestore = true
						}
					case *ast.AssignStmt:
						noteRecentAssign(&info, y, 1)
					}
					return true
				})
				localDepth--
				return false
			}
		case *ast.AssignStmt:
			noteAssignFacts(&info, x)
			noteRecentAssign(&info, x, localDepth)
		case *ast.CallExpr:
			switch callName(x) {
			case "MergeToday":
				info.hasMergeToday = true
				if len(x.Args) >= 2 && isStaleDayIdent(x.Args[1]) {
					info.mergeTodayStaleArgs = append(info.mergeTodayStaleArgs, int(x.Args[1].Pos()))
				}
			case "MergeHistory":
				info.hasMergeHistory = true
			case "MergeHourly":
				info.hasMergeHourly = true
			case "QueueToday":
				info.hasQueueToday = true
				info.queueTodayLine = int(x.Pos())
			case "QueueDelta":
				info.hasQueueDelta = true
			}
		case *ast.CompositeLit:
			noteSnapDay(&info, x)
		}
		return true
	})
	return info
}

func noteAssignFacts(info *snapshotFacts, stmt *ast.AssignStmt) {
	for i, lhs := range stmt.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || len(stmt.Rhs) <= i {
			continue
		}
		rhs := stmt.Rhs[i]
		switch ident.Name {
		case "today":
			if isTodayUTCExpr(rhs) {
				info.hasTodayUTC = true
			}
		case "localActive":
			if isLocalActiveExpr(rhs) {
				info.hasLocalActive = true
			}
		}
	}
}

func noteRecentAssign(info *snapshotFacts, stmt *ast.AssignStmt, localDepth int) {
	if localDepth > 0 {
		return
	}
	for i, lhs := range stmt.Lhs {
		ident, ok := lhs.(*ast.Ident)
		if !ok || len(stmt.Rhs) <= i {
			continue
		}
		if ident.Name != "recent" && ident.Name != "recentBlocks" {
			continue
		}
		if referencesRecorderRecent(stmt.Rhs[i]) {
			info.recentOutsideLocalActive = true
			info.recentLine = int(stmt.Rhs[i].Pos())
		}
	}
}

func noteSnapDay(info *snapshotFacts, lit *ast.CompositeLit) {
	mapType, ok := lit.Type.(*ast.MapType)
	if !ok {
		return
	}
	key, ok := mapType.Key.(*ast.Ident)
	if !ok || key.Name != "string" {
		return
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		keyLit, ok := kv.Key.(*ast.BasicLit)
		if !ok || keyLit.Value != `"day"` {
			continue
		}
		if isStaleDayIdent(kv.Value) {
			info.snapDayUsesStaleKey = true
			info.snapDayLine = int(kv.Value.Pos())
		}
	}
}

func isMergeLocalCall(name string) bool {
	switch name {
	case "mergeLocalCostTotalsIntoSnap", "mergeLocalUsageIntoSnap", "mergeLocalPIIIntoSnap",
		"mergeLocalCircuitIntoSnap", "mergeLocalIDGateIntoSnap", "mergeLocalModelStatusIntoSnap",
		"mergeLocalByKeyIntoSnap", "mergeLocalByProviderIntoSnap", "mergeLocalRateLimitIntoSnap":
		return true
	default:
		return false
	}
}

func isLocalActiveIf(stmt *ast.IfStmt) bool {
	ident, ok := stmt.Cond.(*ast.Ident)
	return ok && ident.Name == "localActive"
}

func isTodayUTCExpr(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	formatSel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || formatSel.Sel.Name != "Format" {
		return false
	}
	utcCall, ok := formatSel.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	utcSel, ok := utcCall.Fun.(*ast.SelectorExpr)
	if !ok || utcSel.Sel.Name != "UTC" {
		return false
	}
	nowCall, ok := utcSel.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	nowSel, ok := nowCall.Fun.(*ast.SelectorExpr)
	if !ok || nowSel.Sel.Name != "Now" {
		return false
	}
	return identName(nowSel.X) == "time"
}

func isLocalActiveExpr(expr ast.Expr) bool {
	bin, ok := expr.(*ast.BinaryExpr)
	if !ok || bin.Op != token.EQL {
		return false
	}
	left, ok := bin.X.(*ast.Ident)
	right, ok2 := bin.Y.(*ast.Ident)
	if !ok || !ok2 {
		return false
	}
	names := left.Name + "/" + right.Name
	return names == "bucketDay/today" || names == "today/bucketDay"
}

func isStaleDayIdent(expr ast.Expr) bool {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name == "dayKey"
	}
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		return sel.Sel.Name == "dayKey"
	}
	return false
}

func referencesRecorderRecent(expr ast.Expr) bool {
	src := exprString(expr)
	return strings.Contains(src, "r.recent") || strings.Contains(src, "r.recentBlocks")
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

func identName(expr ast.Expr) string {
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func exprString(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		return exprString(x.Fun) + "(...)"
	default:
		return ""
	}
}
