"""Assembles prepared-package bundles per plan.md §5.

A bundle is a directory:
    manifest.json
    prompts/<use_case>.md
    context/*.json
    output_schema.json

The prompt file's leading HTML comment (`<!-- prompt_version: N -->`) is the
source of truth for prompt_version; callers don't pass it separately so the
manifest can't drift from the file it's built from.
"""
from __future__ import annotations

import json
import re
import shutil
import uuid
from datetime import datetime, timezone
from pathlib import Path

PROMPTS_DIR = Path(__file__).parent.parent / "prompts"
SCHEMAS_DIR = PROMPTS_DIR / "schemas"

_VERSION_RE = re.compile(r"<!--\s*prompt_version:\s*(\d+)\s*-->")


def _prompt_version(prompt_path: Path) -> int:
    first_line = prompt_path.read_text(encoding="utf-8").splitlines()[0]
    match = _VERSION_RE.search(first_line)
    if not match:
        raise ValueError(f"{prompt_path} missing prompt_version comment on line 1")
    return int(match.group(1))


def build_bundle(
    use_case: str,
    context: dict[str, object],
    out_dir: Path,
    package_id: str | None = None,
) -> Path:
    """Write a package bundle for `use_case` into `out_dir` and return its path."""
    prompt_path = PROMPTS_DIR / f"{use_case}.md"
    schema_path = SCHEMAS_DIR / f"{use_case}.output_schema.json"
    if not prompt_path.exists():
        raise ValueError(f"unknown use_case {use_case!r}: no prompt at {prompt_path}")

    package_id = package_id or str(uuid.uuid4())
    bundle_dir = out_dir / package_id
    (bundle_dir / "prompts").mkdir(parents=True, exist_ok=True)
    (bundle_dir / "context").mkdir(parents=True, exist_ok=True)

    shutil.copyfile(prompt_path, bundle_dir / "prompts" / prompt_path.name)
    shutil.copyfile(schema_path, bundle_dir / "output_schema.json")

    for name, payload in context.items():
        (bundle_dir / "context" / f"{name}.json").write_text(
            json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8"
        )

    manifest = {
        "package_id": package_id,
        "use_case": use_case,
        "prompt_version": _prompt_version(prompt_path),
        "created_at": datetime.now(timezone.utc).isoformat(),
    }
    (bundle_dir / "manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8"
    )

    return bundle_dir
