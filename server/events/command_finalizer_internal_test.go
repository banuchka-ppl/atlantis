// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

const (
	finalizerRunID = "018f4f40-7a32-7b6c-8e5d-4c1f5b3a2d10"
	finalizerHead  = "0123456789abcdef0123456789abcdef01234567"
)

func TestCommandFinalizerAllocatesOnceAndPublishesOneImmutableCompletion(t *testing.T) {
	startedAt := time.Date(2026, time.August, 5, 15, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(time.Minute)
	publisher := &InMemoryCommandCompletionPublisher{}
	Ok(t, publisher.Start())
	finalizer := NewCommandFinalizer(publisher, []string{"ppl-ai/agi"})
	finalizer.newRunID = func() (string, error) { return finalizerRunID, nil }
	finalizer.now = func() time.Time { return startedAt }
	ctx := commandCompletionContextForFinalizer(t)
	cmd := &CommentCommand{Name: command.Plan}

	finalizer.Begin(ctx, cmd)
	finalizer.Begin(ctx, cmd)
	Equals(t, finalizerRunID, ctx.CommandRunID)
	Equals(t, startedAt, ctx.CommandStartedAt)

	finalizer.now = func() time.Time { return completedAt }
	result := command.Result{ProjectResults: []command.ProjectResult{successfulTypedPlanResult()}}
	publication := VCSResultPublication{
		State:                   VCSResultPublicationSucceeded,
		Action:                  VCSResultPublicationCreated,
		CommentIDs:              []int64{1234},
		RootCommentID:           1234,
		TerminalCommentID:       1234,
		NativeResultMarkerState: NativeResultMarkerPublished,
	}
	finalizer.Finalize(ctx, cmd, result, publication)

	events := publisher.Events()
	Equals(t, 1, len(events))
	Equals(t, finalizerRunID, events[0].CommandRunID)
	Equals(t, completedAt, events[0].CompletedAt)
	publication.CommentIDs[0] = 9999
	Equals(t, []string{"1234"}, events[0].VCSPublication.CommentIDs)
}

func TestCommandFinalizerDisabledDoesNotAllocateCommandIdentity(t *testing.T) {
	finalizer := NewCommandFinalizer(DisabledCommandCompletionPublisher{}, []string{"ppl-ai/agi"})
	identityCalls := 0
	finalizer.newRunID = func() (string, error) {
		identityCalls++
		return finalizerRunID, nil
	}
	ctx := commandCompletionContextForFinalizer(t)

	finalizer.Begin(ctx, &CommentCommand{Name: command.Plan})

	Equals(t, 0, identityCalls)
	Equals(t, "", ctx.CommandRunID)
	Equals(t, time.Time{}, ctx.CommandStartedAt)
}

func commandCompletionContextForFinalizer(t *testing.T) *command.Context {
	t.Helper()
	return &command.Context{
		Pull: models.PullRequest{
			Num:        123,
			HeadCommit: finalizerHead,
			BaseRepo: models.Repo{
				FullName: "ppl-ai/agi",
				Owner:    "ppl-ai",
				Name:     "agi",
				VCSHost:  models.VCSHost{Hostname: "github.com", Type: models.Github},
			},
		},
		Trigger: command.CommentTrigger,
		Log:     logging.NewNoopLogger(t).WithHistory(),
	}
}

func successfulTypedPlanResult() command.ProjectResult {
	return command.ProjectResult{
		Command:     command.Plan,
		RepoRelDir:  "infra/example",
		Workspace:   "default",
		ProjectName: "example",
		ProjectCommandOutput: command.ProjectCommandOutput{
			PlanSuccess: &models.PlanSuccess{},
			ProjectRunResult: &models.ProjectRunResult{
				Outcome: models.ProjectRunOutcomeSuccess,
				Changes: &models.ProjectRunChangeSummary{},
			},
		},
	}
}
