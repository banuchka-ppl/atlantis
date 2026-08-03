// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package runtime_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runatlantis/atlantis/server/core/runtime"
	. "github.com/runatlantis/atlantis/testing"
)

const noOpStepResultJSON = `{
	"schema_version": 1,
	"outcome": "success",
	"summary": "No changes.",
	"changes": {
		"has_changes": false,
		"has_output_only_changes": false,
		"add": 0,
		"change": 0,
		"destroy": 0,
		"import": 0,
		"forget": 0
	}
}`

func TestParseStructuredRunResultMode(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected runtime.StructuredRunResultMode
		err      string
	}{
		{
			name:     "empty is off for zero-value callers",
			value:    "",
			expected: runtime.StructuredRunResultModeOff,
		},
		{
			name:     "off",
			value:    "off",
			expected: runtime.StructuredRunResultModeOff,
		},
		{
			name:     "shadow",
			value:    "shadow",
			expected: runtime.StructuredRunResultModeShadow,
		},
		{
			name:     "prefer",
			value:    "prefer",
			expected: runtime.StructuredRunResultModePrefer,
		},
		{
			name:     "required",
			value:    "required",
			expected: runtime.StructuredRunResultModeRequired,
		},
		{
			name:  "unknown mode",
			value: "unknown",
			err:   `invalid structured run result mode "unknown": must be one of [off shadow prefer required]`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := runtime.ParseStructuredRunResultMode(test.value)
			if test.err != "" {
				ErrEquals(t, test.err, err)
				return
			}
			Ok(t, err)
			Equals(t, test.expected, actual)
		})
	}
}

func TestParseStructuredRunResultWorkflowPatterns(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected []string
		err      string
	}{
		{
			name: "empty disables all workflows",
		},
		{
			name:     "comma-separated patterns",
			value:    " terraform-just-* , terraform-delete-module ",
			expected: []string{"terraform-just-*", "terraform-delete-module"},
		},
		{
			name:  "invalid glob",
			value: "terraform-just-[",
			err:   `invalid structured run result workflow pattern "terraform-just-[": syntax error in pattern`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := runtime.ParseStructuredRunResultWorkflowPatterns(test.value)
			if test.err != "" {
				ErrEquals(t, test.err, err)
				return
			}
			Ok(t, err)
			Equals(t, test.expected, actual)
		})
	}
}

func TestParseStructuredRunResultRepoPatterns(t *testing.T) {
	patterns, err := runtime.ParseStructuredRunResultRepoPatterns(
		" ppl-ai/agi , ppl-ai/terraform-* ",
	)

	Ok(t, err)
	Equals(t, []string{"ppl-ai/agi", "ppl-ai/terraform-*"}, patterns)

	_, err = runtime.ParseStructuredRunResultRepoPatterns("ppl-ai/[")
	ErrEquals(
		t,
		`invalid structured run result repository pattern "ppl-ai/[": syntax error in pattern`,
		err,
	)
}

func TestStructuredRunResultCompleterCompletesNoOpPlan(t *testing.T) {
	workingDir := t.TempDir()
	resultPath := filepath.Join(workingDir, "step-result.json")
	err := os.WriteFile(resultPath, []byte(noOpStepResultJSON), 0o600)
	Ok(t, err)

	execution := runtime.RunExecution{
		ConsoleOutput: "terraform plan output\n",
	}
	completion, err := (runtime.StructuredRunResultCompleter{}).CompleteRun(workingDir, resultPath, execution)
	Ok(t, err)

	Equals(t, runtime.CompletedRun{
		Execution: execution,
		Result: runtime.StepResultV1{
			SchemaVersion: 1,
			Outcome:       runtime.StepResultOutcomeSuccess,
			Summary:       "No changes.",
			Changes: &runtime.StepChangeSummary{
				HasChanges:           false,
				HasOutputOnlyChanges: false,
			},
		},
	}, completion)
}

func TestStructuredRunResultCompleterRejectsResultOutsideWorkingDirectory(t *testing.T) {
	workingDir := t.TempDir()
	resultPath := filepath.Join(t.TempDir(), "step-result.json")
	err := os.WriteFile(resultPath, []byte(noOpStepResultJSON), 0o600)
	Ok(t, err)

	_, err = (runtime.StructuredRunResultCompleter{}).CompleteRun(
		workingDir,
		resultPath,
		runtime.RunExecution{},
	)

	ErrContains(t, "structured run result path is outside the working directory", err)
}

func TestStructuredRunResultCompleterRejectsSymlink(t *testing.T) {
	workingDir := t.TempDir()
	targetPath := filepath.Join(workingDir, "target.json")
	err := os.WriteFile(targetPath, []byte(noOpStepResultJSON), 0o600)
	Ok(t, err)
	resultPath := filepath.Join(workingDir, "step-result.json")
	err = os.Symlink(targetPath, resultPath)
	Ok(t, err)

	_, err = (runtime.StructuredRunResultCompleter{}).CompleteRun(
		workingDir,
		resultPath,
		runtime.RunExecution{},
	)

	ErrContains(t, "structured run result must be a regular file, not a symlink", err)
}

func TestStructuredRunResultCompleterRejectsSymlinkedParent(t *testing.T) {
	workingDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideResultPath := filepath.Join(outsideDir, "step-result.json")
	err := os.WriteFile(outsideResultPath, []byte(noOpStepResultJSON), 0o600)
	Ok(t, err)
	symlinkedDir := filepath.Join(workingDir, "result-dir")
	err = os.Symlink(outsideDir, symlinkedDir)
	Ok(t, err)

	_, err = (runtime.StructuredRunResultCompleter{}).CompleteRun(
		workingDir,
		filepath.Join(symlinkedDir, "step-result.json"),
		runtime.RunExecution{},
	)

	ErrContains(t, "inspecting structured run result", err)
}

func TestStructuredRunResultCompleterRejectsInvalidSchema(t *testing.T) {
	cases := []struct {
		name     string
		result   string
		expected string
	}{
		{
			name: "unsupported version",
			result: `{
				"schema_version": 2,
				"outcome": "success",
				"changes": {
					"has_changes": false,
					"has_output_only_changes": false,
					"add": 0,
					"change": 0,
					"destroy": 0,
					"import": 0,
					"forget": 0
				}
			}`,
			expected: "unsupported structured run result schema version 2",
		},
		{
			name: "unknown field",
			result: `{
				"schema_version": 1,
				"outcome": "success",
				"unknown": true
			}`,
			expected: `decoding structured run result: json: unknown field "unknown"`,
		},
		{
			name:     "trailing JSON",
			result:   noOpStepResultJSON + `{}`,
			expected: "structured run result contains more than one JSON value",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			workingDir := t.TempDir()
			resultPath := filepath.Join(workingDir, "step-result.json")
			err := os.WriteFile(resultPath, []byte(c.result), 0o600)
			Ok(t, err)

			_, err = (runtime.StructuredRunResultCompleter{}).CompleteRun(
				workingDir,
				resultPath,
				runtime.RunExecution{},
			)

			ErrContains(t, c.expected, err)
		})
	}
}

func TestStructuredRunResultCompleterRejectsOversizedResult(t *testing.T) {
	workingDir := t.TempDir()
	resultPath := filepath.Join(workingDir, "step-result.json")
	err := os.WriteFile(
		resultPath,
		[]byte(strings.Repeat("x", runtime.MaxStepResultBytes+1)),
		0o600,
	)
	Ok(t, err)

	_, err = (runtime.StructuredRunResultCompleter{}).CompleteRun(
		workingDir,
		resultPath,
		runtime.RunExecution{},
	)

	ErrContains(t, "structured run result exceeds maximum size", err)
}

func TestStructuredRunResultCompleterRejectsInconsistentOutcome(t *testing.T) {
	commandError := errors.New("exit status 1")
	cases := []struct {
		name      string
		result    string
		execution runtime.RunExecution
		expected  string
	}{
		{
			name: "unknown outcome",
			result: `{
				"schema_version": 1,
				"outcome": "unknown"
			}`,
			expected: `invalid structured run result outcome "unknown"`,
		},
		{
			name:      "success with command error",
			result:    noOpStepResultJSON,
			execution: runtime.RunExecution{Err: commandError},
			expected:  `structured run result outcome "success" conflicts with command failure`,
		},
		{
			name: "error with successful command",
			result: `{
				"schema_version": 1,
				"outcome": "error",
				"diagnostic": {
					"code": "terraform_failed",
					"summary": "Terraform failed."
				}
			}`,
			expected: `structured run result outcome "error" conflicts with command success`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			workingDir := t.TempDir()
			resultPath := filepath.Join(workingDir, "step-result.json")
			err := os.WriteFile(resultPath, []byte(c.result), 0o600)
			Ok(t, err)

			_, err = (runtime.StructuredRunResultCompleter{}).CompleteRun(
				workingDir,
				resultPath,
				c.execution,
			)

			ErrContains(t, c.expected, err)
		})
	}
}

func TestStructuredRunResultCompleterRejectsInvalidResultSemantics(t *testing.T) {
	commandError := errors.New("exit status 1")
	cases := []struct {
		name      string
		result    string
		execution runtime.RunExecution
		expected  string
	}{
		{
			name: "successful result without changes",
			result: `{
				"schema_version": 1,
				"outcome": "success"
			}`,
			expected: "changes are required for a successful structured run result",
		},
		{
			name: "error result without diagnostic",
			result: `{
				"schema_version": 1,
				"outcome": "error"
			}`,
			execution: runtime.RunExecution{Err: commandError},
			expected:  "diagnostic is required for an errored structured run result",
		},
		{
			name: "negative change count",
			result: `{
				"schema_version": 1,
				"outcome": "success",
				"changes": {
					"has_changes": true,
					"has_output_only_changes": false,
					"add": -1,
					"change": 0,
					"destroy": 0,
					"import": 0,
					"forget": 0
				}
			}`,
			expected: "structured run result change counts must not be negative",
		},
		{
			name: "counts contradict no changes",
			result: `{
				"schema_version": 1,
				"outcome": "success",
				"changes": {
					"has_changes": false,
					"has_output_only_changes": false,
					"add": 1,
					"change": 0,
					"destroy": 0,
					"import": 0,
					"forget": 0
				}
			}`,
			expected: "structured run result has change counts but has_changes is false",
		},
		{
			name: "output-only contradicts no changes",
			result: `{
				"schema_version": 1,
				"outcome": "success",
				"changes": {
					"has_changes": false,
					"has_output_only_changes": true,
					"add": 0,
					"change": 0,
					"destroy": 0,
					"import": 0,
					"forget": 0
				}
			}`,
			expected: "structured run result has_output_only_changes requires has_changes",
		},
		{
			name: "output-only includes resource counts",
			result: `{
				"schema_version": 1,
				"outcome": "success",
				"changes": {
					"has_changes": true,
					"has_output_only_changes": true,
					"add": 1,
					"change": 0,
					"destroy": 0,
					"import": 0,
					"forget": 0
				}
			}`,
			expected: "structured run result output-only changes must not include resource counts",
		},
		{
			name: "changes without counts or output-only changes",
			result: `{
				"schema_version": 1,
				"outcome": "success",
				"changes": {
					"has_changes": true,
					"has_output_only_changes": false,
					"add": 0,
					"change": 0,
					"destroy": 0,
					"import": 0,
					"forget": 0
				}
			}`,
			expected: "structured run result has_changes requires a change count or output-only changes",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			workingDir := t.TempDir()
			resultPath := filepath.Join(workingDir, "step-result.json")
			err := os.WriteFile(resultPath, []byte(c.result), 0o600)
			Ok(t, err)

			_, err = (runtime.StructuredRunResultCompleter{}).CompleteRun(
				workingDir,
				resultPath,
				c.execution,
			)

			ErrContains(t, c.expected, err)
		})
	}
}

func TestStructuredRunResultCompleterCompletesChangedPlan(t *testing.T) {
	workingDir := t.TempDir()
	err := os.Mkdir(filepath.Join(workingDir, ".atlantis"), 0o700)
	Ok(t, err)
	err = os.WriteFile(
		filepath.Join(workingDir, ".atlantis", "plan-review.txt"),
		[]byte("safe review metadata"),
		0o600,
	)
	Ok(t, err)
	resultPath := filepath.Join(workingDir, "step-result.json")
	err = os.WriteFile(resultPath, []byte(`{
		"schema_version": 1,
		"outcome": "success",
		"summary": "Plan: 2 to add, 1 to change, 0 to destroy.",
		"changes": {
			"has_changes": true,
			"has_output_only_changes": false,
			"add": 2,
			"change": 1,
			"destroy": 0,
			"import": 0,
			"forget": 0
		},
		"review": {
			"detail_mode": "inline",
			"inline_detail_path": ".atlantis/plan-review.txt",
			"details_url": "https://example.com/plan"
		}
	}`), 0o600)
	Ok(t, err)

	execution := runtime.RunExecution{ConsoleOutput: "terraform plan output\n"}
	completion, err := (runtime.StructuredRunResultCompleter{}).CompleteRun(
		workingDir,
		resultPath,
		execution,
	)
	Ok(t, err)

	Equals(t, runtime.CompletedRun{
		Execution:    execution,
		ReviewDetail: "safe review metadata",
		Result: runtime.StepResultV1{
			SchemaVersion: 1,
			Outcome:       runtime.StepResultOutcomeSuccess,
			Summary:       "Plan: 2 to add, 1 to change, 0 to destroy.",
			Changes: &runtime.StepChangeSummary{
				HasChanges: true,
				Add:        2,
				Change:     1,
			},
			Review: &runtime.StepReview{
				DetailMode:       runtime.StepReviewDetailModeInline,
				InlineDetailPath: ".atlantis/plan-review.txt",
				DetailsURL:       "https://example.com/plan",
			},
		},
	}, completion)
}

func TestStructuredRunResultCompleterCompletesErroredRun(t *testing.T) {
	workingDir := t.TempDir()
	err := os.Mkdir(filepath.Join(workingDir, ".atlantis"), 0o700)
	Ok(t, err)
	err = os.WriteFile(
		filepath.Join(workingDir, ".atlantis", "plan-error.txt"),
		[]byte("safe diagnostic metadata"),
		0o600,
	)
	Ok(t, err)
	resultPath := filepath.Join(workingDir, "step-result.json")
	err = os.WriteFile(resultPath, []byte(`{
		"schema_version": 1,
		"outcome": "error",
		"summary": "Terraform plan failed.",
		"diagnostic": {
			"code": "terraform_failed",
			"summary": "Invalid provider configuration.",
			"detail_path": ".atlantis/plan-error.txt"
		}
	}`), 0o600)
	Ok(t, err)

	commandError := errors.New("exit status 1")
	execution := runtime.RunExecution{
		ConsoleOutput: "terraform error output\n",
		Err:           commandError,
	}
	completion, err := (runtime.StructuredRunResultCompleter{}).CompleteRun(
		workingDir,
		resultPath,
		execution,
	)
	Ok(t, err)

	Equals(t, runtime.CompletedRun{
		DiagnosticDetail: "safe diagnostic metadata",
		Execution:        execution,
		Result: runtime.StepResultV1{
			SchemaVersion: 1,
			Outcome:       runtime.StepResultOutcomeError,
			Summary:       "Terraform plan failed.",
			Diagnostic: &runtime.StepDiagnostic{
				Code:       "terraform_failed",
				Summary:    "Invalid provider configuration.",
				DetailPath: ".atlantis/plan-error.txt",
			},
		},
	}, completion)
}

func TestStructuredRunResultCompleterRejectsInvalidReferencedFiles(t *testing.T) {
	commandError := errors.New("exit status 1")
	tests := []struct {
		name      string
		result    string
		execution runtime.RunExecution
		setup     func(t *testing.T, workingDir string)
		expected  string
	}{
		{
			name: "missing review detail",
			result: changedStepResultJSON(`{
				"detail_mode": "inline",
				"inline_detail_path": "missing-review.txt"
			}`),
			expected: "inspecting structured run result review detail",
		},
		{
			name: "review detail is a directory",
			result: changedStepResultJSON(`{
				"detail_mode": "inline",
				"inline_detail_path": "review"
			}`),
			setup: func(t *testing.T, workingDir string) {
				t.Helper()
				Ok(t, os.Mkdir(filepath.Join(workingDir, "review"), 0o700))
			},
			expected: "structured run result review detail must be a regular file",
		},
		{
			name: "review detail is a symlink",
			result: changedStepResultJSON(`{
				"detail_mode": "inline",
				"inline_detail_path": "review.txt"
			}`),
			setup: func(t *testing.T, workingDir string) {
				t.Helper()
				target := filepath.Join(workingDir, "review-target.txt")
				Ok(t, os.WriteFile(target, []byte("review"), 0o600))
				Ok(t, os.Symlink(target, filepath.Join(workingDir, "review.txt")))
			},
			expected: "structured run result review detail must be a regular file, not a symlink",
		},
		{
			name: "review detail parent escapes working directory",
			result: changedStepResultJSON(`{
				"detail_mode": "inline",
				"inline_detail_path": "details/review.txt"
			}`),
			setup: func(t *testing.T, workingDir string) {
				t.Helper()
				outsideDir := t.TempDir()
				Ok(t, os.WriteFile(
					filepath.Join(outsideDir, "review.txt"),
					[]byte("review"),
					0o600,
				))
				Ok(t, os.Symlink(outsideDir, filepath.Join(workingDir, "details")))
			},
			expected: "inspecting structured run result review detail",
		},
		{
			name: "review detail exceeds bound",
			result: changedStepResultJSON(`{
				"detail_mode": "inline",
				"inline_detail_path": "review.txt"
			}`),
			setup: func(t *testing.T, workingDir string) {
				t.Helper()
				Ok(t, os.WriteFile(
					filepath.Join(workingDir, "review.txt"),
					[]byte(strings.Repeat("x", runtime.MaxStepResultDetailBytes+1)),
					0o600,
				))
			},
			expected: "structured run result review detail exceeds maximum size",
		},
		{
			name: "missing diagnostic detail",
			result: `{
				"schema_version": 1,
				"outcome": "error",
				"diagnostic": {
					"code": "terraform_failed",
					"summary": "Terraform failed.",
					"detail_path": "missing-diagnostic.txt"
				}
			}`,
			execution: runtime.RunExecution{Err: commandError},
			expected:  "inspecting structured run result diagnostic detail",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workingDir := t.TempDir()
			if test.setup != nil {
				test.setup(t, workingDir)
			}
			resultPath := filepath.Join(workingDir, "step-result.json")
			Ok(t, os.WriteFile(resultPath, []byte(test.result), 0o600))

			_, err := (runtime.StructuredRunResultCompleter{}).CompleteRun(
				workingDir,
				resultPath,
				test.execution,
			)

			ErrContains(t, test.expected, err)
		})
	}
}

func TestStructuredRunResultCompleterRejectsInvalidReviewAndDiagnostic(t *testing.T) {
	commandError := errors.New("exit status 1")
	cases := []struct {
		name      string
		result    string
		execution runtime.RunExecution
		expected  string
	}{
		{
			name: "unknown review mode",
			result: changedStepResultJSON(`{
				"detail_mode": "unknown"
			}`),
			expected: `invalid structured run result review detail mode "unknown"`,
		},
		{
			name: "inline review without path",
			result: changedStepResultJSON(`{
				"detail_mode": "inline"
			}`),
			expected: "inline structured run result review requires inline_detail_path",
		},
		{
			name: "review path escapes working directory",
			result: changedStepResultJSON(`{
				"detail_mode": "inline",
				"inline_detail_path": "../plan.txt"
			}`),
			expected: "structured run result review path must be relative and remain in the working directory",
		},
		{
			name: "URL review without URL",
			result: changedStepResultJSON(`{
				"detail_mode": "url"
			}`),
			expected: "URL structured run result review requires details_url",
		},
		{
			name: "review URL with unsupported scheme",
			result: changedStepResultJSON(`{
				"detail_mode": "url",
				"details_url": "javascript:alert(1)"
			}`),
			expected: "structured run result review URL must use http or https",
		},
		{
			name: "diagnostic without code",
			result: `{
				"schema_version": 1,
				"outcome": "error",
				"diagnostic": {
					"summary": "Terraform failed."
				}
			}`,
			execution: runtime.RunExecution{Err: commandError},
			expected:  "structured run result diagnostic code is required",
		},
		{
			name: "diagnostic without summary",
			result: `{
				"schema_version": 1,
				"outcome": "error",
				"diagnostic": {
					"code": "terraform_failed"
				}
			}`,
			execution: runtime.RunExecution{Err: commandError},
			expected:  "structured run result diagnostic summary is required",
		},
		{
			name: "unknown diagnostic code",
			result: `{
				"schema_version": 1,
				"outcome": "error",
				"diagnostic": {
					"code": "terraform_error",
					"summary": "Terraform plan failed."
				}
			}`,
			execution: runtime.RunExecution{Err: commandError},
			expected:  `invalid structured run result diagnostic code "terraform_error"`,
		},
		{
			name: "diagnostic path escapes working directory",
			result: `{
				"schema_version": 1,
				"outcome": "error",
				"diagnostic": {
					"code": "terraform_failed",
					"summary": "Terraform failed.",
					"detail_path": "../error.txt"
				}
			}`,
			execution: runtime.RunExecution{Err: commandError},
			expected:  "structured run result diagnostic path must be relative and remain in the working directory",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			workingDir := t.TempDir()
			resultPath := filepath.Join(workingDir, "step-result.json")
			err := os.WriteFile(resultPath, []byte(c.result), 0o600)
			Ok(t, err)

			_, err = (runtime.StructuredRunResultCompleter{}).CompleteRun(
				workingDir,
				resultPath,
				c.execution,
			)

			ErrContains(t, c.expected, err)
		})
	}
}

func changedStepResultJSON(review string) string {
	return `{
		"schema_version": 1,
		"outcome": "success",
		"changes": {
			"has_changes": true,
			"has_output_only_changes": false,
			"add": 1,
			"change": 0,
			"destroy": 0,
			"import": 0,
			"forget": 0
		},
		"review": ` + review + `
	}`
}
