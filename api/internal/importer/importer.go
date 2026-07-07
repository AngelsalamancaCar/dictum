// Package importer loads the dictum-native NDJSON canonical ruling format
// into the store. Adapters (labelbox, folder+csv) convert to canonical
// before this package sees the data.
package importer

type Ruling struct {
	ExternalID   string            `json:"external_id"`
	Text         string            `json:"text"`
	CaseType     string            `json:"case_type"`
	Outcome      string            `json:"outcome"`
	RevertReason string            `json:"revert_reason,omitempty"`
	Court        string            `json:"court,omitempty"`
	Date         string            `json:"date,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}
