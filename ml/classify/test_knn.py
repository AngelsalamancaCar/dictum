from classify.knn import weighted_vote


def test_weighted_vote_picks_highest_weighted_case_type():
    neighbors = [
        {"case_type": "despido injustificado", "similarity": 0.9},
        {"case_type": "despido injustificado", "similarity": 0.8},
        {"case_type": "pago de utilidades", "similarity": 0.85},
    ]
    result = weighted_vote(neighbors)
    assert result["case_type"] == "despido injustificado"
    # (0.9 + 0.8) / (0.9 + 0.8 + 0.85)
    assert abs(result["confidence"] - (1.7 / 2.55)) < 1e-9
    assert len(result["evidence"]) == 2


def test_weighted_vote_ignores_untagged_neighbors():
    neighbors = [
        {"case_type": None, "similarity": 0.99},
        {"case_type": "pago de horas extra", "similarity": 0.5},
    ]
    result = weighted_vote(neighbors)
    assert result["case_type"] == "pago de horas extra"
    assert result["confidence"] == 1.0


def test_weighted_vote_all_untagged_returns_no_classification():
    neighbors = [{"case_type": None, "similarity": 0.9}, {"case_type": "", "similarity": 0.8}]
    result = weighted_vote(neighbors)
    assert result["case_type"] is None
    assert result["confidence"] == 0.0
    assert result["evidence"] == []


def test_weighted_vote_empty_input():
    result = weighted_vote([])
    assert result == {"case_type": None, "confidence": 0.0, "evidence": []}
