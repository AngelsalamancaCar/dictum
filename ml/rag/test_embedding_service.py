import numpy as np

from rag.embedding_service import embed_query_pooled


def test_embed_query_pooled_short_text_is_single_chunk():
    calls = []

    def embed_fn(texts: list[str]) -> np.ndarray:
        calls.append(texts)
        return np.array([[1.0, 2.0, 3.0]])

    result = embed_query_pooled("hola", embed_fn=embed_fn)

    assert len(calls[0]) == 1
    assert np.allclose(result, [1.0, 2.0, 3.0])


def test_embed_query_pooled_averages_multiple_chunks():
    # 512 "uno" + 100 "dos" words exceeds chunk_document's 512-word chunk
    # size, so it's guaranteed to split into 2 chunks (with a 64-word
    # overlap) rather than truncating into one.
    text = ("uno " * 512) + ("dos " * 100)

    def embed_fn(texts: list[str]) -> np.ndarray:
        assert len(texts) == 2
        return np.array([[2.0, 0.0], [0.0, 4.0]])

    result = embed_query_pooled(text, embed_fn=embed_fn)

    assert np.allclose(result, [1.0, 2.0])


def test_embed_query_pooled_empty_text_still_calls_embed_fn():
    calls = []

    def embed_fn(texts: list[str]) -> np.ndarray:
        calls.append(texts)
        return np.array([[0.0, 0.0]])

    embed_query_pooled("", embed_fn=embed_fn)

    # chunk_document("") yields no chunks; falls back to embedding the raw
    # (empty) text rather than erroring, matching embed_queries([""])'s
    # existing behavior for blank input.
    assert calls[0] == [""]
