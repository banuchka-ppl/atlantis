// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

const commandCompletionSensitiveFailure = "credential=must-not-leak"

var commandCompletionInternalStartedAt = time.Date(2026, time.August, 5, 15, 0, 0, 0, time.UTC)

func TestBuildCommandCompletionUsesOnlyBoundedTypedDiagnostics(t *testing.T) {
	ctx := commandCompletionContextForFinalizer(t)
	ctx.CommandRunID = finalizerRunID
	ctx.CommandStartedAt = commandCompletionInternalStartedAt
	longSummary := strings.Repeat("é", maxCommandDiagnosticBytes)
	result := command.Result{ProjectResults: []command.ProjectResult{{
		Command:     command.Plan,
		RepoRelDir:  "infra/example",
		Workspace:   "default",
		ProjectName: "example",
		ProjectCommandOutput: command.ProjectCommandOutput{
			Error: errors.New(commandCompletionSensitiveFailure),
			ProjectRunResult: &models.ProjectRunResult{
				Outcome: models.ProjectRunOutcomeError,
				Diagnostic: &models.ProjectRunDiagnostic{
					Code:    models.ProjectRunDiagnosticCodeToolFailed,
					Summary: longSummary,
					Detail:  commandCompletionSensitiveFailure,
				},
			},
		},
	}}}

	completion, err := BuildCommandCompletion(
		ctx,
		CommentCommand{Name: command.Plan},
		result,
		VCSResultPublication{
			State:                   VCSResultPublicationFailed,
			Action:                  VCSResultPublicationNone,
			NativeResultMarkerState: NativeResultMarkerNotRequested,
		},
		ctx.CommandStartedAt.Add(time.Minute),
	)
	Ok(t, err)
	encoded, err := EncodeCommandCompletion(completion)
	Ok(t, err)
	diagnostic := completion.Execution.Projects[0].Diagnostic
	Assert(t, diagnostic != nil, "expected a typed project diagnostic")
	Equals(t, models.ProjectRunDiagnosticCodeToolFailed, diagnostic.Code)
	Assert(t, len(diagnostic.Summary) <= maxCommandDiagnosticBytes, "diagnostic exceeds byte bound")
	Assert(t, utf8.ValidString(diagnostic.Summary), "bounded diagnostic is invalid UTF-8")
	Assert(t, !strings.Contains(string(encoded), commandCompletionSensitiveFailure), "raw failure detail leaked into completion")
}

func TestBuildCommandCompletionUsesFixedCommandLevelDiagnostic(t *testing.T) {
	ctx := commandCompletionContextForFinalizer(t)
	ctx.CommandRunID = finalizerRunID
	ctx.CommandStartedAt = commandCompletionInternalStartedAt
	completion, err := BuildCommandCompletion(
		ctx,
		&CommentCommand{Name: command.Plan},
		command.Result{Error: errors.New(commandCompletionSensitiveFailure)},
		VCSResultPublication{
			State:                   VCSResultPublicationFailed,
			Action:                  VCSResultPublicationNone,
			NativeResultMarkerState: NativeResultMarkerNotRequested,
		},
		ctx.CommandStartedAt.Add(time.Minute),
	)
	Ok(t, err)
	Equals(t, &CommandCompletionDiagnostic{
		Code:    models.ProjectRunDiagnosticCodeInternalError,
		Summary: "command execution failed",
	}, completion.Execution.Diagnostic)
}

func TestEncodeCommandCompletionRejectsInvalidContractMutations(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*CommandCompletionV1)
	}{
		{
			name: "non-v7 identity",
			mutate: func(completion *CommandCompletionV1) {
				completion.CommandRunID = "00000000-0000-4000-8000-000000000000"
				completion.IdempotencyKey = completion.CommandRunID
			},
		},
		{
			name: "duplicate project identity",
			mutate: func(completion *CommandCompletionV1) {
				completion.Execution.Projects = append(completion.Execution.Projects, completion.Execution.Projects[0])
				completion.Execution.ProjectTotal = len(completion.Execution.Projects)
			},
		},
		{
			name: "successful project without typed changes",
			mutate: func(completion *CommandCompletionV1) {
				completion.Execution.Projects[0].Changes = nil
			},
		},
		{
			name: "absolute project directory",
			mutate: func(completion *CommandCompletionV1) {
				completion.Execution.Projects[0].Dir = "/workspace/secret"
			},
		},
		{
			name: "inconsistent change summary",
			mutate: func(completion *CommandCompletionV1) {
				completion.Execution.Projects[0].Changes.HasChanges = false
				completion.Execution.Projects[0].Changes.Add = 1
			},
		},
		{
			name: "unknown diagnostic code",
			mutate: func(completion *CommandCompletionV1) {
				project := &completion.Execution.Projects[0]
				project.Outcome = "error"
				project.Changes = nil
				project.Diagnostic = &CommandCompletionDiagnostic{
					Code:    models.ProjectRunDiagnosticCode("unknown"),
					Summary: "failed",
				}
				completion.Execution.Outcome = "error"
			},
		},
		{
			name: "terminal comment is not final",
			mutate: func(completion *CommandCompletionV1) {
				completion.VCSPublication.CommentIDs = []string{"1234", "5678"}
				completion.VCSPublication.TerminalCommentID = "1234"
			},
		},
		{
			name: "partial publication claims marker",
			mutate: func(completion *CommandCompletionV1) {
				completion.VCSPublication.State = VCSResultPublicationPartial
				completion.VCSPublication.TerminalCommentID = ""
			},
		},
		{
			name: "oversized event",
			mutate: func(completion *CommandCompletionV1) {
				completion.Command.Subcommand = strings.Repeat("x", MaxCommandCompletionBytes)
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			completion := commandCompletionForTransport(t)
			testCase.mutate(&completion)
			_, err := EncodeCommandCompletion(completion)
			Assert(t, err != nil, "expected invalid contract mutation to be rejected")
		})
	}
}

func TestBuildCommandCompletionRejectsExecutionIdentityConflicts(t *testing.T) {
	ctx := commandCompletionContextForFinalizer(t)
	ctx.CommandRunID = finalizerRunID
	ctx.CommandStartedAt = commandCompletionInternalStartedAt
	result := successfulTypedPlanResult()
	result.Command = command.Apply

	_, err := BuildCommandCompletion(
		ctx,
		&CommentCommand{Name: command.Plan},
		command.Result{ProjectResults: []command.ProjectResult{result}},
		VCSResultPublication{
			State:                   VCSResultPublicationFailed,
			Action:                  VCSResultPublicationNone,
			NativeResultMarkerState: NativeResultMarkerNotRequested,
		},
		ctx.CommandStartedAt.Add(time.Minute),
	)
	Assert(t, err != nil, "expected mismatched project command to be rejected")
}
