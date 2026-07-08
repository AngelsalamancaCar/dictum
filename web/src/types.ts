// Wire types for the Go API. Package/PackageSummary/PackageResult mirror
// api/internal/store's structs directly (no json tags there, so the wire
// format is PascalCase — see CLAUDE.md). PackageBundle mirrors
// api/internal/mlclient.PackageBundle, which DOES carry snake_case json
// tags, so its fields are lowercase even though they live inside a
// PascalCase Package.Bundle field.

export interface PackageBundle {
  package_id: string
  use_case: string
  prompt_version: number
  created_at: string
  prompt: string
  context: Record<string, unknown>
  output_schema: Record<string, unknown>
}

export interface Package {
  ID: string
  CaseID: string
  UseCase: string
  PromptVersion: number
  Status: PackageStatus
  Bundle: PackageBundle
  CreatedBy: string | null
  SubmittedAt: string | null
  CompletedAt: string | null
  Error: string | null
  RetryOf: string | null
  CreatedAt: string
}

export interface PackageSummary {
  ID: string
  CaseID: string
  UseCase: string
  PromptVersion: number
  Status: PackageStatus
  CreatedBy: string | null
  SubmittedAt: string | null
  CompletedAt: string | null
  Error: string | null
  RetryOf: string | null
  CreatedAt: string
}

export type PackageStatus = 'draft' | 'ready' | 'submitted' | 'completed' | 'failed' | 'cancelled'

export interface PackageResult {
  ID: string
  PackageID: string
  RawResponse: unknown
  ValidatedPayload: unknown | null
  ValidationStatus: 'pending' | 'valid' | 'invalid'
  ReceivedAt: string
}

export interface AttachResultResponse {
  Result: PackageResult
  ValidationErrors: string[] | null
}
