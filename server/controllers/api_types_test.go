// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"testing"

	"github.com/runatlantis/atlantis/server/controllers"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestNewProjectResultAPI_IncludesForgetCount(t *testing.T) {
	result := controllers.NewProjectResultAPI(command.ProjectResult{
		Command: command.Plan,
		ProjectCommandOutput: command.ProjectCommandOutput{
			PlanSuccess: &models.PlanSuccess{
				TerraformOutput: "Plan: 0 to add, 0 to change, 0 to destroy, 2 to forget.",
			},
		},
	})

	Assert(t, result.Plan != nil, "expected plan details")
	Equals(t, 2, result.Plan.ToForget)
}

func TestNewProjectResultAPI_UsesTypedPlanFacts(t *testing.T) {
	result := controllers.NewProjectResultAPI(command.ProjectResult{
		Command: command.Plan,
		ProjectCommandOutput: command.ProjectCommandOutput{
			PlanSuccess: &models.PlanSuccess{
				TerraformOutput: "No changes. Your infrastructure matches the configuration.",
			},
			ProjectRunResult: &models.ProjectRunResult{
				Outcome: models.ProjectRunOutcomeSuccess,
				Summary: "Terraform plan has changes.",
				Changes: &models.ProjectRunChangeSummary{
					HasChanges: true,
					Add:        2,
					Destroy:    1,
				},
			},
		},
	})

	Assert(t, result.Plan != nil, "expected plan details")
	Equals(t, true, result.Plan.HasChanges)
	Equals(t, 2, result.Plan.ToAdd)
	Equals(t, 1, result.Plan.ToDestroy)
	Equals(t, "Plan: 2 to add, 0 to change, 1 to destroy.", result.Plan.Summary)
}

func TestNewDriftProjectAPI_IncludesForgetCount(t *testing.T) {
	result := controllers.NewDriftProjectAPI(models.ProjectDrift{
		Drift: models.DriftSummary{
			HasDrift: true,
			ToForget: 3,
		},
	})

	Assert(t, result.Drift != nil, "expected drift details")
	Equals(t, 3, result.Drift.ToForget)
	Equals(t, 3, result.Drift.TotalChanges)
}
