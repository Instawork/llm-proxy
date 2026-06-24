/**
 * AST linter for admin dashboard "today" stats anti-patterns.
 * Emits pipe-delimited lines: severity|file|line|rule|message
 */
import fs from "node:fs";
import path from "node:path";
import ts from "typescript";

type Severity = "error" | "warn";

interface Finding {
  severity: Severity;
  file: string;
  line: number;
  rule: string;
  message: string;
}

const RULES = {
  SLICE_TODAY_FALLBACK: "ts-slice-today-fallback",
  PICK_TODAY_NULLISH_DUAL: "ts-pick-today-nullish-dual",
  STATS_DIRECT_TODAY_SCALAR: "ts-stats-direct-today-scalar",
  DETECTION_RATE_SNAPSHOT: "ts-detection-rate-snapshot",
  LIMIT_PROGRESS_HIDDEN: "ts-limit-progress-hidden",
  SPEND_BY_KEY_MEMORY_REDIS: "ts-spend-by-key-memory-redis",
} as const;

function emit(f: Finding) {
  const rel = f.file;
  console.log(`${f.severity}|${rel}|${f.line}|${f.rule}|${f.message}`);
}

function lineOf(sf: ts.SourceFile, node: ts.Node): number {
  return sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1;
}

function walk(webDir: string, rel: string, visit: (sf: ts.SourceFile) => void) {
  const abs = path.join(webDir, rel);
  const text = fs.readFileSync(abs, "utf8");
  const sf = ts.createSourceFile(abs, text, ts.ScriptTarget.Latest, true, rel.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
  visit(sf);
}

function isSliceRangeFunction(node: ts.Node): node is ts.FunctionDeclaration {
  return ts.isFunctionDeclaration(node) && node.name?.text === "sliceRange";
}

function lintDailyHistory(sf: ts.SourceFile) {
  const visit = (node: ts.Node) => {
    if (isSliceRangeFunction(node) && node.body) {
      ts.forEachChild(node.body, function checkTodayBranch(n) {
        if (!ts.isIfStatement(n)) return;
        const cond = n.expression.getText(sf);
        if (!cond.includes('"today"') && !cond.includes("'today'")) return;
        const then = n.thenStatement;
        if (!ts.isBlock(then)) return;
        for (const stmt of then.statements) {
          if (!ts.isReturnStatement(stmt) || !stmt.expression) continue;
          const ret = stmt.expression.getText(sf);
          if (ret.includes("slice(-1)") || ret.includes(".slice(-1)")) {
            emit({
              severity: "error",
              file: sf.fileName,
              line: lineOf(sf, stmt),
              rule: RULES.SLICE_TODAY_FALLBACK,
              message: 'sliceRange("today") must not fall back to yesterday (.slice(-1))',
            });
          }
        }
      });
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
}

function lintPickTodayNullish(sf: ts.SourceFile) {
  const visit = (node: ts.Node) => {
    if (ts.isCallExpression(node) && node.expression.getText(sf) === "pickToday") {
      const arg0 = node.arguments[0];
      if (!arg0) return;
      const text = arg0.getText(sf);
      if (text.includes("??") && !text.includes("Math.max")) {
        emit({
          severity: "warn",
          file: sf.fileName,
          line: lineOf(sf, arg0),
          rule: RULES.PICK_TODAY_NULLISH_DUAL,
          message: "pickToday memory arg uses ?? between sources; prefer Math.max before pickToday",
        });
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
}

const TODAY_SCALAR_RE =
  /stats\?\.(requests_today|tokens_today|spend_today_usd|input_spend_today_usd|output_spend_today_usd|input_tokens_today|output_tokens_today|requests_with_pii|requests_scanned|entities_total|fail_open|fail_closed|oversize|detection_rate)/;

function lintPageScalars(sf: ts.SourceFile, rel: string) {
  if (!rel.includes("/pages/") && !rel.includes("/keys/detail")) return;
  const text = sf.getFullText();
  if (!text.includes("pickToday") && !text.includes("daily_history")) return;

  const hasRedisFlag =
    text.includes("daily_history_available") || text.includes("hasRedis") || text.includes("usageRedis");

  const visit = (node: ts.Node) => {
    if (ts.isPropertyAccessChain(node) || ts.isPropertyAccessExpression(node)) {
      const expr = node.getText(sf);
      if (TODAY_SCALAR_RE.test(expr) && hasRedisFlag) {
        const parent = node.parent;
        // Allow inside pickToday(...) first argument
        if (parent && ts.isCallExpression(parent) && parent.expression.getText(sf) === "pickToday") {
          return;
        }
        // Allow ternary guard stats?.available ? stats?.field
        if (parent && ts.isConditionalExpression(parent) && parent.condition.getText(sf).includes("available")) {
          return;
        }
        // Allow ?? 0 fallbacks inside Math.max
        let p: ts.Node | undefined = node;
        while (p) {
          if (ts.isCallExpression(p) && p.expression.getText(sf) === "Math.max") return;
          p = p.parent;
        }
        emit({
          severity: "warn",
          file: sf.fileName,
          line: lineOf(sf, node),
          rule: RULES.STATS_DIRECT_TODAY_SCALAR,
          message: `direct ${expr} may undercount fleet today; use pickToday when Redis history is available`,
        });
      }
      if (expr.includes("detection_rate")) {
        emit({
          severity: "error",
          file: sf.fileName,
          line: lineOf(sf, node),
          rule: RULES.DETECTION_RATE_SNAPSHOT,
          message: "recompute detection_rate from pickToday picks, not stats.detection_rate",
        });
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
}

function lintSpendLimitProgress(sf: ts.SourceFile) {
  const visit = (node: ts.Node) => {
    if (ts.isFunctionDeclaration(node) && node.name?.text === "SpendLimitProgress" && node.body) {
      for (const stmt of node.body.statements) {
        if (!ts.isIfStatement(stmt)) continue;
        const cond = stmt.expression.getText(sf);
        if (!cond.includes("limitCents")) continue;
        if (ts.isReturnStatement(stmt.thenStatement) && stmt.thenStatement.expression?.kind === ts.SyntaxKind.NullKeyword) {
          emit({
            severity: "error",
            file: sf.fileName,
            line: lineOf(sf, stmt),
            rule: RULES.LIMIT_PROGRESS_HIDDEN,
            message: "SpendLimitProgress should show Unlimited UI instead of returning null",
          });
        }
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
}

function lintCostPageByKey(sf: ts.SourceFile, rel: string) {
  if (!rel.endsWith("/cost.tsx")) return;
  const text = sf.getFullText();
  if (!text.includes("hasRedis") || text.includes("aggCostByKey")) return;
  if (text.includes("stats?.by_key") && text.includes("limitRows")) {
    emit({
      severity: "warn",
      file: sf.fileName,
      line: 1,
      rule: RULES.SPEND_BY_KEY_MEMORY_REDIS,
      message: "limitRows/spend-vs-limit should use aggCostByKey when hasRedis",
    });
  }
}

function main() {
  const webDir = process.argv[2] ?? ".";
  const files = [
    "src/lib/daily-history.ts",
    "src/pages/overview.tsx",
    "src/pages/usage.tsx",
    "src/pages/cost.tsx",
    "src/pages/pii.tsx",
    "src/pages/circuit.tsx",
    "src/pages/model-status.tsx",
    "src/pages/keys/detail.tsx",
    "src/components/ui/spend-limit-progress.tsx",
    "src/components/ui/spend-breakdown.tsx",
  ];

  for (const rel of files) {
    const abs = path.join(webDir, rel);
    if (!fs.existsSync(abs)) continue;
    walk(webDir, rel, (sf) => {
      lintDailyHistory(sf);
      lintPickTodayNullish(sf);
      lintPageScalars(sf, rel);
      lintSpendLimitProgress(sf);
      lintCostPageByKey(sf, rel);
    });
  }
}

main();
