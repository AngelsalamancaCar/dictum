// Package packages manages the prepared-package lifecycle (draft -> ready ->
// submitted -> completed/failed/cancelled) described in plan.md §5.
package packages

type Status string

const (
	StatusDraft     Status = "draft"
	StatusReady     Status = "ready"
	StatusSubmitted Status = "submitted"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type UseCase string

const (
	UseCaseClassify       UseCase = "classify"
	UseCaseDraft          UseCase = "draft"
	UseCaseRiskExplain    UseCase = "risk_explain"
	UseCaseSimilarExplain UseCase = "similar_explain"
)
