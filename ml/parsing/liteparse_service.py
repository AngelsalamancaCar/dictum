"""Wraps LiteParse (validated in ml/spikes/liteparse_spike.md) into plain
dict output suitable for storing as the `documents.object_ref` payload
(plan.md §3: "LiteParse output (text + bounding boxes) in object storage").
"""
from __future__ import annotations

from pathlib import Path
from typing import Any

from liteparse import LiteParse

_parser = LiteParse(output_format="json", emit_word_boxes=True, quiet=True)


def parse_document(path: str | Path) -> dict[str, Any]:
    """Parse a file into {"text": full_text, "pages": [...]}.

    Plain .txt files are read directly rather than passed to LiteParse,
    which expects document formats (PDF, Office, images).
    """
    path = Path(path)
    if path.suffix.lower() == ".txt":
        text = path.read_text(encoding="utf-8", errors="replace")
        return {"text": text, "pages": [{"page_num": 1, "text": text, "text_items": []}]}

    result = _parser.parse(path)
    pages = [
        {
            "page_num": page.page_num,
            "text": page.text,
            "text_items": [
                {
                    "text": item.text,
                    "x": item.x,
                    "y": item.y,
                    "width": item.width,
                    "height": item.height,
                    "font_name": item.font_name,
                    "font_size": item.font_size,
                    "confidence": item.confidence,
                    "words": [
                        {"text": w.text, "x": w.x, "y": w.y, "width": w.width, "height": w.height}
                        for w in item.words
                    ],
                }
                for item in page.text_items
            ],
        }
        for page in result.pages
    ]
    return {"text": result.text, "pages": pages}
