from rag.retrieval import reciprocal_rank_fusion


def test_rrf_favors_docs_ranked_high_in_both_lists():
    vector_ranked = ["a", "b", "c"]
    fulltext_ranked = ["b", "a", "d"]

    fused = reciprocal_rank_fusion([vector_ranked, fulltext_ranked], k=60)
    fused_ids = [doc_id for doc_id, _ in fused]

    # 'a' and 'b' each appear in both lists near the top; 'c' and 'd' each
    # appear in only one list, so 'a'/'b' should outrank 'c'/'d'.
    assert set(fused_ids[:2]) == {"a", "b"}
    assert set(fused_ids[2:]) == {"c", "d"}


def test_rrf_single_list_preserves_order():
    fused = reciprocal_rank_fusion([["x", "y", "z"]], k=60)
    assert [doc_id for doc_id, _ in fused] == ["x", "y", "z"]


def test_rrf_empty_input():
    assert reciprocal_rank_fusion([]) == []
    assert reciprocal_rank_fusion([[], []]) == []


def test_rrf_score_formula():
    # doc appears at rank 1 in one list only: score = 1/(60+1)
    fused = dict(reciprocal_rank_fusion([["only"]], k=60))
    assert abs(fused["only"] - 1 / 61) < 1e-9
