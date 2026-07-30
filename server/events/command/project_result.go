// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package command

import (
	"fmt"

	"github.com/runatlantis/atlantis/server/events/models"
)

type ProjectFailureReason string

const ProjectLockFailureReason ProjectFailureReason = "project_lock"

// ProjectResult is the result of executing a plan/policy_check/apply for a specific project.
type ProjectResult struct {
	ProjectCommandOutput
	Command           Name
	SubCommand        string
	RepoRelDir        string
	Workspace         string
	ProjectName       string
	SilencePRComments []string
}

// ProjectCommandOutput is the output of a plan/policy_check/apply for a specific project.
type ProjectCommandOutput struct {
	Error              error
	Failure            string
	FailureReason      ProjectFailureReason
	BlockingPullNum    int
	ProjectRunResult   *models.ProjectRunResult
	PlanSuccess        *models.PlanSuccess
	PolicyCheckResults *models.PolicyCheckResults
	ApplySuccess       string
	ApplySuccessURL    string `json:"-"`
	VersionSuccess     string
	ImportSuccess      *models.ImportSuccess
	StateRmSuccess     *models.StateRmSuccess
}

// CommitStatus returns the vcs commit status of this project result.
func (p ProjectResult) CommitStatus() models.CommitStatus {
	if p.Error != nil {
		return models.FailedCommitStatus
	}
	if p.Failure != "" {
		return models.FailedCommitStatus
	}
	return models.SuccessCommitStatus
}

// PolicyStatus returns the approval status of policy sets of this project result.
func (p ProjectResult) PolicyStatus() []models.PolicySetStatus {
	var policyStatuses []models.PolicySetStatus
	if p.PolicyCheckResults != nil {
		for _, policySet := range p.PolicyCheckResults.PolicySetResults {
			policyStatus := models.PolicySetStatus{
				PolicySetName:   policySet.PolicySetName,
				Passed:          policySet.Passed,
				Approvals:       policySet.Approvals,
				Hashes:          policySet.Hashes,
				PolicyItemRegex: policySet.PolicyItemRegex,
			}
			policyStatuses = append(policyStatuses, policyStatus)
		}
	}
	return policyStatuses
}

// PlanStatus returns the plan status.
func (p ProjectResult) PlanStatus() models.ProjectPlanStatus {
	switch p.Command {

	case Plan:
		if p.Error != nil {
			return models.ErroredPlanStatus
		} else if p.Failure != "" {
			return models.ErroredPlanStatus
		} else if p.PlanNoChanges() {
			return models.PlannedNoChangesPlanStatus
		}
		return models.PlannedPlanStatus
	case PolicyCheck, ApprovePolicies:
		if p.Error != nil {
			return models.ErroredPolicyCheckStatus
		} else if p.Failure != "" {
			return models.ErroredPolicyCheckStatus
		}
		return models.PassedPolicyCheckStatus
	case Apply:
		if p.Error != nil {
			return models.ErroredApplyStatus
		} else if p.Failure != "" {
			return models.ErroredApplyStatus
		}
		return models.AppliedPlanStatus
	case Import, State:
		if p.Error != nil {
			return models.ErroredPlanStatus
		} else if p.Failure != "" {
			return models.ErroredPlanStatus
		}
		return models.DiscardedPlanStatus
	}

	panic("PlanStatus() missing a combination")
}

// PlanNoChanges reports the authoritative plan change classification.
func (p ProjectCommandOutput) PlanNoChanges() bool {
	if p.ProjectRunResult != nil && p.ProjectRunResult.Changes != nil {
		return !p.ProjectRunResult.Changes.HasChanges
	}
	return p.PlanSuccess != nil && p.PlanSuccess.NoChanges()
}

// PlanStats returns typed plan counts when available and otherwise uses legacy output.
func (p ProjectCommandOutput) PlanStats() models.PlanSuccessStats {
	if p.ProjectRunResult != nil && p.ProjectRunResult.Changes != nil {
		return models.PlanSuccessStats{
			Import:  p.ProjectRunResult.Changes.Import,
			Add:     p.ProjectRunResult.Changes.Add,
			Change:  p.ProjectRunResult.Changes.Change,
			Destroy: p.ProjectRunResult.Changes.Destroy,
			Forget:  p.ProjectRunResult.Changes.Forget,
			Changes: p.ProjectRunResult.Changes.HasChanges,
		}
	}
	if p.PlanSuccess == nil {
		return models.PlanSuccessStats{}
	}
	return p.PlanSuccess.Stats()
}

// PlanSummary returns typed reviewer summary when available and otherwise uses legacy output.
func (p ProjectCommandOutput) PlanSummary() string {
	if p.ProjectRunResult != nil {
		return p.ProjectRunResult.Summary
	}
	if p.PlanSuccess == nil {
		return ""
	}
	return p.PlanSuccess.Summary()
}

// PlanDiffSummary returns a status summary derived from authoritative plan facts.
func (p ProjectCommandOutput) PlanDiffSummary() string {
	if p.ProjectRunResult == nil || p.ProjectRunResult.Changes == nil {
		if p.PlanSuccess == nil {
			return ""
		}
		return p.PlanSuccess.DiffSummary()
	}
	changes := p.ProjectRunResult.Changes
	if !changes.HasChanges || changes.HasOutputOnlyChanges {
		return p.ProjectRunResult.Summary
	}
	switch {
	case changes.Import > 0 && changes.Forget > 0:
		return fmt.Sprintf(
			"Plan: %d to import, %d to add, %d to change, %d to destroy, %d to forget.",
			changes.Import,
			changes.Add,
			changes.Change,
			changes.Destroy,
			changes.Forget,
		)
	case changes.Import > 0:
		return fmt.Sprintf(
			"Plan: %d to import, %d to add, %d to change, %d to destroy.",
			changes.Import,
			changes.Add,
			changes.Change,
			changes.Destroy,
		)
	case changes.Forget > 0:
		return fmt.Sprintf(
			"Plan: %d to add, %d to change, %d to destroy, %d to forget.",
			changes.Add,
			changes.Change,
			changes.Destroy,
			changes.Forget,
		)
	default:
		return fmt.Sprintf(
			"Plan: %d to add, %d to change, %d to destroy.",
			changes.Add,
			changes.Change,
			changes.Destroy,
		)
	}
}

// ReviewerError returns a bounded typed diagnostic when available.
func (p ProjectCommandOutput) ReviewerError() string {
	if p.ProjectRunResult != nil && p.ProjectRunResult.Diagnostic != nil {
		if p.ProjectRunResult.Diagnostic.Detail != "" {
			return p.ProjectRunResult.Diagnostic.Detail
		}
		return p.ProjectRunResult.Diagnostic.Summary
	}
	if p.Error == nil {
		return ""
	}
	return p.Error.Error()
}

// IsSuccessful returns true if this project result had no errors.
func (p ProjectResult) IsSuccessful() bool {
	return p.PlanSuccess != nil || (p.PolicyCheckResults != nil && p.Error == nil && p.Failure == "") || p.ApplySuccess != ""
}
