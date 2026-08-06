// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package models

import "fmt"

// ProjectRunOutcome describes the execution outcome reported by a project step.
type ProjectRunOutcome string

const (
	ProjectRunOutcomeSuccess ProjectRunOutcome = "success"
	ProjectRunOutcomeError   ProjectRunOutcome = "error"
)

// ProjectRunReviewDetailMode selects the reviewer-detail projection.
type ProjectRunReviewDetailMode string

const (
	ProjectRunReviewDetailModeInline ProjectRunReviewDetailMode = "inline"
	ProjectRunReviewDetailModeURL    ProjectRunReviewDetailMode = "url"
)

// ProjectRunDiagnosticCode is a stable failure class safe for automation.
type ProjectRunDiagnosticCode string

const (
	ProjectRunDiagnosticCodePreconditionFailed ProjectRunDiagnosticCode = "precondition_failed"
	ProjectRunDiagnosticCodeProjectLocked      ProjectRunDiagnosticCode = "project_locked"
	ProjectRunDiagnosticCodePolicyFailed       ProjectRunDiagnosticCode = "policy_failed"
	ProjectRunDiagnosticCodeTerraformFailed    ProjectRunDiagnosticCode = "terraform_failed"
	ProjectRunDiagnosticCodeToolFailed         ProjectRunDiagnosticCode = "tool_failed"
	ProjectRunDiagnosticCodeArtifactFailed     ProjectRunDiagnosticCode = "artifact_failed"
	ProjectRunDiagnosticCodeInternalError      ProjectRunDiagnosticCode = "internal_error"

	// Classified command-failure codes. The step CLI derives these from
	// conservative failure signatures and publishes them with fixed
	// allowlisted summaries; values match the CLI's failure classes.
	ProjectRunDiagnosticCodeProviderRateLimited        ProjectRunDiagnosticCode = "provider_rate_limited"
	ProjectRunDiagnosticCodeProviderValidationRejected ProjectRunDiagnosticCode = "provider_validation_rejected"
	ProjectRunDiagnosticCodeProviderAuthFailed         ProjectRunDiagnosticCode = "provider_auth_failed"
	ProjectRunDiagnosticCodeHelmChartNotFound          ProjectRunDiagnosticCode = "helm_chart_not_found"
	ProjectRunDiagnosticCodeDependencyInitFailed       ProjectRunDiagnosticCode = "dependency_init_failed"
	ProjectRunDiagnosticCodeTimeout                    ProjectRunDiagnosticCode = "timeout"
)

// IsValid reports whether the code belongs to the versioned result contract.
func (c ProjectRunDiagnosticCode) IsValid() bool {
	switch c {
	case ProjectRunDiagnosticCodePreconditionFailed,
		ProjectRunDiagnosticCodeProjectLocked,
		ProjectRunDiagnosticCodePolicyFailed,
		ProjectRunDiagnosticCodeTerraformFailed,
		ProjectRunDiagnosticCodeToolFailed,
		ProjectRunDiagnosticCodeArtifactFailed,
		ProjectRunDiagnosticCodeInternalError,
		ProjectRunDiagnosticCodeProviderRateLimited,
		ProjectRunDiagnosticCodeProviderValidationRejected,
		ProjectRunDiagnosticCodeProviderAuthFailed,
		ProjectRunDiagnosticCodeHelmChartNotFound,
		ProjectRunDiagnosticCodeDependencyInitFailed,
		ProjectRunDiagnosticCodeTimeout:
		return true
	default:
		return false
	}
}

// ProjectRunChangeSummary contains normalized Terraform/OpenTofu plan facts.
type ProjectRunChangeSummary struct {
	HasChanges           bool
	HasOutputOnlyChanges bool
	Add                  int
	Change               int
	Destroy              int
	Import               int
	Forget               int
}

// ProjectRunReview is the bounded reviewer projection of a project result.
type ProjectRunReview struct {
	DetailMode   ProjectRunReviewDetailMode
	InlineDetail string
	DetailsURL   string
}

// ProjectRunDiagnostic is a bounded, stable failure description.
type ProjectRunDiagnostic struct {
	Code    ProjectRunDiagnosticCode
	Summary string
	Detail  string
}

// ProjectRunResult is the normalized typed result used by Atlantis consumers.
type ProjectRunResult struct {
	Outcome    ProjectRunOutcome
	Summary    string
	Changes    *ProjectRunChangeSummary
	Review     *ProjectRunReview
	Diagnostic *ProjectRunDiagnostic
}

// ReviewOutput returns the bounded reviewer projection without consulting console text.
func (r ProjectRunResult) ReviewOutput() string {
	if r.Review == nil {
		return r.Summary
	}
	if r.Review.InlineDetail != "" {
		return r.Review.InlineDetail
	}
	if r.Review.DetailsURL != "" {
		return fmt.Sprintf("%s\n\n[View plan details](%s)", r.Summary, r.Review.DetailsURL)
	}
	return r.Summary
}
