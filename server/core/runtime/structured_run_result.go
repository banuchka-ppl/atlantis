// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// StepResultSchemaVersion is the structured custom-run result version accepted by Atlantis.
	StepResultSchemaVersion = 1
	// MaxStepResultBytes bounds structured custom-run result documents to one MiB.
	MaxStepResultBytes = 1 << 20
	// StepResultFileEnvVar tells a custom run step where it may publish a structured result.
	StepResultFileEnvVar = "ATLANTIS_STEP_RESULT_FILE"
)

// StructuredRunResultMode controls whether custom run steps can publish structured results.
type StructuredRunResultMode string

const (
	// StructuredRunResultModeOff leaves custom run step execution unchanged.
	StructuredRunResultModeOff StructuredRunResultMode = "off"
	// StructuredRunResultModeShadow validates optional results without changing command behavior.
	StructuredRunResultModeShadow StructuredRunResultMode = "shadow"
)

// ParseStructuredRunResultMode validates a server-configured rollout mode.
func ParseStructuredRunResultMode(value string) (StructuredRunResultMode, error) {
	switch StructuredRunResultMode(value) {
	case "", StructuredRunResultModeOff:
		return StructuredRunResultModeOff, nil
	case StructuredRunResultModeShadow:
		return StructuredRunResultModeShadow, nil
	default:
		return "", fmt.Errorf(
			"invalid structured run result mode %q: must be one of [off shadow]",
			value,
		)
	}
}

// ParseStructuredRunResultWorkflowPatterns validates comma-separated workflow globs.
func ParseStructuredRunResultWorkflowPatterns(value string) ([]string, error) {
	return parseStructuredRunResultPatterns(value, "workflow")
}

// ParseStructuredRunResultRepoPatterns validates comma-separated repository globs.
func ParseStructuredRunResultRepoPatterns(value string) ([]string, error) {
	return parseStructuredRunResultPatterns(value, "repository")
}

func parseStructuredRunResultPatterns(value string, kind string) ([]string, error) {
	var patterns []string
	for _, rawPattern := range strings.Split(value, ",") {
		pattern := strings.TrimSpace(rawPattern)
		if pattern == "" {
			continue
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return nil, fmt.Errorf(
				"invalid structured run result %s pattern %q: %w",
				kind,
				pattern,
				err,
			)
		}
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

// StepResultOutcome describes whether the custom command itself completed successfully.
type StepResultOutcome string

const (
	StepResultOutcomeSuccess StepResultOutcome = "success"
	StepResultOutcomeError   StepResultOutcome = "error"
)

// StepChangeSummary contains normalized Terraform/OpenTofu change facts.
type StepChangeSummary struct {
	HasChanges           bool `json:"has_changes"`
	HasOutputOnlyChanges bool `json:"has_output_only_changes"`
	Add                  int  `json:"add"`
	Change               int  `json:"change"`
	Destroy              int  `json:"destroy"`
	Import               int  `json:"import"`
	Forget               int  `json:"forget"`
}

// StepReviewDetailMode selects the primary detail presentation for a review result.
type StepReviewDetailMode string

const (
	StepReviewDetailModeInline StepReviewDetailMode = "inline"
	StepReviewDetailModeURL    StepReviewDetailMode = "url"
)

// StepReview identifies bounded inline detail or an external review URL.
type StepReview struct {
	DetailMode       StepReviewDetailMode `json:"detail_mode"`
	InlineDetailPath string               `json:"inline_detail_path,omitempty"`
	DetailsURL       string               `json:"details_url,omitempty"`
}

// StepResultV1 is the first version of the structured custom-run result contract.
type StepResultV1 struct {
	SchemaVersion int                `json:"schema_version"`
	Outcome       StepResultOutcome  `json:"outcome"`
	Summary       string             `json:"summary,omitempty"`
	Changes       *StepChangeSummary `json:"changes,omitempty"`
	Review        *StepReview        `json:"review,omitempty"`
	Diagnostic    *StepDiagnostic    `json:"diagnostic,omitempty"`
}

// StepDiagnostic contains a stable error class and bounded reviewer-facing detail.
type StepDiagnostic struct {
	Code       string `json:"code"`
	Summary    string `json:"summary"`
	DetailPath string `json:"detail_path,omitempty"`
}

// RunExecution preserves the console channel and process outcome separately from the typed result.
type RunExecution struct {
	ConsoleOutput string
	Err           error
}

// CompletedRun combines process execution with its validated structured result.
type CompletedRun struct {
	Execution RunExecution
	Result    StepResultV1
}

// StructuredRunResultCompleter validates and combines a command result file with process execution.
type StructuredRunResultCompleter struct{}

// CompleteRun reads a structured result rooted in workingDir and validates it against execution.
func (StructuredRunResultCompleter) CompleteRun(workingDir string, resultPath string, execution RunExecution) (CompletedRun, error) {
	workingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return CompletedRun{}, fmt.Errorf("resolving structured run result working directory: %w", err)
	}
	resultPath, err = filepath.Abs(resultPath)
	if err != nil {
		return CompletedRun{}, fmt.Errorf("resolving structured run result path: %w", err)
	}
	relativeResultPath, err := filepath.Rel(workingDir, resultPath)
	if err != nil {
		return CompletedRun{}, fmt.Errorf("checking structured run result path: %w", err)
	}
	if relativeResultPath == ".." || strings.HasPrefix(relativeResultPath, ".."+string(filepath.Separator)) {
		return CompletedRun{}, fmt.Errorf("structured run result path is outside the working directory")
	}

	resultInfo, err := os.Lstat(resultPath)
	if err != nil {
		return CompletedRun{}, fmt.Errorf("inspecting structured run result: %w", err)
	}
	if resultInfo.Mode()&os.ModeSymlink != 0 {
		return CompletedRun{}, fmt.Errorf("structured run result must be a regular file, not a symlink")
	}
	if !resultInfo.Mode().IsRegular() {
		return CompletedRun{}, fmt.Errorf("structured run result must be a regular file")
	}
	resolvedWorkingDir, err := filepath.EvalSymlinks(workingDir)
	if err != nil {
		return CompletedRun{}, fmt.Errorf("resolving structured run result working directory symlinks: %w", err)
	}
	resolvedResultPath, err := filepath.EvalSymlinks(resultPath)
	if err != nil {
		return CompletedRun{}, fmt.Errorf("resolving structured run result path symlinks: %w", err)
	}
	relativeResolvedPath, err := filepath.Rel(resolvedWorkingDir, resolvedResultPath)
	if err != nil {
		return CompletedRun{}, fmt.Errorf("checking resolved structured run result path: %w", err)
	}
	if relativeResolvedPath == ".." || strings.HasPrefix(relativeResolvedPath, ".."+string(filepath.Separator)) {
		return CompletedRun{}, fmt.Errorf("structured run result path resolves outside the working directory")
	}
	if resultInfo.Size() > MaxStepResultBytes {
		return CompletedRun{}, fmt.Errorf(
			"structured run result exceeds maximum size of %d bytes",
			MaxStepResultBytes,
		)
	}

	resultFile, err := os.Open(resultPath)
	if err != nil {
		return CompletedRun{}, fmt.Errorf("opening structured run result: %w", err)
	}
	defer resultFile.Close()
	resultBytes, err := io.ReadAll(io.LimitReader(resultFile, MaxStepResultBytes+1))
	if err != nil {
		return CompletedRun{}, fmt.Errorf("reading structured run result: %w", err)
	}
	if len(resultBytes) > MaxStepResultBytes {
		return CompletedRun{}, fmt.Errorf(
			"structured run result exceeds maximum size of %d bytes",
			MaxStepResultBytes,
		)
	}

	var result StepResultV1
	decoder := json.NewDecoder(bytes.NewReader(resultBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return CompletedRun{}, fmt.Errorf("decoding structured run result: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return CompletedRun{}, fmt.Errorf("structured run result contains more than one JSON value")
	}
	if result.SchemaVersion != StepResultSchemaVersion {
		return CompletedRun{}, fmt.Errorf("unsupported structured run result schema version %d", result.SchemaVersion)
	}
	if err := validateStepResult(result, execution); err != nil {
		return CompletedRun{}, err
	}

	return CompletedRun{
		Execution: execution,
		Result:    result,
	}, nil
}

func validateStepResult(result StepResultV1, execution RunExecution) error {
	switch result.Outcome {
	case StepResultOutcomeSuccess:
		if execution.Err != nil {
			return fmt.Errorf(
				"structured run result outcome %q conflicts with command failure",
				result.Outcome,
			)
		}
		if result.Changes == nil {
			return fmt.Errorf("changes are required for a successful structured run result")
		}
	case StepResultOutcomeError:
		if execution.Err == nil {
			return fmt.Errorf(
				"structured run result outcome %q conflicts with command success",
				result.Outcome,
			)
		}
		if result.Diagnostic == nil {
			return fmt.Errorf("diagnostic is required for an errored structured run result")
		}
	default:
		return fmt.Errorf("invalid structured run result outcome %q", result.Outcome)
	}

	if result.Changes != nil {
		if err := validateStepChanges(*result.Changes); err != nil {
			return err
		}
	}
	if err := validateStepReview(result.Review); err != nil {
		return err
	}
	if err := validateStepDiagnostic(result.Diagnostic); err != nil {
		return err
	}
	return nil
}

func validateStepChanges(changes StepChangeSummary) error {
	if changes.Add < 0 ||
		changes.Change < 0 ||
		changes.Destroy < 0 ||
		changes.Import < 0 ||
		changes.Forget < 0 {
		return fmt.Errorf("structured run result change counts must not be negative")
	}
	if changes.HasOutputOnlyChanges && !changes.HasChanges {
		return fmt.Errorf("structured run result has_output_only_changes requires has_changes")
	}
	hasChangeCount := changes.Add > 0 ||
		changes.Change > 0 ||
		changes.Destroy > 0 ||
		changes.Import > 0 ||
		changes.Forget > 0
	if !changes.HasChanges && hasChangeCount {
		return fmt.Errorf("structured run result has change counts but has_changes is false")
	}
	if changes.HasChanges && !hasChangeCount && !changes.HasOutputOnlyChanges {
		return fmt.Errorf("structured run result has_changes requires a change count or output-only changes")
	}
	return nil
}

func validateStepReview(review *StepReview) error {
	if review == nil {
		return nil
	}
	switch review.DetailMode {
	case StepReviewDetailModeInline:
		if review.InlineDetailPath == "" {
			return fmt.Errorf("inline structured run result review requires inline_detail_path")
		}
	case StepReviewDetailModeURL:
		if review.DetailsURL == "" {
			return fmt.Errorf("URL structured run result review requires details_url")
		}
	default:
		return fmt.Errorf("invalid structured run result review detail mode %q", review.DetailMode)
	}
	if review.InlineDetailPath != "" && !isSafeRelativeResultPath(review.InlineDetailPath) {
		return fmt.Errorf("structured run result review path must be relative and remain in the working directory")
	}
	if review.DetailsURL == "" {
		return nil
	}
	detailsURL, err := url.ParseRequestURI(review.DetailsURL)
	if err != nil ||
		(detailsURL.Scheme != "http" && detailsURL.Scheme != "https") ||
		detailsURL.Host == "" {
		return fmt.Errorf("structured run result review URL must use http or https")
	}
	return nil
}

func validateStepDiagnostic(diagnostic *StepDiagnostic) error {
	if diagnostic == nil {
		return nil
	}
	if strings.TrimSpace(diagnostic.Code) == "" {
		return fmt.Errorf("structured run result diagnostic code is required")
	}
	if strings.TrimSpace(diagnostic.Summary) == "" {
		return fmt.Errorf("structured run result diagnostic summary is required")
	}
	if diagnostic.DetailPath != "" && !isSafeRelativeResultPath(diagnostic.DetailPath) {
		return fmt.Errorf("structured run result diagnostic path must be relative and remain in the working directory")
	}
	return nil
}

func isSafeRelativeResultPath(path string) bool {
	if filepath.IsAbs(path) {
		return false
	}
	path = filepath.Clean(path)
	return path != "." &&
		path != ".." &&
		!strings.HasPrefix(path, ".."+string(filepath.Separator))
}
