from risk.score import _filter_reverted, compute_risk, risk_bucket


def test_risk_bucket_thresholds():
    assert risk_bucket(0.0) == "low"
    assert risk_bucket(0.32) == "low"
    assert risk_bucket(0.34) == "medium"
    assert risk_bucket(0.65) == "medium"
    assert risk_bucket(0.67) == "high"
    assert risk_bucket(1.0) == "high"


def test_compute_risk_weights_by_similarity():
    neighbors = [
        {"outcome": "reverted", "similarity": 0.9},
        {"outcome": "upheld", "similarity": 0.9},
        {"outcome": "reverted", "similarity": 0.1},
    ]
    result = compute_risk(neighbors)
    # (0.9*1 + 0.9*0 + 0.1*1) / (0.9 + 0.9 + 0.1) = 1.0 / 1.9
    assert abs(result["risk"] - (1.0 / 1.9)) < 1e-9
    assert result["sample_size"] == 3


def test_compute_risk_all_upheld_is_zero_low_risk():
    neighbors = [{"outcome": "upheld", "similarity": 0.8}, {"outcome": "upheld", "similarity": 0.5}]
    result = compute_risk(neighbors)
    assert result["risk"] == 0.0
    assert result["bucket"] == "low"


def test_compute_risk_all_reverted_is_max_high_risk():
    neighbors = [{"outcome": "reverted", "similarity": 0.8}, {"outcome": "reverted", "similarity": 0.5}]
    result = compute_risk(neighbors)
    assert result["risk"] == 1.0
    assert result["bucket"] == "high"


def test_compute_risk_ignores_pending_neighbors():
    neighbors = [
        {"outcome": "pending", "similarity": 0.99},
        {"outcome": "reverted", "similarity": 0.5},
    ]
    result = compute_risk(neighbors)
    assert result["risk"] == 1.0
    assert result["sample_size"] == 1


def test_compute_risk_no_tagged_neighbors_returns_none():
    neighbors = [{"outcome": "pending", "similarity": 0.9}]
    result = compute_risk(neighbors)
    assert result["risk"] is None
    assert result["bucket"] is None
    assert result["sample_size"] == 0
    assert result["caveat"] is not None


def test_compute_risk_thin_sample_carries_caveat():
    neighbors = [{"outcome": "reverted", "similarity": 0.9}]
    result = compute_risk(neighbors)
    assert result["caveat"] is not None


def test_compute_risk_empty_input():
    result = compute_risk([])
    assert result["risk"] is None
    assert result["sample_size"] == 0


def test_filter_reverted_keeps_only_reverted_outcome():
    neighbors = [
        {"outcome": "reverted", "similarity": 0.9},
        {"outcome": "upheld", "similarity": 0.85},
        {"outcome": "pending", "similarity": 0.8},
        {"outcome": "reverted", "similarity": 0.7},
    ]
    result = _filter_reverted(neighbors, k=5)
    assert len(result) == 2
    assert all(n["outcome"] == "reverted" for n in result)


def test_filter_reverted_respects_k():
    neighbors = [{"outcome": "reverted", "similarity": s} for s in (0.9, 0.8, 0.7)]
    result = _filter_reverted(neighbors, k=2)
    assert len(result) == 2
    assert [n["similarity"] for n in result] == [0.9, 0.8]


def test_filter_reverted_empty_when_none_reverted():
    neighbors = [{"outcome": "upheld", "similarity": 0.9}, {"outcome": "pending", "similarity": 0.5}]
    assert _filter_reverted(neighbors, k=5) == []
