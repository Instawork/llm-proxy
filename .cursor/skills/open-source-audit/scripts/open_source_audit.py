#!/usr/bin/env python3
"""Scan repository files for open-source release hygiene issues."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

DEFAULT_TERMS_FILE = Path(".cursor/open-source-audit/blocked-terms.txt")
MAX_FILE_BYTES = 1_000_000
HOOK_FINDING_LIMIT = 20

SKIP_DIRS = {
    ".git",
    ".idea",
    ".venv",
    "bin",
    "dist",
    "node_modules",
    "tmp",
    "vendor",
}

SKIP_SUFFIXES = {
    ".7z",
    ".avif",
    ".bin",
    ".bmp",
    ".class",
    ".dylib",
    ".exe",
    ".gif",
    ".ico",
    ".jar",
    ".jpeg",
    ".jpg",
    ".lock",
    ".mov",
    ".mp4",
    ".pdf",
    ".png",
    ".so",
    ".sum",
    ".tar",
    ".webp",
    ".zip",
}

SECRET_RULES = [
    ("secret", re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----")),
    ("secret", re.compile(r"\b(?:AKIA|ASIA)[A-Z0-9]{16}\b")),
    ("secret", re.compile(r"\bgh[pousr]_[A-Za-z0-9_]{36,}\b")),
    ("secret", re.compile(r"\bsk-[A-Za-z0-9]{32,}\b")),
    ("secret", re.compile(r"\bxox[baprs]-[A-Za-z0-9-]{20,}\b")),
    (
        "secret",
        re.compile(
            r"(?i)\b(?:api[_-]?key|access[_-]?token|auth[_-]?token|secret|password)\b"
            r"\s*[:=]\s*['\"]?(?!YOUR_|EXAMPLE_|REPLACE_|<)[A-Za-z0-9_./+=:-]{16,}"
        ),
    ),
]

LOCAL_PATH_RULES = [
    ("local-path", re.compile(r"/Users/[A-Za-z0-9._-]+/")),
    ("local-path", re.compile(r"/home/[A-Za-z0-9._-]+/")),
    ("local-path", re.compile(r"/var/folders/[A-Za-z0-9/._-]+")),
]

PRIVATE_ENDPOINT_RULES = [
    ("private-endpoint", re.compile(r"https?://[^/\s\"']*(?:\.corp|\.internal|\.local)(?:[/:]|\b)", re.I)),
    ("private-endpoint", re.compile(r"\b(?:10|127)\.\d{1,3}\.\d{1,3}\.\d{1,3}\b")),
    ("private-endpoint", re.compile(r"\b192\.168\.\d{1,3}\.\d{1,3}\b")),
    ("private-endpoint", re.compile(r"\b172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}\b")),
]

PLACEHOLDER_ALLOWLIST = (
    "YOUR_API_KEY",
    "EXAMPLE_API_KEY",
    "REPLACE_ME",
    "example.com",
    "localhost",
    "127.0.0.1",
)


@dataclass(frozen=True)
class Finding:
    path: str
    line: int
    severity: str
    rule: str
    match: str
    message: str


def run_git(args: list[str], root: Path) -> list[str]:
    proc = subprocess.run(
        ["git", *args],
        cwd=root,
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
    )
    if proc.returncode != 0:
        return []
    return [line.strip() for line in proc.stdout.splitlines() if line.strip()]


def repo_root() -> Path:
    proc = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
    )
    if proc.returncode == 0 and proc.stdout.strip():
        return Path(proc.stdout.strip())
    return Path.cwd()


def load_terms(root: Path, term_files: Iterable[Path]) -> list[str]:
    terms: list[str] = []
    env_terms = os.environ.get("OPEN_SOURCE_AUDIT_TERMS", "")
    for term in re.split(r"[,\n]", env_terms):
        cleaned = term.strip()
        if cleaned:
            terms.append(cleaned)

    for term_file in term_files:
        path = term_file if term_file.is_absolute() else root / term_file
        if not path.exists():
            continue
        for line in path.read_text(encoding="utf-8").splitlines():
            cleaned = line.strip()
            if cleaned and not cleaned.startswith("#"):
                terms.append(cleaned)

    return sorted(set(terms), key=str.lower)


def compile_term_rules(terms: Iterable[str]) -> list[tuple[str, re.Pattern[str]]]:
    rules: list[tuple[str, re.Pattern[str]]] = []
    for term in terms:
        escaped = re.escape(term)
        if re.search(r"\W", term):
            pattern = re.compile(escaped, re.I)
        else:
            pattern = re.compile(rf"(?<![A-Za-z0-9_-]){escaped}(?![A-Za-z0-9_-])", re.I)
        rules.append(("private-reference", pattern))
    return rules


def changed_files(root: Path) -> list[Path]:
    names = set(run_git(["diff", "--name-only", "HEAD"], root))
    names.update(run_git(["diff", "--cached", "--name-only"], root))
    names.update(run_git(["ls-files", "--others", "--exclude-standard"], root))
    return [root / name for name in sorted(names)]


def tracked_files(root: Path) -> list[Path]:
    return [root / name for name in run_git(["ls-files"], root)]


def should_scan(path: Path, root: Path) -> bool:
    if not path.exists() or not path.is_file():
        return False
    try:
        relative = path.relative_to(root)
    except ValueError:
        return False
    if any(part in SKIP_DIRS for part in relative.parts):
        return False
    if path.suffix.lower() in SKIP_SUFFIXES:
        return False
    if path.stat().st_size > MAX_FILE_BYTES:
        return False
    return True


def line_is_allowed(line: str) -> bool:
    return any(token in line for token in PLACEHOLDER_ALLOWLIST)


def load_module_path(root: Path) -> str:
    go_mod = root / "go.mod"
    if not go_mod.exists():
        return ""
    for line in go_mod.read_text(encoding="utf-8").splitlines():
        stripped = line.strip()
        if stripped.startswith("module "):
            parts = stripped.split()
            if len(parts) >= 2:
                return parts[1]
    return ""


def line_uses_self_module(line: str, module_path: str) -> bool:
    """Skip blocked-term hits on canonical Go imports of this repository's module."""
    if not module_path or module_path not in line:
        return False
    return bool(
        re.search(
            rf'["\']{re.escape(module_path)}(?:/[^"\']*)?["\']',
            line,
        )
    )


def scan_file(
    path: Path,
    root: Path,
    rules: list[tuple[str, re.Pattern[str]]],
    module_path: str,
) -> list[Finding]:
    try:
        text = path.read_text(encoding="utf-8")
    except UnicodeDecodeError:
        return []

    findings: list[Finding] = []
    rel_path = str(path.relative_to(root))
    for line_number, line in enumerate(text.splitlines(), start=1):
        if line_is_allowed(line) or line_uses_self_module(line, module_path):
            continue
        for rule_name, pattern in rules:
            match = pattern.search(line)
            if not match:
                continue
            severity = "high" if rule_name == "secret" else "medium"
            findings.append(
                Finding(
                    path=rel_path,
                    line=line_number,
                    severity=severity,
                    rule=rule_name,
                    match=match.group(0)[:120],
                    message=message_for_rule(rule_name),
                )
            )
    return findings


def message_for_rule(rule_name: str) -> str:
    if rule_name == "secret":
        return "Remove the value, rotate it if real, and use documented placeholders."
    if rule_name == "local-path":
        return "Replace machine-local paths with relative paths or neutral examples."
    if rule_name == "private-endpoint":
        return "Replace private endpoints with public documentation or placeholders."
    return "Remove or replace private release terminology."


def format_text(findings: list[Finding]) -> str:
    if not findings:
        return "Open-source audit passed for the scanned files."

    lines = ["Open-source audit findings:"]
    for finding in findings:
        lines.append(
            f"- {finding.severity}: {finding.path}:{finding.line} "
            f"[{finding.rule}] {finding.message} (matched: {finding.match!r})"
        )
    return "\n".join(lines)


def _sentinel_path(root: Path) -> Path:
    """Path to the per-repo hook sentinel file."""
    return Path("/tmp") / f"oss-audit-queued-{hashlib.md5(str(root).encode()).hexdigest()[:8]}"


def _findings_hash(findings: list[Finding]) -> str:
    key = "|".join(f"{f.path}:{f.line}:{f.rule}:{f.match}" for f in findings)
    return hashlib.sha256(key.encode()).hexdigest()


def already_queued(root: Path, findings: list[Finding]) -> bool:
    """Return True if an identical finding set was already queued this session."""
    sentinel = _sentinel_path(root)
    if not sentinel.exists():
        return False
    try:
        return sentinel.read_text().strip() == _findings_hash(findings)
    except OSError:
        return False


def mark_queued(root: Path, findings: list[Finding]) -> None:
    """Record the current findings hash so subsequent hook runs skip re-queuing."""
    try:
        _sentinel_path(root).write_text(_findings_hash(findings))
    except OSError:
        pass


def clear_queued(root: Path) -> None:
    """Remove the sentinel so the next non-empty findings set is re-queued."""
    try:
        _sentinel_path(root).unlink(missing_ok=True)
    except OSError:
        pass


def hook_payload(findings: list[Finding], root: Path) -> str:
    if not findings:
        clear_queued(root)
        return json.dumps({})

    if already_queued(root, findings):
        return json.dumps({})

    shown = findings[:HOOK_FINDING_LIMIT]
    extra = len(findings) - len(shown)
    body = format_text(shown)
    if extra > 0:
        body += f"\n- ... {extra} more finding(s). Run the audit script manually for the full list."

    mark_queued(root, findings)
    return json.dumps(
        {
            "followup_message": (
                "Open-source audit found release-readiness issues in changed files. "
                "Fix them or explain why each finding is acceptable before finalizing.\n\n"
                f"{body}"
            )
        }
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Audit files for open-source release hygiene.")
    scope = parser.add_mutually_exclusive_group()
    scope.add_argument("--changed", action="store_true", help="Scan changed and untracked files.")
    scope.add_argument("--all", action="store_true", help="Scan all tracked files.")
    parser.add_argument("--terms-file", action="append", default=[], type=Path, help="Additional blocked terms file.")
    parser.add_argument("--hook", action="store_true", help="Emit Cursor stop-hook JSON.")
    parser.add_argument("--json", action="store_true", help="Emit JSON findings.")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    root = repo_root()
    term_files = [DEFAULT_TERMS_FILE, *args.terms_file]
    rules = [
        *SECRET_RULES,
        *LOCAL_PATH_RULES,
        *PRIVATE_ENDPOINT_RULES,
        *compile_term_rules(load_terms(root, term_files)),
    ]

    module_path = load_module_path(root)
    files = tracked_files(root) if args.all else changed_files(root)
    findings: list[Finding] = []
    for path in files:
        if should_scan(path, root):
            findings.extend(scan_file(path, root, rules, module_path))

    findings.sort(key=lambda item: (item.path, item.line, item.rule))

    if args.hook:
        # Drain hook stdin so Cursor can pipe event JSON without affecting the scan.
        sys.stdin.read()
        print(hook_payload(findings, root))
        return 0

    if args.json:
        print(json.dumps([finding.__dict__ for finding in findings], indent=2))
    else:
        print(format_text(findings))

    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
