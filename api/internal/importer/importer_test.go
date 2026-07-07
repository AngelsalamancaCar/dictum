package importer

import "testing"

func TestValidate_CatchesMissingAndDuplicateAndBadOutcome(t *testing.T) {
	rulings := []Ruling{
		{ExternalID: "a", Text: "texto"},
		{ExternalID: "", Text: "texto"},                         // missing id
		{ExternalID: "a", Text: "texto"},                        // duplicate of index 0
		{ExternalID: "b", Text: ""},                             // empty text
		{ExternalID: "c", Text: "texto", Outcome: "overturned"}, // invalid outcome
	}

	issues := Validate(rulings)
	if len(issues) != 4 {
		t.Fatalf("expected 4 issues, got %d: %v", len(issues), issues)
	}
}

func TestDryRun_CountsOutcomesAndUntagged(t *testing.T) {
	rulings := []Ruling{
		{ExternalID: "a", Text: "t", Outcome: OutcomeUpheld, CaseType: "despido injustificado"},
		{ExternalID: "b", Text: "t", Outcome: OutcomeReverted, CaseType: "pago de utilidades"},
		{ExternalID: "c", Text: "t"}, // no outcome, no case_type -> defaults to pending, untagged
	}

	report := DryRun(rulings)
	if report.Total != 3 {
		t.Fatalf("expected total 3, got %d", report.Total)
	}
	if report.ByOutcome[OutcomePending] != 1 {
		t.Fatalf("expected 1 pending, got %d", report.ByOutcome[OutcomePending])
	}
	if report.Untagged != 1 {
		t.Fatalf("expected 1 untagged, got %d", report.Untagged)
	}
	if len(report.ValidationIssues) != 0 {
		t.Fatalf("expected no validation issues, got %v", report.ValidationIssues)
	}
}
