// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runatlantis/atlantis/server/core/runtime"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

// End-to-end nonleak proof for classified typed-result summaries: a
// CLI-shaped result.json flows through the completer's validation and
// mapping, ReviewerError, and the markdown renderer into the final comment.
// The classified summary must render; raw step output must not.
func TestClassifiedSummaryReachesRenderedCommentWithoutRawStderr(t *testing.T) {
	classifiedSummary := "Terraform plan failed: a provider API rate-limited the run. " +
		"This is usually transient — re-run `atlantis plan`."
	rawMarker := "raw-stderr-marker-do-not-render"

	workingDir := t.TempDir()
	resultPath := filepath.Join(workingDir, "result.json")
	resultJSON, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"outcome":        "error",
		"summary":        classifiedSummary,
		"diagnostic": map[string]any{
			"code":    "terraform_failed",
			"summary": classifiedSummary,
		},
	})
	Ok(t, err)
	Ok(t, os.WriteFile(resultPath, resultJSON, 0o600))

	execution := runtime.RunExecution{
		ConsoleOutput: "📦 Planning Terraform project\n" + rawMarker + "\n",
		Err:           errors.New("running 'sh -c': exit status 1: " + rawMarker),
	}
	completed, err := runtime.StructuredRunResultCompleter{}.CompleteRun(
		workingDir, resultPath, execution,
	)
	Ok(t, err)

	renderer := NewMarkdownRenderer(
		false,      // gitlabSupportsCommonMark
		false,      // disableApplyAll
		false,      // disableApply
		false,      // disableMarkdownFolding
		false,      // disableRepoLocking
		false,      // enableDiffMarkdownFormat
		"",         // markdownTemplateOverridesDir
		"atlantis", // executableName
		false,      // hideUnchangedPlanComments
		false,      // quietPolicyChecks
	)
	ctx := &command.Context{
		Log: logging.NewNoopLogger(t),
		Pull: models.PullRequest{
			BaseRepo: models.Repo{VCSHost: models.VCSHost{Type: models.Github}},
		},
	}
	projectResult := command.ProjectResult{
		ProjectCommandOutput: command.ProjectCommandOutput{
			Error:            execution.Err,
			ProjectRunResult: newProjectRunResult(&completed),
		},
		Command:    command.Plan,
		RepoRelDir: "infra/terraform/example",
		Workspace:  "default",
	}

	rendered := renderer.Render(
		ctx,
		command.Result{ProjectResults: []command.ProjectResult{projectResult}},
		&CommentCommand{Name: command.Plan},
	)

	Assert(t, strings.Contains(rendered, classifiedSummary),
		"classified summary must reach the rendered comment; got:\n%s", rendered)
	Assert(t, !strings.Contains(rendered, rawMarker),
		"raw step output must not reach the rendered comment; got:\n%s", rendered)

	// Without a typed result, ReviewerError falls back to the raw error —
	// the typed result is what keeps raw output out of the comment.
	projectResult.ProjectRunResult = nil
	fallback := renderer.Render(
		ctx,
		command.Result{ProjectResults: []command.ProjectResult{projectResult}},
		&CommentCommand{Name: command.Plan},
	)
	Assert(t, strings.Contains(fallback, rawMarker),
		"fallback control: raw error renders when no typed result exists; got:\n%s",
		fallback)
}
