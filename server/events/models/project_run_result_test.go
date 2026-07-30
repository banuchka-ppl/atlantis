// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package models_test

import (
	"testing"

	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestProjectRunResultReviewOutput(t *testing.T) {
	tests := []struct {
		name     string
		result   models.ProjectRunResult
		expected string
	}{
		{
			name:     "summary only",
			result:   models.ProjectRunResult{Summary: "Terraform plan has no changes."},
			expected: "Terraform plan has no changes.",
		},
		{
			name: "inline detail",
			result: models.ProjectRunResult{
				Summary: "Terraform plan has changes.",
				Review: &models.ProjectRunReview{
					DetailMode:   models.ProjectRunReviewDetailModeInline,
					InlineDetail: "bounded reviewer detail",
				},
			},
			expected: "bounded reviewer detail",
		},
		{
			name: "details URL",
			result: models.ProjectRunResult{
				Summary: "Terraform plan has changes.",
				Review: &models.ProjectRunReview{
					DetailMode: models.ProjectRunReviewDetailModeURL,
					DetailsURL: "https://example.com/plan",
				},
			},
			expected: "Terraform plan has changes.\n\n[View plan details](https://example.com/plan)",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			Equals(t, test.expected, test.result.ReviewOutput())
		})
	}
}

func TestProjectRunDiagnosticCodeIsValid(t *testing.T) {
	validCodes := []models.ProjectRunDiagnosticCode{
		models.ProjectRunDiagnosticCodePreconditionFailed,
		models.ProjectRunDiagnosticCodeProjectLocked,
		models.ProjectRunDiagnosticCodePolicyFailed,
		models.ProjectRunDiagnosticCodeTerraformFailed,
		models.ProjectRunDiagnosticCodeToolFailed,
		models.ProjectRunDiagnosticCodeArtifactFailed,
		models.ProjectRunDiagnosticCodeInternalError,
	}

	for _, code := range validCodes {
		Assert(t, code.IsValid(), "expected %q to be valid", code)
	}
	Assert(
		t,
		!models.ProjectRunDiagnosticCode("unknown").IsValid(),
		"unknown diagnostic code must be rejected",
	)
}
