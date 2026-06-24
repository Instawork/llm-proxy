/**
 * AST security linter for the admin dashboard (TypeScript).
 * Emits: severity|file|line|rule|message
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
  DANGEROUS_HTML: "sec-dangerously-set-inner-html",
  EVAL: "sec-eval",
  INNER_HTML: "sec-inner-html",
  DOCUMENT_WRITE: "sec-document-write",
  HARDCODED_SECRET: "sec-hardcoded-credential",
  TARGET_BLANK: "sec-target-blank-noopener",
  LOCALSTORAGE_SECRET: "sec-localstorage-secret",
  HTTP_URL: "sec-insecure-http-url",
} as const;

const SECRET_LIT =
  /\b(sk-(?:ant|iw)-[A-Za-z0-9_-]{10,}|AIza[0-9A-Za-z_-]{20,}|AKIA[0-9A-Z]{16})\b/;
const ASSIGN_SECRET =
  /(api[_-]?key|secret|password|token|bearer)\s*[:=]\s*['"][^'"]{8,}['"]/i;

function emit(f: Finding) {
  console.log(`${f.severity}|${f.file}|${f.line}|${f.rule}|${f.message}`);
}

function lineOf(sf: ts.SourceFile, node: ts.Node): number {
  return sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1;
}

function walk(webDir: string, rel: string, visit: (sf: ts.SourceFile) => void) {
  const abs = path.join(webDir, rel);
  const text = fs.readFileSync(abs, "utf8");
  const sf = ts.createSourceFile(
    abs,
    text,
    ts.ScriptTarget.Latest,
    true,
    rel.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
  );
  visit(sf);
}

function isTestFile(rel: string): boolean {
  return rel.includes(".test.") || rel.includes("__tests__");
}

function lintCall(sf: ts.SourceFile, rel: string, node: ts.CallExpression) {
  const text = node.expression.getText(sf);
  const line = lineOf(sf, node);
  const sev: Severity = isTestFile(rel) ? "warn" : "error";

  if (text === "eval") {
    emit({ severity: sev, file: sf.fileName, line, rule: RULES.EVAL, message: "eval() executes arbitrary code" });
  }
  if (text === "document.write") {
    emit({
      severity: sev,
      file: sf.fileName,
      line,
      rule: RULES.DOCUMENT_WRITE,
      message: "document.write can enable DOM XSS",
    });
  }
  if (text.endsWith(".setItem") && node.arguments.length >= 2) {
    const key = node.arguments[0].getText(sf).toLowerCase();
    if (/token|secret|password|apikey|api_key|session/.test(key)) {
      emit({
        severity: "warn",
        file: sf.fileName,
        line,
        rule: RULES.LOCALSTORAGE_SECRET,
        message: "storing secrets in localStorage is readable by any script on the origin",
      });
    }
  }
}

function lintPropertyAccess(sf: ts.SourceFile, rel: string, node: ts.PropertyAccessExpression) {
  const name = node.name.text;
  const line = lineOf(sf, node);
  const sev: Severity = isTestFile(rel) ? "warn" : "error";

  if (name === "innerHTML" && ts.isBinaryExpression(node.parent)) {
    emit({
      severity: sev,
      file: sf.fileName,
      line,
      rule: RULES.INNER_HTML,
      message: "assigning innerHTML can enable DOM XSS",
    });
  }
}

function lintJsx(sf: ts.SourceFile, rel: string, node: ts.JsxAttribute) {
  const name = node.name.getText(sf);
  const line = lineOf(sf, node);
  const sev: Severity = isTestFile(rel) ? "warn" : "error";

  if (name === "dangerouslySetInnerHTML") {
    emit({
      severity: sev,
      file: sf.fileName,
      line,
      rule: RULES.DANGEROUS_HTML,
      message: "dangerouslySetInnerHTML bypasses React XSS protections",
    });
  }
  if (name === "target" && node.initializer) {
    const val = node.initializer.getText(sf);
    if (val.includes("_blank")) {
      const el = node.parent?.parent;
      if (el && ts.isJsxOpeningElement(el)) {
        const hasNoopener = el.attributes.properties.some((a) => {
          if (!ts.isJsxAttribute(a)) return false;
          const n = a.name.getText(sf);
          if (n !== "rel") return false;
          return a.initializer?.getText(sf).includes("noopener") ||
            a.initializer?.getText(sf).includes("noreferrer") ||
            false;
        });
        if (!hasNoopener) {
          emit({
            severity: "warn",
            file: sf.fileName,
            line,
            rule: RULES.TARGET_BLANK,
            message: 'target="_blank" links should include rel="noopener noreferrer"',
          });
        }
      }
    }
  }
}

function isBenignKeyLiteral(raw: string): boolean {
  const lower = raw.toLowerCase();
  return ["fake", "placeholder", "your_", "your-", "xxx", "example", "here"].some((f) =>
    lower.includes(f),
  );
}

function lintStringLiteral(sf: ts.SourceFile, rel: string, node: ts.StringLiteral | ts.NoSubstitutionTemplateLiteral) {
  const raw = node.text;
  const line = lineOf(sf, node);
  const sev: Severity = isTestFile(rel) ? "warn" : "error";

  if (SECRET_LIT.test(raw) && !isBenignKeyLiteral(raw)) {
    emit({
      severity: sev,
      file: sf.fileName,
      line,
      rule: RULES.HARDCODED_SECRET,
      message: "possible live API key or cloud credential in source",
    });
  }
  if (ASSIGN_SECRET.test(raw)) {
    emit({
      severity: sev,
      file: sf.fileName,
      line,
      rule: RULES.HARDCODED_SECRET,
      message: "possible hardcoded credential assignment",
    });
  }
  if (/^http:\/\//.test(raw) && !raw.includes("localhost") && !raw.includes("127.0.0.1") && !raw.includes("w3.org")) {
    emit({
      severity: "warn",
      file: sf.fileName,
      line,
      rule: RULES.HTTP_URL,
      message: "non-local http:// URL may transmit secrets in cleartext",
    });
  }
}

function lintNewExpression(sf: ts.SourceFile, rel: string, node: ts.NewExpression) {
  const text = node.expression.getText(sf);
  if (text !== "Function") return;
  emit({
    severity: isTestFile(rel) ? "warn" : "error",
    file: sf.fileName,
    line: lineOf(sf, node),
    rule: RULES.EVAL,
    message: "new Function() is equivalent to eval",
  });
}

function lintSource(sf: ts.SourceFile, rel: string) {
  const visit = (node: ts.Node) => {
    if (ts.isCallExpression(node)) lintCall(sf, rel, node);
    if (ts.isPropertyAccessExpression(node)) lintPropertyAccess(sf, rel, node);
    if (ts.isJsxAttribute(node)) lintJsx(sf, rel, node);
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
      lintStringLiteral(sf, rel, node);
    }
    if (ts.isNewExpression(node)) lintNewExpression(sf, rel, node);
    ts.forEachChild(node, visit);
  };
  visit(sf);
}

function main() {
  const webDir = process.argv[2] ?? ".";
  const files: string[] = [];

  function scanDir(rel: string) {
    const abs = path.join(webDir, rel);
    for (const ent of fs.readdirSync(abs, { withFileTypes: true })) {
      const child = path.join(rel, ent.name);
      if (ent.isDirectory()) {
        if (ent.name === "node_modules" || ent.name === "dist" || ent.name.startsWith(".")) continue;
        scanDir(child);
        continue;
      }
      if (/\.(ts|tsx)$/.test(ent.name) && !ent.name.endsWith(".d.ts")) {
        files.push(child);
      }
    }
  }

  scanDir("src");

  for (const rel of files) {
    walk(webDir, rel, (sf) => lintSource(sf, rel));
  }
}

main();
