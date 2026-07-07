"""Embedding wrapper for intfloat/multilingual-e5-large (locked in
ml/spikes/embedding_benchmark_report.md).

E5 models are trained for asymmetric retrieval: corpus-side text must be
prefixed "passage: " and query-side text "query: ". Skipping this degrades
retrieval quality, so it's enforced here rather than left to callers.
"""
from __future__ import annotations

import threading

import numpy as np
from sentence_transformers import SentenceTransformer

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


def dimension() -> int:
    return _get_model().get_embedding_dimension()
