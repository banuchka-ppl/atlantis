// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package runtime_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/go-version"
	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server/core/runtime"
	tf "github.com/runatlantis/atlantis/server/core/terraform"
	"github.com/runatlantis/atlantis/server/core/terraform/mocks"
	tfclientmocks "github.com/runatlantis/atlantis/server/core/terraform/tfclient/mocks"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	jobmocks "github.com/runatlantis/atlantis/server/jobs/mocks"
	"github.com/runatlantis/atlantis/server/logging"
	loggingmocks "github.com/runatlantis/atlantis/server/logging/mocks"
	. "github.com/runatlantis/atlantis/testing"
	tally "github.com/uber-go/tally/v4"
)

const pplxManagedWorkflow = "terraform-just-a1b2c3d4e5f6"
const pplxManagedRepo = "ppl-ai/agi"
const diagnosticEvidenceID = "8f754d80-6f1e-44c8-a370-2f4752605d95"
const otherDiagnosticEvidenceID = "d5611749-2c73-436a-bca7-2dc9d10bc6df"

var pplxManagedWorkflowPatterns = []string{
	"terraform-delete-module",
	"terraform-just-*",
}

var pplxManagedRepoPatterns = []string{"ppl-ai/agi"}

func TestRunStepRunner_StructuredResultOffDoesNotExposePath(t *testing.T) {
	runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeOff)
	workingDir := t.TempDir()

	output, err := runner.Run(
		ctx,
		nil,
		`if [ -z "${ATLANTIS_STEP_RESULT_FILE+x}" ]; then printf 'legacy\n'; else printf 'exposed\n'; fi`,
		workingDir,
		nil,
		false,
		nil,
		nil,
	)

	Ok(t, err)
	Equals(t, "legacy\n", output)
	assertNoStructuredResultDirectories(t, workingDir)
}

func TestRunStepRunner_ShadowValidatesAndCleansResultWithoutChangingOutput(t *testing.T) {
	runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeShadow)
	workingDir := t.TempDir()
	command := fmt.Sprintf(
		`test ! -e "$%[1]s" && case "$%[1]s" in "$DIR"/.atlantis-step-result-*/result.json) ;; *) exit 74 ;; esac && printf '%%s' '%[2]s' > "$%[1]s" && printf 'legacy\n'`,
		runtime.StepResultFileEnvVar,
		`{"schema_version":1,"outcome":"success","changes":{"has_changes":false,"has_output_only_changes":false,"add":0,"change":0,"destroy":0,"import":0,"forget":0}}`,
	)

	output, err := runner.Run(
		ctx,
		nil,
		command,
		workingDir,
		map[string]string{runtime.StepResultFileEnvVar: "/tmp/caller-controlled-result.json"},
		false,
		nil,
		nil,
	)

	Ok(t, err)
	Equals(t, "legacy\n", output)
	assertNoStructuredResultDirectories(t, workingDir)
}

func TestRunStepRunner_AuthoritativeModesReturnValidatedResult(t *testing.T) {
	modes := []runtime.StructuredRunResultMode{
		runtime.StructuredRunResultModePrefer,
		runtime.StructuredRunResultModeRequired,
	}

	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			runner, ctx := newStructuredResultRunStepRunner(t, mode)
			workingDir := t.TempDir()
			command := fmt.Sprintf(
				`printf '%%s' '%s' > "$%s" && printf 'legacy\n'`,
				`{"schema_version":1,"outcome":"success","summary":"Terraform plan has changes.","changes":{"has_changes":true,"has_output_only_changes":false,"add":1,"change":0,"destroy":0,"import":0,"forget":0}}`,
				runtime.StepResultFileEnvVar,
			)

			result, err := runner.RunWithResult(
				ctx,
				nil,
				command,
				workingDir,
				nil,
				false,
				nil,
				nil,
			)

			Ok(t, err)
			Equals(t, "legacy\n", result.ConsoleOutput)
			Assert(t, result.StructuredResult != nil, "expected a validated structured result")
			Equals(t, runtime.StepResultOutcomeSuccess, result.StructuredResult.Result.Outcome)
			Equals(t, 1, result.StructuredResult.Result.Changes.Add)
			assertNoStructuredResultDirectories(t, workingDir)
		})
	}
}

func TestRunStepRunner_PreferFallsBackForMissingOrInvalidResult(t *testing.T) {
	tests := []struct {
		name    string
		command string
		status  string
	}{
		{
			name:    "missing",
			command: `printf 'legacy\n'`,
			status:  "missing",
		},
		{
			name: "invalid",
			command: fmt.Sprintf(
				`printf 'not-json' > "$%s" && printf 'legacy\n'`,
				runtime.StepResultFileEnvVar,
			),
			status: "invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModePrefer)
			scope := tally.NewTestScope("structured", nil)
			runner.StructuredRunResultObserver = runtime.StructuredRunResultObserver{Scope: scope}
			workingDir := t.TempDir()

			result, err := runner.RunWithResult(
				ctx,
				nil,
				test.command,
				workingDir,
				nil,
				false,
				nil,
				nil,
			)

			Ok(t, err)
			Equals(t, "legacy\n", result.ConsoleOutput)
			Assert(t, result.StructuredResult == nil, "expected legacy fallback")
			assertCounterNameContains(
				t,
				scope.Snapshot().Counters(),
				"artifact",
				"command=plan",
				"status="+test.status,
			)
			assertNoStructuredResultDirectories(t, workingDir)
		})
	}
}

func TestRunStepRunner_RequiredRejectsMissingOrInvalidResult(t *testing.T) {
	tests := []struct {
		name    string
		command string
		status  string
		err     string
	}{
		{
			name:    "missing",
			command: `printf 'legacy\n'`,
			status:  "missing",
			err:     "required structured run result is missing",
		},
		{
			name: "invalid",
			command: fmt.Sprintf(
				`printf 'not-json' > "$%s" && printf 'legacy\n'`,
				runtime.StepResultFileEnvVar,
			),
			status: "invalid",
			err:    "invalid required structured run result: decoding structured run result",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeRequired)
			scope := tally.NewTestScope("structured", nil)
			runner.StructuredRunResultObserver = runtime.StructuredRunResultObserver{Scope: scope}
			workingDir := t.TempDir()

			result, err := runner.RunWithResult(
				ctx,
				nil,
				test.command,
				workingDir,
				nil,
				false,
				nil,
				nil,
			)

			ErrContains(t, test.err, err)
			Equals(t, "legacy\n", result.ConsoleOutput)
			Assert(t, result.StructuredResult == nil, "did not expect a structured result")
			assertCounterNameContains(
				t,
				scope.Snapshot().Counters(),
				"artifact",
				"command=plan",
				"status="+test.status,
			)
			assertNoStructuredResultDirectories(t, workingDir)
		})
	}
}

func TestRunStepRunner_RequiredReturnsValidatedErrorResult(t *testing.T) {
	runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeRequired)
	workingDir := t.TempDir()
	command := fmt.Sprintf(
		`printf '%%s' '%s' > "$%s"; printf 'legacy failure\n'; exit 7`,
		`{"schema_version":1,"outcome":"error","diagnostic":{"code":"tool_failed","summary":"tool failed"}}`,
		runtime.StepResultFileEnvVar,
	)

	result, err := runner.RunWithResult(
		ctx,
		nil,
		command,
		workingDir,
		nil,
		false,
		nil,
		nil,
	)

	ErrContains(t, "exit status 7", err)
	Assert(t, result.StructuredResult != nil, "expected a validated structured error result")
	Equals(t, runtime.StepResultOutcomeError, result.StructuredResult.Result.Outcome)
	Equals(t, "tool failed", result.StructuredResult.Result.Diagnostic.Summary)
	assertNoStructuredResultDirectories(t, workingDir)
}

func TestRunStepRunner_RequiredBindsDiagnosticEvidenceToAtlantisJob(t *testing.T) {
	runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeRequired)
	ctx.JobID = diagnosticEvidenceID
	workingDir := t.TempDir()
	command := fmt.Sprintf(
		`test "$%s" = '%s'; printf '%%s' '%s' > "$%s"; exit 7`,
		runtime.StepJobIDEnvVar,
		diagnosticEvidenceID,
		`{"schema_version":1,"outcome":"error","diagnostic":{"code":"terraform_failed","summary":"Terraform failed.","evidence_id":"`+diagnosticEvidenceID+`"}}`,
		runtime.StepResultFileEnvVar,
	)

	result, err := runner.RunWithResult(
		ctx,
		nil,
		command,
		workingDir,
		map[string]string{runtime.StepJobIDEnvVar: otherDiagnosticEvidenceID},
		false,
		nil,
		nil,
	)

	ErrContains(t, "exit status 7", err)
	Assert(t, result.StructuredResult != nil, "expected a validated structured error result")
	Equals(t, diagnosticEvidenceID, result.StructuredResult.Result.Diagnostic.EvidenceID)
}

func TestRunStepRunner_RequiredRejectsEvidenceFromAnotherJob(t *testing.T) {
	runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeRequired)
	ctx.JobID = diagnosticEvidenceID
	workingDir := t.TempDir()
	command := fmt.Sprintf(
		`printf '%%s' '%s' > "$%s"; exit 7`,
		`{"schema_version":1,"outcome":"error","diagnostic":{"code":"terraform_failed","summary":"Terraform failed.","evidence_id":"`+otherDiagnosticEvidenceID+`"}}`,
		runtime.StepResultFileEnvVar,
	)

	result, err := runner.RunWithResult(
		ctx,
		nil,
		command,
		workingDir,
		nil,
		false,
		nil,
		nil,
	)

	ErrContains(t, "structured run result diagnostic evidence does not match the Atlantis job", err)
	Assert(t, result.StructuredResult == nil, "did not expect mismatched evidence")
}

func TestRunStepRunner_ShadowInvalidResultDoesNotChangeLegacyReturn(t *testing.T) {
	runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeShadow)
	logger := loggingmocks.NewMockSimpleLogging()
	When(logger.With(Any[any](), Any[any]())).ThenReturn(logger)
	ctx.Log = logger
	workingDir := t.TempDir()

	output, err := runner.Run(
		ctx,
		nil,
		fmt.Sprintf(`printf 'not-json' > "$%s" && printf 'legacy\n'`, runtime.StepResultFileEnvVar),
		workingDir,
		nil,
		false,
		nil,
		nil,
	)

	Ok(t, err)
	Equals(t, "legacy\n", output)
	logger.VerifyWasCalledOnce().Warn(
		Eq("invalid optional structured run result; legacy command result is unchanged: %s"),
		Any[any](),
	)
	assertNoStructuredResultDirectories(t, workingDir)
}

func TestRunStepRunner_ShadowDoesNotChangeLegacyError(t *testing.T) {
	runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeShadow)
	workingDir := t.TempDir()
	command := fmt.Sprintf(
		`printf '%%s' '%s' > "$%s"; printf 'legacy failure\n'; exit 7`,
		`{"schema_version":1,"outcome":"error","diagnostic":{"code":"tool_failed","summary":"tool failed"}}`,
		runtime.StepResultFileEnvVar,
	)

	output, err := runner.Run(
		ctx,
		nil,
		command,
		workingDir,
		nil,
		false,
		nil,
		nil,
	)

	Equals(t, "", output)
	ErrContains(t, "exit status 7", err)
	ErrContains(t, "legacy failure", err)
	assertNoStructuredResultDirectories(t, workingDir)
}

func TestRunStepRunner_ShadowRecordsOnlyLowCardinalityComparisonMetrics(t *testing.T) {
	runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeShadow)
	scope := tally.NewTestScope("structured", nil)
	runner.StructuredRunResultObserver = runtime.StructuredRunResultObserver{Scope: scope}
	ctx.ProjectName = "sensitive-project"
	ctx.Pull.Num = 12345
	ctx.BaseRepo.FullName = pplxManagedRepo
	workingDir := t.TempDir()
	command := fmt.Sprintf(
		`printf 'safe review metadata' > "$DIR/review.txt"; printf '%%s' '%s' > "$%s"; printf 'Plan: 1 to add, 0 to change, 0 to destroy.\n'`,
		`{"schema_version":1,"outcome":"success","changes":{"has_changes":true,"has_output_only_changes":false,"add":1,"change":0,"destroy":0,"import":0,"forget":0},"review":{"detail_mode":"inline","inline_detail_path":"review.txt"}}`,
		runtime.StepResultFileEnvVar,
	)

	output, err := runner.Run(
		ctx,
		nil,
		command,
		workingDir,
		nil,
		false,
		nil,
		nil,
	)

	Ok(t, err)
	Equals(t, "Plan: 1 to add, 0 to change, 0 to destroy.\n", output)
	counters := scope.Snapshot().Counters()
	Equals(t, 2, len(counters))
	for name, counter := range counters {
		Equals(t, int64(1), counter.Value())
		if strings.Contains(name, "sensitive-project") ||
			strings.Contains(name, pplxManagedRepo) ||
			strings.Contains(name, "12345") {
			t.Fatalf("metric contains a high-cardinality identity tag: %s", name)
		}
	}
	assertCounterNameContains(t, counters, "artifact", "command=plan", "status=valid")
	assertCounterNameContains(t, counters, "comparison", "class=match", "command=plan")
	assertNoStructuredResultDirectories(t, workingDir)
}

func TestRunStepRunner_ShadowRecordsMissingAndInvalidArtifacts(t *testing.T) {
	tests := []struct {
		name    string
		command string
		status  string
	}{
		{
			name:    "missing",
			command: `printf 'legacy\n'`,
			status:  "missing",
		},
		{
			name: "invalid",
			command: fmt.Sprintf(
				`printf 'not-json' > "$%s"; printf 'legacy\n'`,
				runtime.StepResultFileEnvVar,
			),
			status: "invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeShadow)
			scope := tally.NewTestScope("structured", nil)
			runner.StructuredRunResultObserver = runtime.StructuredRunResultObserver{Scope: scope}
			workingDir := t.TempDir()

			output, err := runner.Run(
				ctx,
				nil,
				test.command,
				workingDir,
				nil,
				false,
				nil,
				nil,
			)

			Ok(t, err)
			Equals(t, "legacy\n", output)
			counters := scope.Snapshot().Counters()
			Equals(t, 1, len(counters))
			assertCounterNameContains(
				t,
				counters,
				"artifact",
				"command=plan",
				"status="+test.status,
			)
			assertNoStructuredResultDirectories(t, workingDir)
		})
	}
}

func TestRunStepRunner_ShadowDoesNotExposePathOutsideScope(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*runtime.RunStepRunner, *command.ProjectContext)
	}{
		{
			name: "workflow is not allowlisted",
			configure: func(_ *runtime.RunStepRunner, ctx *command.ProjectContext) {
				ctx.WorkflowName = "repo-defined-workflow"
			},
		},
		{
			name: "workflow allowlist is empty",
			configure: func(runner *runtime.RunStepRunner, _ *command.ProjectContext) {
				runner.StructuredRunResultWorkflowPatterns = nil
			},
		},
		{
			name: "repository is not allowlisted",
			configure: func(_ *runtime.RunStepRunner, ctx *command.ProjectContext) {
				ctx.BaseRepo.FullName = "another-org/repo"
			},
		},
		{
			name: "repository allowlist is empty",
			configure: func(runner *runtime.RunStepRunner, _ *command.ProjectContext) {
				runner.StructuredRunResultRepoPatterns = nil
			},
		},
		{
			name: "command is not plan or apply",
			configure: func(_ *runtime.RunStepRunner, ctx *command.ProjectContext) {
				ctx.CommandName = command.PolicyCheck
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeShadow)
			test.configure(&runner, &ctx)
			workingDir := t.TempDir()

			output, err := runner.Run(
				ctx,
				nil,
				`if [ -z "${ATLANTIS_STEP_RESULT_FILE+x}" ]; then printf 'legacy\n'; else printf 'exposed\n'; fi`,
				workingDir,
				nil,
				false,
				nil,
				nil,
			)

			Ok(t, err)
			Equals(t, "legacy\n", output)
			assertNoStructuredResultDirectories(t, workingDir)
		})
	}
}

func TestRunStepRunner_RequiredDoesNotChangeApplyOrOutOfScopeRuns(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*runtime.RunStepRunner, *command.ProjectContext)
	}{
		{
			name: "apply",
			configure: func(_ *runtime.RunStepRunner, ctx *command.ProjectContext) {
				ctx.CommandName = command.Apply
			},
		},
		{
			name: "policy check",
			configure: func(_ *runtime.RunStepRunner, ctx *command.ProjectContext) {
				ctx.CommandName = command.PolicyCheck
			},
		},
		{
			name: "repository is not allowlisted",
			configure: func(_ *runtime.RunStepRunner, ctx *command.ProjectContext) {
				ctx.BaseRepo.FullName = "another-org/repo"
			},
		},
		{
			name: "workflow is not allowlisted",
			configure: func(_ *runtime.RunStepRunner, ctx *command.ProjectContext) {
				ctx.WorkflowName = "repo-defined-workflow"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeRequired)
			test.configure(&runner, &ctx)
			workingDir := t.TempDir()

			output, err := runner.Run(
				ctx,
				nil,
				`if [ -z "${ATLANTIS_STEP_RESULT_FILE+x}" ]; then printf 'legacy\n'; else printf 'exposed\n'; fi`,
				workingDir,
				nil,
				false,
				nil,
				nil,
			)

			Ok(t, err)
			Equals(t, "legacy\n", output)
			assertNoStructuredResultDirectories(t, workingDir)
		})
	}
}

func TestRunStepRunner_ApplyModeIsIndependentFromRequiredPlanMode(t *testing.T) {
	runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeRequired)
	runner.StructuredApplyResultsMode = runtime.StructuredRunResultModeShadow
	ctx.CommandName = command.Apply
	workingDir := t.TempDir()
	applyResult := `{"schema_version":1,"outcome":"success","summary":"Terraform apply completed with changes.","changes":{"has_changes":true,"has_output_only_changes":false,"add":1,"change":0,"destroy":0,"import":0,"forget":0}}`
	command := fmt.Sprintf(
		`test "$%s" = shadow && printf '%%s' '%s' > "$%s" && printf 'legacy apply\n'`,
		runtime.StepResultModeEnvVar,
		applyResult,
		runtime.StepResultFileEnvVar,
	)

	result, err := runner.RunWithResult(
		ctx,
		nil,
		command,
		workingDir,
		nil,
		false,
		nil,
		nil,
	)

	Ok(t, err)
	Equals(t, "legacy apply\n", result.ConsoleOutput)
	Assert(t, result.StructuredResult == nil, "shadow apply result must not become authoritative")
	assertNoStructuredResultDirectories(t, workingDir)
}

func TestRunStepRunner_ZeroValuePlanModeDoesNotExposePath(t *testing.T) {
	runner, ctx := newStructuredResultRunStepRunner(t, "")
	workingDir := t.TempDir()

	output, err := runner.Run(
		ctx,
		nil,
		`if [ -z "${ATLANTIS_STEP_RESULT_FILE+x}" ]; then printf 'legacy\n'; else printf 'exposed\n'; fi`,
		workingDir,
		nil,
		false,
		nil,
		nil,
	)

	Ok(t, err)
	Equals(t, "legacy\n", output)
	assertNoStructuredResultDirectories(t, workingDir)
}

func TestRunStepRunner_PreferApplyReturnsValidatedResult(t *testing.T) {
	runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeRequired)
	runner.StructuredApplyResultsMode = runtime.StructuredRunResultModePrefer
	ctx.CommandName = command.Apply
	workingDir := t.TempDir()
	applyResult := `{"schema_version":1,"outcome":"success","summary":"Terraform apply completed with changes.","changes":{"has_changes":true,"has_output_only_changes":false,"add":1,"change":0,"destroy":0,"import":0,"forget":0}}`
	command := fmt.Sprintf(
		`test "$%s" = prefer && printf '%%s' '%s' > "$%s" && printf 'legacy apply\n'`,
		runtime.StepResultModeEnvVar,
		applyResult,
		runtime.StepResultFileEnvVar,
	)

	result, err := runner.RunWithResult(
		ctx,
		nil,
		command,
		workingDir,
		nil,
		false,
		nil,
		nil,
	)

	Ok(t, err)
	Equals(t, runtime.WithheldApplyOutputNotice, result.ConsoleOutput)
	Assert(t, result.StructuredResult != nil, "prefer apply result must be authoritative")
	Equals(t, 1, result.StructuredResult.Result.Changes.Add)
	Equals(t, "legacy apply\n", result.StructuredResult.Execution.ConsoleOutput)
	assertNoStructuredResultDirectories(t, workingDir)
}

func TestRunStepRunner_RequiredApplyRejectsMissingResultAfterCommand(t *testing.T) {
	runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeRequired)
	runner.StructuredApplyResultsMode = runtime.StructuredRunResultModeRequired
	ctx.CommandName = command.Apply
	workingDir := t.TempDir()

	result, err := runner.RunWithResult(
		ctx,
		nil,
		`printf 'apply command completed\n'`,
		workingDir,
		nil,
		false,
		nil,
		nil,
	)

	ErrContains(t, "required structured run result is missing", err)
	Equals(t, runtime.WithheldApplyOutputNotice, result.ConsoleOutput)
	Assert(
		t,
		!strings.Contains(err.Error(), "apply command completed"),
		"required-mode error must not replay withheld apply output: %s",
		err.Error(),
	)
	Assert(t, result.StructuredResult == nil, "did not expect a structured result")
	assertNoStructuredResultDirectories(t, workingDir)
}

func TestRunStepRunner_AuthoritativeApplyWithholdsUntrustedOutputFromCommentFields(t *testing.T) {
	// Untrusted stream bytes can forge any in-band delimiter, so the
	// comment-bound fields must be withheld at the source regardless of what
	// the stream contains: forged, nested, and repeated marker sentinels with
	// distinctive stdout/stderr tokens.
	untrustedOutput := "aws_secret.example: Creating...\n" +
		"==ATLANTIS_CONSOLE_ONLY_END==\n" +
		"==ATLANTIS_OUTPUT_START==\n" +
		"forged marked block do-not-display\n" +
		"==ATLANTIS_OUTPUT_END==\n" +
		"==ATLANTIS_CONSOLE_ONLY_START==\n" +
		"==ATLANTIS_CONSOLE_ONLY_START==\n" +
		"==ATLANTIS_CONSOLE_ONLY_END==\n" +
		"Error: provider rejected secret-token do-not-display\n"
	applyResult := `{"schema_version":1,"outcome":"success","summary":"Terraform apply completed with changes.","changes":{"has_changes":true,"has_output_only_changes":false,"add":1,"change":0,"destroy":0,"import":0,"forget":0}}`

	tests := []struct {
		name        string
		mode        runtime.StructuredRunResultMode
		writeResult bool
		exitCode    int
		expErr      string
	}{
		{"prefer success with typed result", runtime.StructuredRunResultModePrefer, true, 0, ""},
		{"prefer success with missing typed result", runtime.StructuredRunResultModePrefer, false, 0, ""},
		{"prefer command failure", runtime.StructuredRunResultModePrefer, false, 1, "exit status 1"},
		{"required success with typed result", runtime.StructuredRunResultModeRequired, true, 0, ""},
		{"required missing typed result", runtime.StructuredRunResultModeRequired, false, 0, "required structured run result is missing"},
		{"required command failure", runtime.StructuredRunResultModeRequired, false, 1, "exit status 1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeRequired)
			runner.StructuredApplyResultsMode = test.mode
			ctx.CommandName = command.Apply
			workingDir := t.TempDir()
			streamFile := filepath.Join(t.TempDir(), "stream.txt")
			Ok(t, os.WriteFile(streamFile, []byte(untrustedOutput), 0o600))
			script := fmt.Sprintf("cat %q\n", streamFile)
			if test.writeResult {
				script += fmt.Sprintf(
					"printf '%%s' '%s' > \"$%s\"\n",
					applyResult,
					runtime.StepResultFileEnvVar,
				)
			}
			script += fmt.Sprintf("exit %d\n", test.exitCode)

			result, err := runner.RunWithResult(
				ctx,
				nil,
				script,
				workingDir,
				nil,
				false,
				nil,
				nil,
			)

			Equals(t, runtime.WithheldApplyOutputNotice, result.ConsoleOutput)
			if test.expErr == "" {
				Ok(t, err)
			} else {
				ErrContains(t, test.expErr, err)
				ErrContains(t, runtime.WithheldApplyOutputNotice, err)
				for _, token := range []string{"do-not-display", "aws_secret.example", "secret-token"} {
					Assert(
						t,
						!strings.Contains(err.Error(), token),
						"comment-bound error must not contain untrusted token %q: %s",
						token,
						err.Error(),
					)
				}
			}
			if result.StructuredResult != nil {
				Equals(t, untrustedOutput, result.StructuredResult.Execution.ConsoleOutput)
			}
			assertNoStructuredResultDirectories(t, workingDir)
		})
	}
}

func TestRunStepRunner_RequiredFailsBeforeCommandWhenResultPathCannotBeAllocated(t *testing.T) {
	runner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeRequired)
	scope := tally.NewTestScope("structured", nil)
	runner.StructuredRunResultObserver = runtime.StructuredRunResultObserver{Scope: scope}
	rootDir := t.TempDir()
	workingDir := filepath.Join(rootDir, "working")
	Ok(t, os.Mkdir(workingDir, 0o700))
	Ok(t, os.Chmod(workingDir, 0o500))
	t.Cleanup(func() {
		Ok(t, os.Chmod(workingDir, 0o700))
	})
	commandMarker := filepath.Join(rootDir, "command-executed")

	_, err := runner.RunWithResult(
		ctx,
		nil,
		fmt.Sprintf("touch %q", commandMarker),
		workingDir,
		nil,
		false,
		nil,
		nil,
	)

	ErrContains(t, "allocating required structured run result path", err)
	_, markerErr := os.Stat(commandMarker)
	Assert(t, os.IsNotExist(markerErr), "custom command executed after result-path allocation failed")
	assertCounterNameContains(
		t,
		scope.Snapshot().Counters(),
		"artifact",
		"command=plan",
		"status=unavailable",
	)
	assertNoStructuredResultDirectories(t, workingDir)
}

func TestEnvStepRunner_ShadowDoesNotExposeStructuredResultPath(t *testing.T) {
	runStepRunner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeShadow)
	envStepRunner := runtime.EnvStepRunner{RunStepRunner: &runStepRunner}
	workingDir := t.TempDir()

	value, err := envStepRunner.Run(
		ctx,
		nil,
		`if [ -z "${ATLANTIS_STEP_RESULT_FILE+x}" ]; then printf 'value\n'; else printf 'exposed\n'; fi`,
		"",
		workingDir,
		nil,
	)

	Ok(t, err)
	Equals(t, "value", value)
	assertNoStructuredResultDirectories(t, workingDir)
}

func TestMultiEnvStepRunner_ShadowDoesNotExposeStructuredResultPath(t *testing.T) {
	runStepRunner, ctx := newStructuredResultRunStepRunner(t, runtime.StructuredRunResultModeShadow)
	multiEnvStepRunner := runtime.MultiEnvStepRunner{RunStepRunner: &runStepRunner}
	workingDir := t.TempDir()
	envs := make(map[string]string)

	_, err := multiEnvStepRunner.Run(
		ctx,
		nil,
		`if [ -z "${ATLANTIS_STEP_RESULT_FILE+x}" ]; then printf 'KEY=value\n'; else printf 'KEY=exposed\n'; fi`,
		workingDir,
		envs,
		nil,
	)

	Ok(t, err)
	Equals(t, map[string]string{"KEY": "value"}, envs)
	assertNoStructuredResultDirectories(t, workingDir)
}

func newStructuredResultRunStepRunner(
	t *testing.T,
	mode runtime.StructuredRunResultMode,
) (runtime.RunStepRunner, command.ProjectContext) {
	t.Helper()
	RegisterMockTestingT(t)
	terraformExecutor := tfclientmocks.NewMockClient()
	When(terraformExecutor.EnsureVersion(
		Any[logging.SimpleLogging](),
		Any[tf.Distribution](),
		Any[*version.Version](),
	)).ThenReturn(nil)
	defaultVersion, err := version.NewVersion("1.0.0")
	Ok(t, err)

	return runtime.RunStepRunner{
			TerraformExecutor:                   terraformExecutor,
			DefaultTFDistribution:               tf.NewDistributionTerraformWithDownloader(mocks.NewMockDownloader()),
			DefaultTFVersion:                    defaultVersion,
			TerraformBinDir:                     "/bin",
			ProjectCmdOutputHandler:             jobmocks.NewMockProjectCommandOutputHandler(),
			StructuredRunResultsMode:            mode,
			StructuredRunResultRepoPatterns:     pplxManagedRepoPatterns,
			StructuredRunResultWorkflowPatterns: pplxManagedWorkflowPatterns,
		}, command.ProjectContext{
			CommandName:  command.Plan,
			WorkflowName: pplxManagedWorkflow,
			BaseRepo:     models.Repo{FullName: pplxManagedRepo},
			JobID:        diagnosticEvidenceID,
			Log:          logging.NewNoopLogger(t),
		}
}

func assertNoStructuredResultDirectories(t *testing.T, workingDir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(workingDir, ".atlantis-step-result-*"))
	Ok(t, err)
	Equals(t, []string(nil), matches)
}

func assertCounterNameContains(
	t *testing.T,
	counters map[string]tally.CounterSnapshot,
	parts ...string,
) {
	t.Helper()
	for name := range counters {
		matches := true
		for _, part := range parts {
			if !strings.Contains(name, part) {
				matches = false
				break
			}
		}
		if matches {
			return
		}
	}
	t.Fatalf("no counter name contains %v: %v", parts, counters)
}
