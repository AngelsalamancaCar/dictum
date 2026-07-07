"""Assembles prepared-package bundles per plan.md §5.

A bundle is an in-memory dict — not files on disk — so the caller (the Go
API server) can store it directly in the `packages.bundle` jsonb column.
`pack_bundle` reconstitutes the on-disk layout described in plan.md
(manifest.json, prompts/<use_case>.md, context/*.json, output_schema.json)
as a zip archive, for the "download packed archive for manual hand-off"
path.

The prompt file's leading HTML comment (`<!-- prompt_version: N -->`) is the
source of truth for prompt_version; callers don't pass it separately so the
manifest can't drift from the file it's built from.
"""
from __future__ import annotations

import io
import json
import re
import uuid
import zipfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

PROMPTS_DIR = Path(__file__).parent.parent / "prompts"
SCHEMAS_DIR = PROMPTS_DIR / "schemas"

_VERSION_RE = re.compile(r"<!--\s*prompt_version:\s*(\d+)\s*-->")
_PLACEHOLDER_RE = re.compile(r"\{\{(\w+)\}\}")


def _prompt_version(prompt_path: Path) -> int:
    first_line = prompt_path.read_text(encoding="utf-8").splitlines()[0]
    match = _VERSION_RE.search(first_line)
    if not match:
        raise ValueError(f"{prompt_path} missing prompt_version comment on line 1")
    return int(match.group(1))


def _render_value(value: Any) -> str:
    """Strings render verbatim; structured values render as pretty JSON."""
    if isinstance(value, str):
        return value
    return json.dumps(value, ensure_ascii=False, indent=2)


def render_prompt(use_case: str, context: dict[str, Any]) -> str:
    """Substitute `{{key}}` placeholders in the use_case's prompt template.

    Raises ValueError listing every placeholder missing from `context` —
    a package built with an unrendered `{{...}}` token is silently useless
    to the harness, so this fails loudly rather than shipping one.
    """
    prompt_path = PROMPTS_DIR / f"{use_case}.md"
    if not prompt_path.exists():
        raise ValueError(f"unknown use_case {use_case!r}: no prompt at {prompt_path}")

    template = prompt_path.read_text(encoding="utf-8")
    # Drop the leading prompt_version comment line; it's metadata, not prompt body.
    template = "\n".join(template.splitlines()[1:]).lstrip("\n")

    placeholders = set(_PLACEHOLDER_RE.findall(template))
    missing = placeholders - context.keys()
    if missing:
        raise ValueError(
            f"{use_case}: missing context for placeholder(s): {', '.join(sorted(missing))}"
        )

    return _PLACEHOLDER_RE.sub(lambda m: _render_value(context[m.group(1)]), template)


def build_bundle(
    use_case: str,
    context: dict[str, Any],
    package_id: str | None = None,
) -> dict[str, Any]:
    """Assemble a package bundle for `use_case` and return it as a dict.

    The dict is the full bundle contents (manifest fields + rendered prompt
    + raw context + output schema) — the caller is responsible for
    persisting it wherever it needs to live.
    """
    prompt_path = PROMPTS_DIR / f"{use_case}.md"
    schema_path = SCHEMAS_DIR / f"{use_case}.output_schema.json"
    if not prompt_path.exists():
        raise ValueError(f"unknown use_case {use_case!r}: no prompt at {prompt_path}")
    if not schema_path.exists():
        raise ValueError(f"unknown use_case {use_case!r}: no schema at {schema_path}")

    rendered_prompt = render_prompt(use_case, context)
    output_schema = json.loads(schema_path.read_text(encoding="utf-8"))

    return {
        "package_id": package_id or str(uuid.uuid4()),
        "use_case": use_case,
        "prompt_version": _prompt_version(prompt_path),
        "created_at": datetime.now(timezone.utc).isoformat(),
        "prompt": rendered_prompt,
        "context": context,
        "output_schema": output_schema,
    }


def pack_bundle(bundle: dict[str, Any]) -> bytes:
    """Zip a bundle dict into the on-disk layout described in plan.md §5."""
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zf:
        manifest = {
            "package_id": bundle["package_id"],
            "use_case": bundle["use_case"],
            "prompt_version": bundle["prompt_version"],
            "created_at": bundle["created_at"],
        }
        zf.writestr("manifest.json", json.dumps(manifest, ensure_ascii=False, indent=2))
        zf.writestr(f"prompts/{bundle['use_case']}.md", bundle["prompt"])
        zf.writestr(
            "output_schema.json", json.dumps(bundle["output_schema"], ensure_ascii=False, indent=2)
        )
        for name, payload in bundle["context"].items():
            zf.writestr(f"context/{name}.json", json.dumps(payload, ensure_ascii=False, indent=2))
    return buf.getvalue()
