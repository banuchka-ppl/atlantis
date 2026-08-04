// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/models/testdata"
	"github.com/runatlantis/atlantis/server/events/vcs"
	vcsmocks "github.com/runatlantis/atlantis/server/events/vcs/mocks"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func TestAppendNativeResultCommentMarker(t *testing.T) {
	cmd := &CommentCommand{
		Name:        command.Plan,
		SubName:     "",
		RepoRelDir:  "infra/prod",
		Workspace:   "default",
		ProjectName: "prod",
	}
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{Pull: pull}

	marker, err := encodeNativeResultCommentMarker(ctx, cmd, command.Result{})
	Ok(t, err)
	comment := appendNativeResultCommentMarker("rendered comment\n", marker)

	Equals(t, true, strings.HasPrefix(comment, "rendered comment\n\n"+nativeResultCommentMarkerPrefix))
	Equals(t, true, strings.HasSuffix(comment, nativeResultCommentMarkerSuffix))
	payload := decodeNativeResultCommentMarkerForTest(t, comment)
	Equals(t, nativeResultCommentMarkerType, payload.Type)
	Equals(t, nativeResultCommentMarkerVersion, payload.Version)
	Equals(t, "runatlantis", payload.RepoOwner)
	Equals(t, "atlantis", payload.RepoName)
	Equals(t, testdata.Pull.Num, payload.PullNum)
	Equals(t, testdata.Pull.HeadCommit, payload.HeadSHA)
	Equals(t, "plan", payload.Command)
	Equals(t, "infra/prod", payload.Dir)
	Equals(t, "default", payload.Workspace)
	Equals(t, "prod", payload.ProjectName)
	Equals(t, false, payload.Autoplan)
	Equals(t, "success", payload.Outcome)
	Equals(t, 0, payload.ProjectTotal)
	Equals(t, 0, len(payload.Projects))
	Equals(t, 0, len(payload.Failures))
}

func TestNativeResultCommentMarkerRecordsProjectFailures(t *testing.T) {
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{Pull: pull}
	result := command.Result{ProjectResults: []command.ProjectResult{
		{
			ProjectCommandOutput: command.ProjectCommandOutput{
				Failure:         "locked",
				FailureReason:   command.ProjectLockFailureReason,
				BlockingPullNum: 18748,
			},
			RepoRelDir:  "infra/locked",
			Workspace:   "default",
			ProjectName: "locked",
		},
		{
			ProjectCommandOutput: command.ProjectCommandOutput{
				Error: errors.New("boom"),
				ProjectRunResult: &models.ProjectRunResult{
					Outcome: models.ProjectRunOutcomeError,
					Diagnostic: &models.ProjectRunDiagnostic{
						Code:    "terraform_failed",
						Summary: "Terraform plan failed.",
					},
				},
			},
			RepoRelDir:  "infra/broken",
			Workspace:   "default",
			ProjectName: "broken",
		},
		{
			ProjectCommandOutput: command.ProjectCommandOutput{PlanSuccess: &models.PlanSuccess{}},
			RepoRelDir:           "infra/success",
			Workspace:            "default",
			ProjectName:          "success",
		},
	}}

	marker, err := encodeNativeResultCommentMarker(ctx, &CommentCommand{Name: command.Plan}, result)
	Ok(t, err)
	payload := decodeNativeResultCommentMarkerForTest(t, marker)

	Equals(t, "error", payload.Outcome)
	Equals(t, 3, payload.ProjectTotal)
	Equals(t, []nativeResultCommentMarkerProject{
		{
			Dir:         "infra/locked",
			Workspace:   "default",
			ProjectName: "locked",
			Outcome:     "project_lock",
		},
		{
			Dir:         "infra/broken",
			Workspace:   "default",
			ProjectName: "broken",
			Outcome:     "error",
		},
		{
			Dir:         "infra/success",
			Workspace:   "default",
			ProjectName: "success",
			Outcome:     "changes",
		},
	}, payload.Projects)
	Equals(t, []nativeResultCommentMarkerFailure{
		{
			Dir:             "infra/locked",
			Workspace:       "default",
			ProjectName:     "locked",
			Reason:          "project_lock",
			BlockingPullNum: 18748,
		},
		{
			Dir:         "infra/broken",
			Workspace:   "default",
			ProjectName: "broken",
			Reason:      "terraform_failed",
		},
	}, payload.Failures)
}

func TestNativeResultCommentMarkerKeepsApplyIdentityEnvelopeOnly(t *testing.T) {
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{Pull: pull}
	result := command.Result{ProjectResults: []command.ProjectResult{
		{
			ProjectCommandOutput: command.ProjectCommandOutput{ApplySuccess: "applied"},
			RepoRelDir:           "infra/applied",
			Workspace:            "default",
			ProjectName:          "applied",
		},
		{
			ProjectCommandOutput: command.ProjectCommandOutput{Error: errors.New("boom")},
			RepoRelDir:           "infra/broken",
			Workspace:            "default",
			ProjectName:          "broken",
		},
	}}

	marker, err := encodeNativeResultCommentMarker(ctx, &CommentCommand{Name: command.Apply}, result)
	Ok(t, err)
	payload := decodeNativeResultCommentMarkerForTest(t, marker)

	Equals(t, "apply", payload.Command)
	Equals(t, "error", payload.Outcome)
	Equals(t, 2, payload.ProjectTotal)
	Equals(t, 0, len(payload.Projects))
	Equals(t, 0, len(payload.Failures))
}

func TestNativeResultCommentMarkerRecordsNoChangeProjectOutcomes(t *testing.T) {
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{Pull: pull}
	result := command.Result{ProjectResults: []command.ProjectResult{
		{
			ProjectCommandOutput: command.ProjectCommandOutput{
				PlanSuccess: &models.PlanSuccess{
					TerraformOutput: "Plan: 1 to add, 0 to change, 0 to destroy.",
				},
				ProjectRunResult: &models.ProjectRunResult{
					Outcome: models.ProjectRunOutcomeSuccess,
					Changes: &models.ProjectRunChangeSummary{},
				},
			},
			RepoRelDir:  "infra/testing",
			Workspace:   "default",
			ProjectName: "testing",
		},
		{
			ProjectCommandOutput: command.ProjectCommandOutput{
				PlanSuccess: &models.PlanSuccess{
					TerraformOutput: "No changes. Your infrastructure matches the configuration.",
				},
				ProjectRunResult: &models.ProjectRunResult{
					Outcome: models.ProjectRunOutcomeSuccess,
					Changes: &models.ProjectRunChangeSummary{
						HasChanges:           true,
						HasOutputOnlyChanges: true,
					},
				},
			},
			RepoRelDir:  "infra/prod",
			Workspace:   "default",
			ProjectName: "prod",
		},
	}}

	marker, err := encodeNativeResultCommentMarker(ctx, &CommentCommand{Name: command.Plan}, result)
	Ok(t, err)
	payload := decodeNativeResultCommentMarkerForTest(t, marker)

	Equals(t, 2, payload.ProjectTotal)
	Equals(t, []nativeResultCommentMarkerProject{
		{
			Dir:         "infra/testing",
			Workspace:   "default",
			ProjectName: "testing",
			Outcome:     "no_changes",
		},
		{
			Dir:         "infra/prod",
			Workspace:   "default",
			ProjectName: "prod",
			Outcome:     "changes",
		},
	}, payload.Projects)
}

func TestPullUpdaterAddsNativeResultCommentMarkerWhenEnabled(t *testing.T) {
	RegisterMockTestingT(t)
	vcsClient := vcsmocks.NewMockClient()
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{
		Pull: pull,
		Log:  logging.NewNoopLogger(t).WithHistory(),
	}
	updater := &PullUpdater{
		NativeResultCommentMarkersEnabled: true,
		VCSClient:                         vcsClient,
		MarkdownRenderer:                  NewMarkdownRenderer(false, false, false, false, false, false, "", "atlantis", false, false),
	}

	updater.updatePull(ctx, &CommentCommand{Name: command.Plan}, command.Result{Error: errors.New("boom")})

	_, _, _, comment, _ := vcsClient.VerifyWasCalledOnce().CreateComment(
		Any[logging.SimpleLogging](),
		Eq(testdata.GithubRepo),
		Eq(testdata.Pull.Num),
		AnyString(),
		Eq(command.Plan.String()),
	).GetCapturedArguments()
	Equals(t, true, strings.Contains(comment, nativeResultCommentMarkerPrefix))
	payload := decodeNativeResultCommentMarkerForTest(t, comment)
	Equals(t, "plan", payload.Command)
}

func TestPullUpdaterDoesNotAddNativeResultCommentMarkerByDefault(t *testing.T) {
	RegisterMockTestingT(t)
	vcsClient := vcsmocks.NewMockClient()
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{
		Pull: pull,
		Log:  logging.NewNoopLogger(t).WithHistory(),
	}
	updater := &PullUpdater{
		VCSClient:        vcsClient,
		MarkdownRenderer: NewMarkdownRenderer(false, false, false, false, false, false, "", "atlantis", false, false),
	}

	updater.updatePull(ctx, &CommentCommand{Name: command.Plan}, command.Result{Error: errors.New("boom")})

	_, _, _, comment, _ := vcsClient.VerifyWasCalledOnce().CreateComment(
		Any[logging.SimpleLogging](),
		Eq(testdata.GithubRepo),
		Eq(testdata.Pull.Num),
		AnyString(),
		Eq(command.Plan.String()),
	).GetCapturedArguments()
	Equals(t, false, strings.Contains(comment, nativeResultCommentMarkerPrefix))
}

func TestPullUpdaterUpsertsNativeResultCommentWhenEnabled(t *testing.T) {
	client := &recordingNativeResultCommentClient{}
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{
		Pull: pull,
		Log:  logging.NewNoopLogger(t).WithHistory(),
	}
	updater := &PullUpdater{
		NativeResultCommentUpsertEnabled: true,
		VCSClient:                        client,
		MarkdownRenderer:                 NewMarkdownRenderer(false, false, false, false, false, false, "", "atlantis", false, false),
	}

	updater.updatePull(ctx, &CommentCommand{Name: command.Plan}, command.Result{Error: errors.New("boom")})

	Equals(t, 1, client.upsertCalls)
	Equals(t, 0, client.createCalls)
	Equals(t, command.Plan.String(), client.upsertCommand)
	Equals(t, false, strings.Contains(client.upsertComment, nativeResultCommentMarkerPrefix))
	payload := decodeNativeResultCommentMarkerForTest(t, client.upsertMarker)
	Equals(t, "plan", payload.Command)
}

func TestPullUpdaterDoesNotUpsertNativeAutoplanResultComment(t *testing.T) {
	client := &recordingNativeResultCommentClient{}
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{
		Pull: pull,
		Log:  logging.NewNoopLogger(t).WithHistory(),
	}
	updater := &PullUpdater{
		NativeResultCommentMarkersEnabled: true,
		NativeResultCommentUpsertEnabled:  true,
		VCSClient:                         client,
		MarkdownRenderer:                  NewMarkdownRenderer(false, false, false, false, false, false, "", "atlantis", false, false),
	}

	updater.updatePull(ctx, AutoplanCommand{}, command.Result{Error: errors.New("boom")})

	Equals(t, 0, client.upsertCalls)
	Equals(t, 1, client.createCalls)
	Equals(t, command.Plan.String(), client.createdCommand)
	Equals(t, true, strings.Contains(client.createdComment, nativeResultCommentMarkerPrefix))
	payload := decodeNativeResultCommentMarkerForTest(t, client.createdComment)
	Equals(t, "plan", payload.Command)
	Equals(t, true, payload.Autoplan)
}

func TestPullUpdaterDoesNotUpsertNativeApplyResultComment(t *testing.T) {
	client := &recordingNativeResultCommentClient{}
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{
		Pull: pull,
		Log:  logging.NewNoopLogger(t).WithHistory(),
	}
	updater := &PullUpdater{
		NativeResultCommentMarkersEnabled: true,
		NativeResultCommentUpsertEnabled:  true,
		VCSClient:                         client,
		MarkdownRenderer:                  NewMarkdownRenderer(false, false, false, false, false, false, "", "atlantis", false, false),
	}

	updater.updatePull(ctx, &CommentCommand{Name: command.Apply}, command.Result{Error: errors.New("boom")})

	Equals(t, 0, client.upsertCalls)
	Equals(t, 1, client.createCalls)
	Equals(t, command.Apply.String(), client.createdCommand)
	Equals(t, true, strings.Contains(client.createdComment, nativeResultCommentMarkerPrefix))
	payload := decodeNativeResultCommentMarkerForTest(t, client.createdComment)
	Equals(t, "apply", payload.Command)
}

func TestPullUpdaterDoesNotMarkNativeApplyResultWhenOnlyUpsertEnabled(t *testing.T) {
	client := &recordingNativeResultCommentClient{}
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{
		Pull: pull,
		Log:  logging.NewNoopLogger(t).WithHistory(),
	}
	updater := &PullUpdater{
		NativeResultCommentUpsertEnabled: true,
		VCSClient:                        client,
		MarkdownRenderer:                 NewMarkdownRenderer(false, false, false, false, false, false, "", "atlantis", false, false),
	}

	updater.updatePull(ctx, &CommentCommand{Name: command.Apply}, command.Result{Error: errors.New("boom")})

	Equals(t, 0, client.upsertCalls)
	Equals(t, 1, client.createCalls)
	Equals(t, command.Apply.String(), client.createdCommand)
	Equals(t, false, strings.Contains(client.createdComment, nativeResultCommentMarkerPrefix))
}

func TestPullUpdaterFallsBackToCreateCommentWhenNativeResultUpsertFails(t *testing.T) {
	client := &recordingNativeResultCommentClient{upsertErr: errors.New("api error")}
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{
		Pull: pull,
		Log:  logging.NewNoopLogger(t).WithHistory(),
	}
	updater := &PullUpdater{
		NativeResultCommentUpsertEnabled: true,
		VCSClient:                        client,
		MarkdownRenderer:                 NewMarkdownRenderer(false, false, false, false, false, false, "", "atlantis", false, false),
	}

	updater.updatePull(ctx, &CommentCommand{Name: command.Plan}, command.Result{Error: errors.New("boom")})

	Equals(t, 1, client.upsertCalls)
	Equals(t, 1, client.createCalls)
	Equals(t, true, strings.Contains(client.createdComment, nativeResultCommentMarkerPrefix))
}

func TestPullUpdaterFallsBackToUnmarkedCreateCommentWhenNativeResultUpsertUnsupported(t *testing.T) {
	client := &recordingNativeResultCommentClient{upsertErr: vcs.ErrNativeResultCommentUpsertUnsupported}
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{
		Pull: pull,
		Log:  logging.NewNoopLogger(t).WithHistory(),
	}
	updater := &PullUpdater{
		NativeResultCommentUpsertEnabled: true,
		VCSClient:                        client,
		MarkdownRenderer:                 NewMarkdownRenderer(false, false, false, false, false, false, "", "atlantis", false, false),
	}

	updater.updatePull(ctx, &CommentCommand{Name: command.Plan}, command.Result{Error: errors.New("boom")})

	Equals(t, 1, client.upsertCalls)
	Equals(t, 1, client.createCalls)
	Equals(t, false, strings.Contains(client.createdComment, nativeResultCommentMarkerPrefix))
}

func TestPullUpdaterRoutesNativeResultMarkerThroughTrailerCommenter(t *testing.T) {
	client := &recordingNativeResultTrailerClient{}
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{
		Pull: pull,
		Log:  logging.NewNoopLogger(t).WithHistory(),
	}
	updater := &PullUpdater{
		NativeResultCommentMarkersEnabled: true,
		VCSClient:                         client,
		MarkdownRenderer:                  NewMarkdownRenderer(false, false, false, false, false, false, "", "atlantis", false, false),
	}

	updater.updatePull(ctx, &CommentCommand{Name: command.Apply}, command.Result{Error: errors.New("boom")})

	Equals(t, 1, client.trailerCalls)
	Equals(t, 0, client.createCalls)
	Equals(t, command.Apply.String(), client.trailerCommand)
	Equals(t, false, strings.Contains(client.trailerComment, nativeResultCommentMarkerPrefix))
	payload := decodeNativeResultCommentMarkerForTest(t, client.trailerMarker)
	Equals(t, "apply", payload.Command)
}

func TestPullUpdaterFallsBackToMarkedCreateCommentWhenTrailerUnsupported(t *testing.T) {
	client := &recordingNativeResultTrailerClient{trailerErr: vcs.ErrNativeResultTrailerCommentUnsupported}
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{
		Pull: pull,
		Log:  logging.NewNoopLogger(t).WithHistory(),
	}
	updater := &PullUpdater{
		NativeResultCommentMarkersEnabled: true,
		VCSClient:                         client,
		MarkdownRenderer:                  NewMarkdownRenderer(false, false, false, false, false, false, "", "atlantis", false, false),
	}

	updater.updatePull(ctx, &CommentCommand{Name: command.Apply}, command.Result{Error: errors.New("boom")})

	Equals(t, 1, client.trailerCalls)
	Equals(t, 1, client.createCalls)
	Equals(t, true, strings.Contains(client.createdComment, nativeResultCommentMarkerPrefix))
}

func TestPullUpdaterDoesNotRetryAfterTrailerCommentFailure(t *testing.T) {
	client := &recordingNativeResultTrailerClient{trailerErr: errors.New("api error")}
	pull := testdata.Pull
	pull.BaseRepo = testdata.GithubRepo
	ctx := &command.Context{
		Pull: pull,
		Log:  logging.NewNoopLogger(t).WithHistory(),
	}
	updater := &PullUpdater{
		NativeResultCommentMarkersEnabled: true,
		VCSClient:                         client,
		MarkdownRenderer:                  NewMarkdownRenderer(false, false, false, false, false, false, "", "atlantis", false, false),
	}

	updater.updatePull(ctx, &CommentCommand{Name: command.Apply}, command.Result{Error: errors.New("boom")})

	Equals(t, 1, client.trailerCalls)
	Equals(t, 0, client.createCalls)
}

func decodeNativeResultCommentMarkerForTest(t *testing.T, comment string) nativeResultCommentMarkerPayload {
	t.Helper()
	_, encoded, ok := strings.Cut(comment, nativeResultCommentMarkerPrefix)
	Equals(t, true, ok)
	encoded, _, ok = strings.Cut(encoded, nativeResultCommentMarkerSuffix)
	Equals(t, true, ok)

	rawPayload, err := base64.RawURLEncoding.DecodeString(encoded)
	Ok(t, err)
	var payload nativeResultCommentMarkerPayload
	Ok(t, json.Unmarshal(rawPayload, &payload))
	return payload
}

type recordingNativeResultCommentClient struct {
	vcs.NotConfiguredVCSClient
	upsertErr      error
	upsertCalls    int
	createCalls    int
	upsertComment  string
	upsertCommand  string
	upsertMarker   string
	createdComment string
	createdCommand string
}

func (c *recordingNativeResultCommentClient) CreateComment(_ logging.SimpleLogging, _ models.Repo, _ int, comment string, command string) error {
	c.createCalls++
	c.createdComment = comment
	c.createdCommand = command
	return nil
}

func (c *recordingNativeResultCommentClient) UpsertNativeResultComment(_ logging.SimpleLogging, _ models.Repo, _ int, comment string, command string, marker string) error {
	c.upsertCalls++
	c.upsertComment = comment
	c.upsertCommand = command
	c.upsertMarker = marker
	return c.upsertErr
}

type recordingNativeResultTrailerClient struct {
	recordingNativeResultCommentClient
	trailerErr     error
	trailerCalls   int
	trailerComment string
	trailerCommand string
	trailerMarker  string
}

func (c *recordingNativeResultTrailerClient) CreateCommentWithNativeResultTrailer(_ logging.SimpleLogging, _ models.Repo, _ int, comment string, command string, marker string) error {
	c.trailerCalls++
	c.trailerComment = comment
	c.trailerCommand = command
	c.trailerMarker = marker
	return c.trailerErr
}
