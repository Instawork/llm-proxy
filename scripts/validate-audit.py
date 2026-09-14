#!/usr/bin/env python3
"""Validate an audits/audit-MM-DD-YYYY.json file against audits/audit.schema.json
plus the cross-field rules the schema cannot express.

Usage: python3 scripts/validate-audit.py audits/audit-09-14-2026.json
"""

import json
import re
import sys
from datetime import date
from pathlib import Path

try:
    from jsonschema import Draft202012Validator
except ImportError:
    sys.exit("jsonschema is not installed: pip install -r requirements.txt")

REPO = Path(__file__).resolve().parent.parent
SCHEMA = REPO / "audits" / "audit.schema.json"
FILENAME = re.compile(r"^audit-(\d{2})-(\d{2})-(\d{4})\.json$")


def semantic_errors(path: Path, audit: dict) -> list[str]:
    errors: list[str] = []
    generated_at = date.fromisoformat(audit["generated_at"])

    m = FILENAME.match(path.name)
    if not m:
        errors.append(f"{path.name}: filename must be audit-MM-DD-YYYY.json")
    elif date(int(m[3]), int(m[1]), int(m[2])) != generated_at:
        errors.append(f"{path.name}: filename date does not match generated_at {generated_at}")

    kinds_by_provider: dict[str, set[str]] = {}
    for src in audit["sources"]:
        kinds_by_provider.setdefault(src["provider"], set()).add(src["kind"])
    for provider, section in audit["providers"].items():
        missing = {"pricing", "deprecations"} - kinds_by_provider.get(provider, set())
        if missing:
            errors.append(f"{provider}: no source of kind {sorted(missing)}")

        known: dict[str, str] = {}
        for model in section["models"]:
            for name in [model["id"], *model["aliases"]]:
                if name in known:
                    errors.append(f"{provider}/{model['id']}: '{name}' already used by {known[name]}")
                known[name] = model["id"]

        for model in section["models"]:
            where = f"{provider}/{model['id']}"
            for field in ("replaced_by", "ga_equivalent"):
                target = model[field]
                if target is not None and target not in known:
                    errors.append(f"{where}: {field} '{target}' is not a model or alias in {provider}")

            tiers = model["pricing"]
            ceilings = [t["up_to_tokens"] for t in tiers]
            if ceilings[-1] is not None or any(c is None for c in ceilings[:-1]):
                errors.append(f"{where}: only the last pricing tier may have up_to_tokens null")
            elif ceilings[:-1] != sorted(ceilings[:-1]):
                errors.append(f"{where}: pricing tiers must be ordered by ascending up_to_tokens")

            sunset = model["sunset_date"]
            if sunset and date.fromisoformat(sunset) <= generated_at and model["status"] != "shutdown":
                errors.append(f"{where}: sunset_date {sunset} has passed but status is {model['status']}")
            if model["ga_equivalent"] is not None and model["status"] not in ("preview", "deprecated", "shutdown"):
                errors.append(f"{where}: ga_equivalent set on a non-preview model")
    return errors


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        sys.exit(__doc__)
    path = Path(argv[1])
    schema = json.loads(SCHEMA.read_text())
    audit = json.loads(path.read_text())

    Draft202012Validator.check_schema(schema)
    validator = Draft202012Validator(schema, format_checker=Draft202012Validator.FORMAT_CHECKER)
    errors = [
        f"{'/'.join(str(p) for p in e.absolute_path) or '<root>'}: {e.message}" for e in validator.iter_errors(audit)
    ]
    if not errors:
        errors = semantic_errors(path, audit)
    for err in errors:
        print(err, file=sys.stderr)
    if errors:
        return 1
    total = sum(len(p["models"]) for p in audit["providers"].values())
    print(f"{path.name}: ok ({total} models, generated {audit['generated_at']})")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
