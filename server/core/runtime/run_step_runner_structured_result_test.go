// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package runtime_test

import (
	"fmt"
	"path/filepath"
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
)

const pplxManagedWorkflow = "terraform-just-a1b2c3d4e5f6"
const pplxManagedRepo = "ppl-ai/agi"

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
			Log:          logging.NewNoopLogger(t),
		}
}

func assertNoStructuredResultDirectories(t *testing.T, workingDir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(workingDir, ".atlantis-step-result-*"))
	Ok(t, err)
	Equals(t, []string(nil), matches)
}
