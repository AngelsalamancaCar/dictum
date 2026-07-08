"""Embedding wrapper for intfloat/multilingual-e5-large (locked in
ml/spikes/embedding_benchmark_report.md).

E5 models are trained for asymmetric retrieval: corpus-side text must be
prefixed "passage: " and query-side text "query: ". Skipping this degrades
retrieval quality, so it's enforced here rather than left to callers.
"""
from __future__ import annotations

import threading
from typing import Callable

import numpy as np
from sentence_transformers import SentenceTransformer

from rag.chunking import chunk_document

MODEL_NAME = "intfloat/multilingual-e5-large"

_model: SentenceTransformer | None = None
_model_lock = threading.Lock()


def _get_model() -> SentenceTransformer:
    global _model
    if _model is None:
        with _model_lock:
            if _model is None:
                _model = SentenceTransformer(MODEL_NAME)
    return _model


def embed_passages(texts: list[str]) -> np.ndarray:
    """Embed corpus-side chunks (documents, rulings) for indexing."""
    prefixed = [f"passage: {t}" for t in texts]
    return _get_model().encode(prefixed, convert_to_numpy=True)


def embed_queries(texts: list[str]) -> np.ndarray:
    """Embed query-side text (case summaries, drafts) for retrieval."""
    prefixed = [f"query: {t}" for t in texts]
    return _get_model().encode(prefixed, convert_to_numpy=True)


def embed_query_pooled(
    text: str, embed_fn: Callable[[list[str]], np.ndarray] = embed_queries
) -> np.ndarray:
    """Embed query-side text of arbitrary length as a single vector.

    A plain `embed_queries([text])[0]` silently truncates at the model's max
    sequence length for anything longer than ~512 words — fine for a short
    case summary, wrong for a full case/draft text. Gives query text the
    same chunk + mean-pool treatment `importer.EmbedRuling` (Go) already
    gives corpus rulings, so long text degrades gracefully instead of
    losing everything past the first chunk.
    """
    chunks = chunk_document(text)
    texts = [c.text for c in chunks] if chunks else [text]
    vectors = embed_fn(texts)
    return vectors.mean(axis=0)


def dimension() -> int:
    return _get_model().get_embedding_dimension()
