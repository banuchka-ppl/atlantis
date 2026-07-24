// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/go-version"
	"github.com/runatlantis/atlantis/server/core/config/valid"
	"github.com/runatlantis/atlantis/server/core/runtime/models"
	"github.com/runatlantis/atlantis/server/core/terraform"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/jobs"
)

// RunStepRunner runs custom commands.
type RunStepRunner struct {
	TerraformExecutor     TerraformExec
	DefaultTFDistribution terraform.Distribution
	DefaultTFVersion      *version.Version
	// TerraformBinDir is the directory where Atlantis downloads Terraform binaries.
	TerraformBinDir                     string
	ProjectCmdOutputHandler             jobs.ProjectCommandOutputHandler
	StructuredRunResultsMode            StructuredRunResultMode
	StructuredRunResultRepoPatterns     []string
	StructuredRunResultWorkflowPatterns []string
}

func (r *RunStepRunner) Run(
	ctx command.ProjectContext,
	shell *valid.CommandShell,
	command string,
	path string,
	envs map[string]string,
	streamOutput bool,
	postProcessOutput []valid.PostProcessRunOutputOption,
	postProcessFilterRegexes []*regexp.Regexp,
) (string, error) {
	return r.run(
		ctx,
		shell,
		command,
		path,
		envs,
		streamOutput,
		postProcessOutput,
		postProcessFilterRegexes,
		true,
	)
}

func (r *RunStepRunner) run(
	ctx command.ProjectContext,
	shell *valid.CommandShell,
	command string,
	path string,
	envs map[string]string,
	streamOutput bool,
	postProcessOutput []valid.PostProcessRunOutputOption,
	postProcessFilterRegexes []*regexp.Regexp,
	structuredResultEligible bool,
) (string, error) {
	tfDistribution := r.DefaultTFDistribution
	tfVersion := r.DefaultTFVersion
	if ctx.TerraformDistribution != nil {
		tfDistribution = terraform.NewDistribution(*ctx.TerraformDistribution)
	}
	if ctx.TerraformVersion != nil {
		tfVersion = ctx.TerraformVersion
	}

	err := r.TerraformExecutor.EnsureVersion(ctx.Log, tfDistribution, tfVersion)
	if err != nil {
		err = fmt.Errorf("%s: Downloading terraform Version %s", err, tfVersion.String())
		ctx.Log.Debug("error: %s", err)
		return "", err
	}

	structuredResult := r.prepareStructuredRunResult(ctx, path, structuredResultEligible)
	if structuredResult != nil {
		defer structuredResult.cleanup(ctx)
	}

	baseEnvVars := os.Environ()
	customEnvVars := map[string]string{
		"ATLANTIS_TERRAFORM_DISTRIBUTION": tfDistribution.BinName(),
		"ATLANTIS_TERRAFORM_VERSION":      tfVersion.String(),
		"BASE_BRANCH_NAME":                ctx.Pull.BaseBranch,
		"BASE_REPO_NAME":                  ctx.BaseRepo.Name,
		"BASE_REPO_OWNER":                 ctx.BaseRepo.Owner,
		"COMMENT_ARGS":                    strings.Join(ctx.EscapedCommentArgs, ","),
		"DIR":                             path,
		"HEAD_BRANCH_NAME":                ctx.Pull.HeadBranch,
		"HEAD_COMMIT":                     ctx.Pull.HeadCommit,
		"HEAD_REPO_NAME":                  ctx.HeadRepo.Name,
		"HEAD_REPO_OWNER":                 ctx.HeadRepo.Owner,
		"PATH":                            fmt.Sprintf("%s:%s", os.Getenv("PATH"), r.TerraformBinDir),
		"PLANFILE":                        filepath.Join(path, GetPlanFilename(ctx.Workspace, ctx.ProjectName)),
		"SHOWFILE":                        filepath.Join(path, ctx.GetShowResultFileName()),
		"POLICYCHECKFILE":                 filepath.Join(path, ctx.GetPolicyCheckResultFileName()),
		"PROJECT_NAME":                    ctx.ProjectName,
		"PULL_AUTHOR":                     ctx.Pull.Author,
		"PULL_NUM":                        fmt.Sprintf("%d", ctx.Pull.Num),
		"PULL_URL":                        ctx.Pull.URL,
		"REPO_REL_DIR":                    ctx.RepoRelDir,
		"USER_NAME":                       ctx.User.Username,
		"WORKSPACE":                       ctx.Workspace,
	}
	// Add PR metadata environment variables for plan and apply steps
	if ctx.CommandName.String() == "plan" || ctx.CommandName.String() == "apply" {
		customEnvVars["ATLANTIS_PR_APPROVED"] = strconv.FormatBool(ctx.PullReqStatus.ApprovalStatus.IsApproved)
		customEnvVars["ATLANTIS_PR_MERGEABLE"] = strconv.FormatBool(ctx.PullReqStatus.MergeableStatus.IsMergeable)
	}

	finalEnvVars := baseEnvVars
	for key, val := range customEnvVars {
		finalEnvVars = append(finalEnvVars, fmt.Sprintf("%s=%s", key, val))
	}
	for key, val := range envs {
		finalEnvVars = append(finalEnvVars, fmt.Sprintf("%s=%s", key, val))
	}
	if structuredResult != nil {
		// Append this last so a workflow-provided env cannot redirect Atlantis to
		// read a result outside the directory it allocated.
		finalEnvVars = append(finalEnvVars, fmt.Sprintf("%s=%s", StepResultFileEnvVar, structuredResult.resultPath))
	}

	runner := models.NewShellCommandRunner(shell, command, finalEnvVars, path, streamOutput, r.ProjectCmdOutputHandler)
	output, err := runner.Run(ctx)

	// These need to run before the error check to filter output
	for _, processOutput := range postProcessOutput {
		switch processOutput {
		case valid.PostProcessRunOutputStripRefreshing:
			output = StripRefreshingFromPlanOutput(output, tfVersion)
		case valid.PostProcessRunOutputFilterRegexKey:
			for _, filterRegexes := range postProcessFilterRegexes {
				output = FilterRegexFromPlanOutput(output, filterRegexes)
			}
		}
	}

	if structuredResult != nil {
		r.completeStructuredRunResult(
			ctx,
			path,
			structuredResult.resultPath,
			RunExecution{ConsoleOutput: output, Err: err},
		)
	}

	if err != nil {
		err = runStepError{
			err:          err,
			command:      command,
			path:         path,
			output:       output,
			streamOutput: streamOutput,
		}
		if !ctx.CustomPolicyCheck {
			ctx.Log.Debug("error: %s", err)
		} else {
			ctx.Log.Debug("Treating custom policy tool error exit code as a policy failure.  Error output: %s", err)
		}
		return "", err
	}

	for _, processOutput := range postProcessOutput {
		switch processOutput {
		case valid.PostProcessRunOutputHide:
			output = ""
		default:
		}
	}

	return output, nil
}

type structuredRunResultSession struct {
	directory  string
	resultPath string
}

func (r *RunStepRunner) prepareStructuredRunResult(
	ctx command.ProjectContext,
	workingDir string,
	eligible bool,
) *structuredRunResultSession {
	if !eligible {
		return nil
	}
	if r.StructuredRunResultsMode != StructuredRunResultModeShadow {
		return nil
	}
	if ctx.CommandName != command.Plan && ctx.CommandName != command.Apply {
		return nil
	}
	if !matchesAnyPattern(ctx.BaseRepo.FullName, r.StructuredRunResultRepoPatterns) {
		return nil
	}
	if !matchesAnyPattern(ctx.WorkflowName, r.StructuredRunResultWorkflowPatterns) {
		return nil
	}

	resultDir, err := os.MkdirTemp(workingDir, ".atlantis-step-result-")
	if err != nil {
		ctx.Log.Warn("unable to allocate structured run result path; continuing without shadow validation: %s", err)
		return nil
	}
	return &structuredRunResultSession{
		directory:  resultDir,
		resultPath: filepath.Join(resultDir, "result.json"),
	}
}

func matchesAnyPattern(value string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, err := path.Match(pattern, value)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func (s structuredRunResultSession) cleanup(ctx command.ProjectContext) {
	if err := os.RemoveAll(s.directory); err != nil {
		ctx.Log.Warn("unable to clean up structured run result directory: %s", err)
	}
}

func (r *RunStepRunner) completeStructuredRunResult(
	ctx command.ProjectContext,
	workingDir string,
	resultPath string,
	execution RunExecution,
) {
	if _, err := os.Lstat(resultPath); err != nil {
		if os.IsNotExist(err) {
			ctx.Log.Debug("custom run step did not publish an optional structured result")
			return
		}
		ctx.Log.Warn("unable to inspect optional structured run result; legacy command result is unchanged: %s", err)
		return
	}

	completed, err := (StructuredRunResultCompleter{}).CompleteRun(workingDir, resultPath, execution)
	if err != nil {
		ctx.Log.Warn("invalid optional structured run result; legacy command result is unchanged: %s", err)
		return
	}
	ctx.Log.Debug("validated optional structured run result with outcome %q", completed.Result.Outcome)
}

type runStepError struct {
	err          error
	command      string
	path         string
	output       string
	streamOutput bool
}

func (e runStepError) Error() string {
	return fmt.Sprintf("%s: running %q in %q: \n%s", e.err, e.command, e.path, e.output)
}

func (e runStepError) JobMessage() string {
	if !e.streamOutput && e.output != "" {
		return e.Error()
	}
	return e.err.Error()
}
