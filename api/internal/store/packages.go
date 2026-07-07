package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Package is a row from the packages table (plan.md §5's prepared-package
// lifecycle: draft -> ready -> submitted -> completed/failed/cancelled).
type Package struct {
	ID            uuid.UUID
	CaseID        uuid.UUID
	UseCase       string
	PromptVersion int
	Status        string
	Bundle        json.RawMessage
	CreatedBy     *string
	SubmittedAt   *time.Time
	CompletedAt   *time.Time
	Error         *string
	RetryOf       *uuid.UUID
	CreatedAt     time.Time
}

// PackageSummary is Package without the bundle payload, for list views where
// the (potentially large) prompt/context/schema content isn't needed.
type PackageSummary struct {
	ID            uuid.UUID
	CaseID        uuid.UUID
	UseCase       string
	PromptVersion int
	Status        string
	CreatedBy     *string
	SubmittedAt   *time.Time
	CompletedAt   *time.Time
	Error         *string
	RetryOf       *uuid.UUID
	CreatedAt     time.Time
}

type PackageInput struct {
	CaseID        uuid.UUID
	UseCase       string
	PromptVersion int
	Status        string
	Bundle        json.RawMessage
	CreatedBy     string
	RetryOf       *uuid.UUID
}

func (s *Store) CreatePackage(ctx context.Context, in PackageInput) (Package, error) {
	var p Package
	err := s.pool.QueryRow(ctx, `
		INSERT INTO packages (case_id, use_case, prompt_version, status, bundle, created_by, retry_of)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7)
		RETURNING id, case_id, use_case, prompt_version, status, bundle, created_by, submitted_at, completed_at, error, retry_of, created_at
	`, in.CaseID, in.UseCase, in.PromptVersion, in.Status, []byte(in.Bundle), in.CreatedBy, in.RetryOf,
	).Scan(&p.ID, &p.CaseID, &p.UseCase, &p.PromptVersion, &p.Status, &p.Bundle, &p.CreatedBy, &p.SubmittedAt, &p.CompletedAt, &p.Error, &p.RetryOf, &p.CreatedAt)
	return p, err
}

func (s *Store) GetPackage(ctx context.Context, id uuid.UUID) (Package, error) {
	var p Package
	err := s.pool.QueryRow(ctx, `
		SELECT id, case_id, use_case, prompt_version, status, bundle, created_by, submitted_at, completed_at, error, retry_of, created_at
		FROM packages WHERE id = $1
	`, id).Scan(&p.ID, &p.CaseID, &p.UseCase, &p.PromptVersion, &p.Status, &p.Bundle, &p.CreatedBy, &p.SubmittedAt, &p.CompletedAt, &p.Error, &p.RetryOf, &p.CreatedAt)
	return p, err
}

// PackageFilter narrows ListPackages; zero values (empty string / nil) mean
// "no filter" on that field.
type PackageFilter struct {
	CaseID  *uuid.UUID
	UseCase string
	Status  string
}

func (s *Store) ListPackages(ctx context.Context, filter PackageFilter) ([]PackageSummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, case_id, use_case, prompt_version, status, created_by, submitted_at, completed_at, error, retry_of, created_at
		FROM packages
		WHERE ($1::uuid IS NULL OR case_id = $1)
		  AND ($2 = '' OR use_case = $2)
		  AND ($3 = '' OR status = $3)
		ORDER BY created_at DESC
	`, filter.CaseID, filter.UseCase, filter.Status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PackageSummary{}
	for rows.Next() {
		var p PackageSummary
		if err := rows.Scan(&p.ID, &p.CaseID, &p.UseCase, &p.PromptVersion, &p.Status, &p.CreatedBy, &p.SubmittedAt, &p.CompletedAt, &p.Error, &p.RetryOf, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) MarkPackageSubmitted(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE packages SET status = 'submitted', submitted_at = now() WHERE id = $1`, id)
	return err
}

func (s *Store) MarkPackageCompleted(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE packages SET status = 'completed', completed_at = now() WHERE id = $1`, id)
	return err
}

func (s *Store) MarkPackageFailed(ctx context.Context, id uuid.UUID, errMsg string) error {
	_, err := s.pool.Exec(ctx, `UPDATE packages SET status = 'failed', error = $2 WHERE id = $1`, id, errMsg)
	return err
}

func (s *Store) MarkPackageCancelled(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE packages SET status = 'cancelled' WHERE id = $1`, id)
	return err
}

// PackageResult is a row from package_results — a single ingested harness
// response for a package, with its schema-validation outcome.
type PackageResult struct {
	ID               uuid.UUID
	PackageID        uuid.UUID
	RawResponse      json.RawMessage
	ValidatedPayload json.RawMessage
	ValidationStatus string
	ReceivedAt       time.Time
}

type PackageResultInput struct {
	PackageID        uuid.UUID
	RawResponse      json.RawMessage
	ValidatedPayload json.RawMessage // nil when ValidationStatus is "invalid"
	ValidationStatus string
}

func (s *Store) InsertPackageResult(ctx context.Context, in PackageResultInput) (PackageResult, error) {
	var r PackageResult
	err := s.pool.QueryRow(ctx, `
		INSERT INTO package_results (package_id, raw_response, validated_payload, validation_status)
		VALUES ($1, $2, $3, $4)
		RETURNING id, package_id, raw_response, validated_payload, validation_status, received_at
	`, in.PackageID, []byte(in.RawResponse), nullableJSON(in.ValidatedPayload), in.ValidationStatus,
	).Scan(&r.ID, &r.PackageID, &r.RawResponse, &r.ValidatedPayload, &r.ValidationStatus, &r.ReceivedAt)
	return r, err
}

func nullableJSON(raw json.RawMessage) any {
	if raw == nil {
		return nil
	}
	return []byte(raw)
}
