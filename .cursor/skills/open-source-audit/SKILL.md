---
name: open-source-audit
description: Audit repository changes for open-source readiness by scanning for private references, secret-like values, machine-local paths, and release-blocking metadata. Use when preparing a project for public release, reviewing OSS hygiene, or when the user says "open-source audit", "OSS audit", or "public release audit".
---

# Open Source Audit

Use this skill before publishing or updating a public repository. Keep the audit generic and vendor-neutral: do not bake organization names, private project names, customer names, employee groups, or internal environment details into the skill or script.

## Quick Start

Run the audit from the repository root:

```bash
.cursor/skills/open-source-audit/scripts/open_source_audit.py --changed
```

For a full repository scan:

```bash
.cursor/skills/open-source-audit/scripts/open_source_audit.py --all
```

The script reads blocked terms from:

1. `.cursor/open-source-audit/blocked-terms.txt`
2. `OPEN_SOURCE_AUDIT_TERMS` as a comma- or newline-separated list
3. `--terms-file <path>` for an explicit file

Keep real blocked terms in local, ignored config. Commit only placeholder examples.

## Workflow

Use the script for exact matches, but do not rely on it alone. When the user asks for an open-source audit, run the script and launch readonly subagents to catch semantic leaks the script cannot see.

1. Determine scope:

   ```bash
   git status --short
   git diff --name-only HEAD
   git diff --cached --name-only
   git ls-files --others --exclude-standard
   ```

   Default to changed and untracked files. Use `--all` only when the user asks for a full repository audit.

2. Run the deterministic scanner:

   ```bash
   .cursor/skills/open-source-audit/scripts/open_source_audit.py --changed
   ```

3. In a single message, launch relevant readonly subagents in parallel. Give each subagent the file list, the audit checklist below, and instructions to return only release-blocking findings as JSON objects with `{path, line, severity, category, evidence, remediation}`.

   - Documentation and examples: README files, docs, examples, sample requests, screenshots, package metadata, license text.
   - Code and configuration: source comments, log messages, environment variable names, endpoint names, import paths, build files, CI config, container config.
   - Secrets and local artifacts: credential-like strings, tokens, private keys, local machine paths, generated logs, scratch files, cache files, editor metadata.

4. Aggregate results:

   - Combine script findings and subagent findings.
   - De-duplicate by path, line, category, and evidence.
   - Sort by severity: `high`, `medium`, `low`.
   - Fix clear issues directly when in agent mode; otherwise report them with concrete remediation.

5. After fixes, rerun:

   ```bash
   .cursor/skills/open-source-audit/scripts/open_source_audit.py --changed
   ```

   If subagents found semantic issues that the script cannot detect, mention that they were reviewed manually after the script pass.

## Audit Checklist

Treat any finding as release-blocking until reviewed:

- Private references: organization-specific product names, team names, customer names, people groups, code names, private domains, private repository paths.
- Secrets: API tokens, private keys, cloud credentials, webhook URLs, bearer tokens, pasted `.env` values.
- Local machine data: home-directory paths, editor cache paths, local scratch files, machine-specific absolute paths.
- Public metadata: README, license, examples, package metadata, container files, and CI config should describe only public behavior.
- Examples: sample commands should use placeholders like `YOUR_API_KEY`, `example.com`, and neutral IDs.

## Hook Behavior

This repository wires the audit as a `stop` hook. When changed files contain findings, the hook returns a follow-up message instructing the agent to fix or explicitly justify them before finalizing.

The hook is advisory and fail-open: it runs the deterministic script only. If the hook flags findings, or if the user explicitly asks for an audit, use the full workflow above with subagents before finalizing.

## Output Format

Report findings grouped by file with severity, line number, rule, and a short remediation. If no findings are found, say:

```text
Open-source audit passed for the scanned files.
```
