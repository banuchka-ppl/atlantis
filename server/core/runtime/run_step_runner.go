// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"errors"
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
	StructuredApplyResultsMode          StructuredRunResultMode
	StructuredRunResultRepoPatterns     []string
	StructuredRunResultWorkflowPatterns []string
	StructuredRunResultObserver         StructuredRunResultObserver
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
	result, err := r.RunWithResult(
		ctx,
		shell,
		command,
		path,
		envs,
		streamOutput,
		postProcessOutput,
		postProcessFilterRegexes,
	)
	if err != nil {
		return "", err
	}
	return result.ConsoleOutput, nil
}

// RunWithResult runs a custom command and may return an authoritative typed result.
func (r *RunStepRunner) RunWithResult(
	ctx command.ProjectContext,
	shell *valid.CommandShell,
	command string,
	path string,
	envs map[string]string,
	streamOutput bool,
	postProcessOutput []valid.PostProcessRunOutputOption,
	postProcessFilterRegexes []*regexp.Regexp,
) (RunStepOutput, error) {
	return r.runWithResult(
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
	result, err := r.runWithResult(
		ctx,
		shell,
		command,
		path,
		envs,
		streamOutput,
		postProcessOutput,
		postProcessFilterRegexes,
		structuredResultEligible,
	)
	if err != nil {
		return "", err
	}
	return result.ConsoleOutput, nil
}

func (r *RunStepRunner) runWithResult(
	ctx command.ProjectContext,
	shell *valid.CommandShell,
	command string,
	path string,
	envs map[string]string,
	streamOutput bool,
	postProcessOutput []valid.PostProcessRunOutputOption,
	postProcessFilterRegexes []*regexp.Regexp,
	structuredResultEligible bool,
) (RunStepOutput, error) {
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
		return RunStepOutput{}, err
	}

	structuredResult, err := r.prepareStructuredRunResult(ctx, path, structuredResultEligible)
	if err != nil {
		return RunStepOutput{}, err
	}
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
		// read a result outside the directory it allocated or spoof rollout authority.
		finalEnvVars = append(finalEnvVars, fmt.Sprintf("%s=%s", StepResultFileEnvVar, structuredResult.resultPath))
		finalEnvVars = append(finalEnvVars, fmt.Sprintf("%s=%s", StepResultModeEnvVar, structuredResult.mode))
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

	result := RunStepOutput{ConsoleOutput: output}
	if structuredResult != nil {
		completed, structuredResultErr := r.completeStructuredRunResult(
			ctx,
			path,
			structuredResult.resultPath,
			structuredResult.mode,
			RunExecution{ConsoleOutput: output, Err: err},
		)
		if structuredResult.mode == StructuredRunResultModePrefer ||
			structuredResult.mode == StructuredRunResultModeRequired {
			result.StructuredResult = completed
		}
		if structuredResultErr != nil {
			err = errors.Join(err, structuredResultErr)
		}
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
		return result, err
	}

	for _, processOutput := range postProcessOutput {
		switch processOutput {
		case valid.PostProcessRunOutputHide:
			output = ""
		default:
		}
	}

	result.ConsoleOutput = output
	return result, nil
}

type structuredRunResultSession struct {
	directory  string
	resultPath string
	mode       StructuredRunResultMode
}

func (r *RunStepRunner) prepareStructuredRunResult(
	ctx command.ProjectContext,
	workingDir string,
	eligible bool,
) (*structuredRunResultSession, error) {
	if !eligible {
		return nil, nil
	}
	mode := r.structuredRunResultMode(ctx.CommandName)
	if mode == StructuredRunResultModeOff {
		return nil, nil
	}
	if !matchesAnyPattern(ctx.BaseRepo.FullName, r.StructuredRunResultRepoPatterns) {
		return nil, nil
	}
	if !matchesAnyPattern(ctx.WorkflowName, r.StructuredRunResultWorkflowPatterns) {
		return nil, nil
	}

	resultDir, err := os.MkdirTemp(workingDir, ".atlantis-step-result-")
	if err != nil {
		if mode == StructuredRunResultModeRequired {
			r.StructuredRunResultObserver.recordArtifact(ctx.CommandName.String(), "unavailable")
			return nil, fmt.Errorf("allocating required structured run result path: %w", err)
		}
		ctx.Log.Warn("unable to allocate structured run result path; continuing without shadow validation: %s", err)
		return nil, nil
	}
	return &structuredRunResultSession{
		directory:  resultDir,
		resultPath: filepath.Join(resultDir, "result.json"),
		mode:       mode,
	}, nil
}

func (r *RunStepRunner) structuredRunResultMode(commandName command.Name) StructuredRunResultMode {
	var mode StructuredRunResultMode
	switch commandName {
	case command.Plan:
		mode = r.StructuredRunResultsMode
	case command.Apply:
		mode = r.StructuredApplyResultsMode
	default:
		return StructuredRunResultModeOff
	}
	if mode == "" {
		return StructuredRunResultModeOff
	}
	return mode
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
	mode StructuredRunResultMode,
	execution RunExecution,
) (*CompletedRun, error) {
	commandName := ctx.CommandName.String()
	if _, err := os.Lstat(resultPath); err != nil {
		if os.IsNotExist(err) {
			r.StructuredRunResultObserver.recordArtifact(commandName, "missing")
			if mode == StructuredRunResultModeRequired {
				return nil, errors.New("required structured run result is missing")
			}
			ctx.Log.Debug("custom run step did not publish an optional structured result")
			return nil, nil
		}
		r.StructuredRunResultObserver.recordArtifact(commandName, "invalid")
		if mode == StructuredRunResultModeRequired {
			return nil, fmt.Errorf("invalid required structured run result: inspecting artifact: %w", err)
		}
		ctx.Log.Warn("unable to inspect optional structured run result; legacy command result is unchanged: %s", err)
		return nil, nil
	}

	completed, err := (StructuredRunResultCompleter{}).CompleteRun(workingDir, resultPath, execution)
	if err != nil {
		r.StructuredRunResultObserver.recordArtifact(commandName, "invalid")
		if mode == StructuredRunResultModeRequired {
			return nil, fmt.Errorf("invalid required structured run result: %w", err)
		}
		ctx.Log.Warn("invalid optional structured run result; legacy command result is unchanged: %s", err)
		return nil, nil
	}
	r.StructuredRunResultObserver.recordArtifact(commandName, "valid")
	comparison := CompareStructuredRunResult(completed)
	if ctx.CommandName == command.Apply {
		comparison = CompareStructuredApplyResult(completed)
	}
	r.StructuredRunResultObserver.recordComparison(commandName, comparison)
	ctx.Log.Debug(
		"validated structured run result with outcome %q and shadow comparison %q",
		completed.Result.Outcome,
		comparison,
	)
	return &completed, nil
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
