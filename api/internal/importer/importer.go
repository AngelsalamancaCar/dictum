// Package importer loads the dictum-native NDJSON canonical ruling format
// into the store. Adapters (labelbox, folder+csv) convert to canonical
// before this package sees the data.
package importer

import "fmt"

const (
	OutcomeUpheld   = "upheld"
	OutcomeReverted = "reverted"
	OutcomePending  = "pending"
)

var validOutcomes = map[string]bool{
	OutcomeUpheld:   true,
	OutcomeReverted: true,
	OutcomePending:  true,
}

type Ruling struct {
	ExternalID   string            `json:"external_id"`
	Text         string            `json:"text"`
	CaseType     string            `json:"case_type,omitempty"`
	Outcome      string            `json:"outcome"`
	RevertReason string            `json:"revert_reason,omitempty"`
	Court        string            `json:"court,omitempty"`
	Date         string            `json:"date,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// Normalize fills in defaults (outcome defaults to "pending", matching the
// rulings.outcome CHECK constraint) so adapters don't each have to.
func (r *Ruling) Normalize() {
	if r.Outcome == "" {
		r.Outcome = OutcomePending
	}
}

// ValidationIssue is one problem found with a single ruling, keyed by its
// position in the input (adapters may not have a usable ExternalID yet).
type ValidationIssue struct {
	Index      int
	ExternalID string
	Message    string
}

func (v ValidationIssue) String() string {
	return fmt.Sprintf("[%d] %s: %s", v.Index, v.ExternalID, v.Message)
}

// Validate checks structural requirements the store's schema will otherwise
// reject, so callers get one readable report instead of a failed insert
// partway through a batch.
func Validate(rulings []Ruling) []ValidationIssue {
	var issues []ValidationIssue
	seen := make(map[string]int)

	for i, r := range rulings {
		if r.ExternalID == "" {
			issues = append(issues, ValidationIssue{i, r.ExternalID, "missing external_id"})
			continue
		}
		if prev, ok := seen[r.ExternalID]; ok {
			issues = append(issues, ValidationIssue{i, r.ExternalID, fmt.Sprintf("duplicate external_id (first seen at index %d)", prev)})
		}
		seen[r.ExternalID] = i

		if r.Text == "" {
			issues = append(issues, ValidationIssue{i, r.ExternalID, "empty text"})
		}
		if r.Outcome != "" && !validOutcomes[r.Outcome] {
			issues = append(issues, ValidationIssue{i, r.ExternalID, fmt.Sprintf("invalid outcome %q (must be upheld/reverted/pending)", r.Outcome)})
		}
	}
	return issues
}

// DryRunReport summarizes a batch before anything is written, so an operator
// can sanity-check counts and tag coverage.
type DryRunReport struct {
	Total            int
	ByOutcome        map[string]int
	ByCaseType       map[string]int
	Untagged         int // no case_type and outcome left as default "pending"
	ValidationIssues []ValidationIssue
}

func DryRun(rulings []Ruling) DryRunReport {
	report := DryRunReport{
		Total:      len(rulings),
		ByOutcome:  map[string]int{},
		ByCaseType: map[string]int{},
	}
	for _, r := range rulings {
		normalized := r
		normalized.Normalize()
		report.ByOutcome[normalized.Outcome]++
		if normalized.CaseType == "" {
			report.Untagged++
		} else {
			report.ByCaseType[normalized.CaseType]++
		}
	}
	report.ValidationIssues = Validate(rulings)
	return report
}
