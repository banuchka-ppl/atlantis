// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

const (
	commandCompletionRunID = "018f4f40-7a32-7b6c-8e5d-4c1f5b3a2d10"
	commandCompletionHead  = "0123456789abcdef0123456789abcdef01234567"
)

func TestBuildCommandCompletionProducesDeterministicTypedContract(t *testing.T) {
	startedAt := time.Date(2026, time.August, 5, 14, 0, 0, 123, time.UTC)
	completedAt := startedAt.Add(2 * time.Minute)
	ctx := &command.Context{
		CommandRunID:     commandCompletionRunID,
		CommandStartedAt: startedAt,
		Pull: models.PullRequest{
			Num:        123,
			HeadCommit: commandCompletionHead,
			BaseRepo: models.Repo{
				Owner: "ppl-ai",
				Name:  "agi",
				VCSHost: models.VCSHost{
					Hostname: "github.com",
					Type:     models.Github,
				},
			},
		},
		Trigger: command.CommentTrigger,
	}
	cmd := events.NewCommentCommand(
		"infra",
		nil,
		command.Plan,
		"",
		false,
		false,
		"",
		"default",
		"",
		"",
		false,
	)
	result := command.Result{ProjectResults: []command.ProjectResult{
		{
			Command:     command.Plan,
			RepoRelDir:  "infra/zeta",
			Workspace:   "default",
			ProjectName: "zeta",
			ProjectCommandOutput: command.ProjectCommandOutput{
				PlanSuccess: &models.PlanSuccess{},
				ProjectRunResult: &models.ProjectRunResult{
					Outcome: models.ProjectRunOutcomeSuccess,
					Changes: &models.ProjectRunChangeSummary{
						HasChanges: true,
						Add:        1,
					},
				},
			},
		},
		{
			Command:     command.Plan,
			RepoRelDir:  "infra/alpha",
			Workspace:   "production",
			ProjectName: "alpha",
			ProjectCommandOutput: command.ProjectCommandOutput{
				PlanSuccess: &models.PlanSuccess{},
				ProjectRunResult: &models.ProjectRunResult{
					Outcome: models.ProjectRunOutcomeSuccess,
					Changes: &models.ProjectRunChangeSummary{},
				},
			},
		},
	}}
	publication := events.VCSResultPublication{
		State:                   events.VCSResultPublicationSucceeded,
		Action:                  events.VCSResultPublicationCreated,
		CommentIDs:              []int64{1001, 1002},
		RootCommentID:           1001,
		TerminalCommentID:       1002,
		NativeResultMarkerState: events.NativeResultMarkerPublished,
	}

	completion, err := events.BuildCommandCompletion(ctx, cmd, result, publication, completedAt)
	Ok(t, err)
	encoded, err := events.EncodeCommandCompletion(completion)
	Ok(t, err)

	Equals(t, `{"type":"command_completion","schema_version":1,"command_run_id":"018f4f40-7a32-7b6c-8e5d-4c1f5b3a2d10","idempotency_key":"018f4f40-7a32-7b6c-8e5d-4c1f5b3a2d10","started_at":"2026-08-05T14:00:00.000000123Z","completed_at":"2026-08-05T14:02:00.000000123Z","repository":{"vcs_host":"github.com","owner":"ppl-ai","name":"agi"},"pull_request":{"number":123,"head_sha":"0123456789abcdef0123456789abcdef01234567"},"command":{"name":"plan","trigger":"comment","subcommand":"","requested_scope":{"dir":"infra","workspace":"default","project_name":""}},"execution":{"outcome":"success","project_total":2,"projects":[{"dir":"infra/alpha","workspace":"production","project_name":"alpha","outcome":"success","changes":{"has_changes":false,"has_output_only_changes":false,"add":0,"change":0,"destroy":0,"import":0,"forget":0}},{"dir":"infra/zeta","workspace":"default","project_name":"zeta","outcome":"success","changes":{"has_changes":true,"has_output_only_changes":false,"add":1,"change":0,"destroy":0,"import":0,"forget":0}}]},"vcs_publication":{"state":"succeeded","action":"created","native_result_marker_state":"published","comment_ids":["1001","1002"],"root_comment_id":"1001","terminal_comment_id":"1002"}}`, string(encoded))
}
