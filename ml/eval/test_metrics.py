from eval.metrics import (
    classification_report,
    outcome_separation,
    precision_at_k,
    suggest_buckets,
)


def test_classification_report_counts_abstention_against_accuracy():
    pairs = [
        ("despido injustificado", "despido injustificado"),
        ("despido injustificado", "pago de utilidades"),
        ("pago de horas extra", None),  # abstained
        ("pago de horas extra", "pago de horas extra"),
    ]
    report = classification_report(pairs)
    assert report["n"] == 4
    assert report["accuracy"] == 0.5  # 2/4, abstention is a miss
    assert report["accuracy_decided"] == 2 / 3  # abstention excluded
    assert report["abstained"] == 1
    assert report["per_class"]["despido injustificado"] == {"n": 2, "correct": 1}


def test_classification_report_empty():
    report = classification_report([])
    assert report["n"] == 0
    assert report["accuracy"] is None
    assert report["accuracy_decided"] is None


def test_classification_report_all_abstained():
    report = classification_report([("x", None)])
    assert report["accuracy"] == 0.0
    assert report["accuracy_decided"] is None


def test_precision_at_k():
    retrieved = ["a", "b", "a", None, "a"]
    assert precision_at_k(retrieved, "a", 3) == 2 / 3
    assert precision_at_k(retrieved, "a", 5) == 3 / 5  # None counts as miss
    assert precision_at_k([], "a", 5) is None


def test_precision_at_k_shorter_than_k_uses_actual_length():
    assert precision_at_k(["a", "a"], "a", 10) == 1.0


def test_suggest_buckets_tertiles():
    scores = [0.0, 0.1, 0.2, 0.5, 0.6, 0.9]
    buckets = suggest_buckets(scores)
    assert buckets is not None
    t1, t2 = buckets
    assert 0.0 < t1 < t2 < 0.9


def test_suggest_buckets_too_few():
    assert suggest_buckets([0.5, 0.6]) is None


def test_outcome_separation():
    scored = [("reverted", 0.8), ("reverted", 0.6), ("upheld", 0.2)]
    sep = outcome_separation(scored)
    assert sep["reverted"] == {"n": 2, "mean_risk": 0.7}
    assert sep["upheld"] == {"n": 1, "mean_risk": 0.2}
