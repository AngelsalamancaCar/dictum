"""Section-aware chunking for parsed documents.

Splits on the parser's section/heading boundaries where available, then
sub-splits any section still over the target size by words (~512 tokens,
approximated 1:1 with words, with overlap). Word-based sizing is an
approximation of tokens, consistent with the sizing used in
ml/spikes/embedding_benchmark.py.
"""
from __future__ import annotations

import re
from dataclasses import dataclass

CHUNK_WORDS = 512
CHUNK_OVERLAP = 64

_HEADING_RE = re.compile(
    r"^\s*(R\s*E\s*S\s*U\s*L\s*T\s*A\s*N\s*D\s*O|C\s*O\s*N\s*S\s*I\s*D\s*E\s*R\s*A\s*N\s*D\s*O|"
    r"R\s*E\s*S\s*U\s*E\s*L\s*V\s*E|[IVXLCDM]+\.\s)",
    re.MULTILINE,
)


@dataclass
class Chunk:
    text: str
    section_label: str | None


def _split_into_sections(text: str) -> list[tuple[str | None, str]]:
    """Split on recognized sentencia section headings (RESULTANDO,
    CONSIDERANDO, RESUELVE, roman-numeral items). Falls back to one section
    covering the whole text if no headings are found."""
    matches = list(_HEADING_RE.finditer(text))
    if not matches:
        return [(None, text)]

    sections = []
    for i, m in enumerate(matches):
        start = m.start()
        end = matches[i + 1].start() if i + 1 < len(matches) else len(text)
        label = re.sub(r"\s+", " ", m.group(1)).strip()
        sections.append((label, text[start:end]))
    if matches[0].start() > 0:
        sections.insert(0, (None, text[: matches[0].start()]))
    return sections


def _chunk_words(text: str) -> list[str]:
    words = re.split(r"\s+", text.strip())
    if not words or words == [""]:
        return []
    step = CHUNK_WORDS - CHUNK_OVERLAP
    pieces = []
    for start in range(0, len(words), step):
        piece = words[start : start + CHUNK_WORDS]
        if piece:
            pieces.append(" ".join(piece))
        if start + CHUNK_WORDS >= len(words):
            break
    return pieces


def chunk_document(text: str) -> list[Chunk]:
    chunks: list[Chunk] = []
    for label, section_text in _split_into_sections(text):
        for piece in _chunk_words(section_text):
            chunks.append(Chunk(text=piece, section_label=label))
    return chunks
