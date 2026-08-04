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
	"regexp"
	"strconv"
	"strings"

	eventmodels "github.com/runatlantis/atlantis/server/events/models"
	tally "github.com/uber-go/tally/v4"
)

const (
	// StepResultSchemaVersion is the structured custom-run result version accepted by Atlantis.
	StepResultSchemaVersion = 1
	// MaxStepResultBytes bounds structured custom-run result documents to one MiB.
	MaxStepResultBytes = 1 << 20
	// MaxStepResultDetailBytes bounds inline review and diagnostic sidecars.
	MaxStepResultDetailBytes = 55_000
	// StepResultFileEnvVar tells a custom run step where it may publish a structured result.
	StepResultFileEnvVar = "ATLANTIS_STEP_RESULT_FILE"
	// StepResultModeEnvVar tells the result producer whether its typed result is shadowed or authoritative.
	StepResultModeEnvVar = "ATLANTIS_STEP_RESULT_MODE"
)

// StructuredRunResultMode controls how custom run steps publish structured results.
type StructuredRunResultMode string

const (
	// StructuredRunResultModeOff leaves custom run step execution unchanged.
	StructuredRunResultModeOff StructuredRunResultMode = "off"
	// StructuredRunResultModeShadow validates optional results without changing command behavior.
	StructuredRunResultModeShadow StructuredRunResultMode = "shadow"
	// StructuredRunResultModePrefer returns valid structured results to callers and falls back to legacy output.
	StructuredRunResultModePrefer StructuredRunResultMode = "prefer"
	// StructuredRunResultModeRequired requires valid structured results from eligible command steps.
	StructuredRunResultModeRequired StructuredRunResultMode = "required"
)

// ParseStructuredRunResultMode validates a server-configured rollout mode.
func ParseStructuredRunResultMode(value string) (StructuredRunResultMode, error) {
	switch StructuredRunResultMode(value) {
	case "", StructuredRunResultModeOff:
		return StructuredRunResultModeOff, nil
	case StructuredRunResultModeShadow, StructuredRunResultModePrefer, StructuredRunResultModeRequired:
		return StructuredRunResultMode(value), nil
	default:
		return "", fmt.Errorf(
			"invalid structured run result mode %q: must be one of [off shadow prefer required]",
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
	Code       eventmodels.ProjectRunDiagnosticCode `json:"code"`
	Summary    string                               `json:"summary"`
	DetailPath string                               `json:"detail_path,omitempty"`
}

// RunExecution preserves the console channel and process outcome separately from the typed result.
type RunExecution struct {
	ConsoleOutput string
	Err           error
}

// CompletedRun combines process execution with its validated structured result.
type CompletedRun struct {
	DiagnosticDetail string
	Execution        RunExecution
	Result           StepResultV1
	ReviewDetail     string
}

// RunStepOutput keeps legacy console output separate from an authoritative typed result.
type RunStepOutput struct {
	ConsoleOutput    string
	StructuredResult *CompletedRun
}

// StructuredRunResultComparison describes typed-versus-legacy shadow parity.
type StructuredRunResultComparison string

const (
	StructuredRunResultComparisonMatch                  StructuredRunResultComparison = "match"
	StructuredRunResultComparisonLegacyUnavailable      StructuredRunResultComparison = "legacy_unavailable"
	StructuredRunResultComparisonOutcomeMismatch        StructuredRunResultComparison = "outcome_mismatch"
	StructuredRunResultComparisonChangePresenceMismatch StructuredRunResultComparison = "change_presence_mismatch"
	StructuredRunResultComparisonCountMismatch          StructuredRunResultComparison = "count_mismatch"
	StructuredRunResultComparisonReviewDetailMismatch   StructuredRunResultComparison = "review_detail_mode_mismatch"
)

// StructuredRunResultObserver emits only aggregate, low-cardinality shadow metrics.
type StructuredRunResultObserver struct {
	Scope tally.Scope
}

func (o StructuredRunResultObserver) recordArtifact(command string, status string) {
	if o.Scope == nil {
		return
	}
	o.Scope.Tagged(map[string]string{
		"command": command,
		"status":  status,
	}).Counter("artifact").Inc(1)
}

func (o StructuredRunResultObserver) recordComparison(
	command string,
	comparison StructuredRunResultComparison,
) {
	if o.Scope == nil {
		return
	}
	o.Scope.Tagged(map[string]string{
		"class":   string(comparison),
		"command": command,
	}).Counter("comparison").Inc(1)
}

// CompareStructuredRunResult compares a validated result with the existing
// legacy output without changing either result.
func CompareStructuredRunResult(completed CompletedRun) StructuredRunResultComparison {
	typedSuccess := completed.Result.Outcome == StepResultOutcomeSuccess
	legacySuccess := completed.Execution.Err == nil
	if typedSuccess != legacySuccess {
		return StructuredRunResultComparisonOutcomeMismatch
	}
	if !typedSuccess {
		return StructuredRunResultComparisonMatch
	}
	if !eventmodels.HasPlanSummary(completed.Execution.ConsoleOutput) {
		return StructuredRunResultComparisonLegacyUnavailable
	}

	typedChanges := completed.Result.Changes
	legacyStats := eventmodels.NewPlanSuccessStats(completed.Execution.ConsoleOutput)
	if typedChanges == nil || typedChanges.HasChanges != legacyStats.Changes {
		return StructuredRunResultComparisonChangePresenceMismatch
	}
	if typedChanges.Import != legacyStats.Import ||
		typedChanges.Add != legacyStats.Add ||
		typedChanges.Change != legacyStats.Change ||
		typedChanges.Destroy != legacyStats.Destroy ||
		typedChanges.Forget != legacyStats.Forget {
		return StructuredRunResultComparisonCountMismatch
	}
	if completed.Result.Review == nil ||
		completed.Result.Review.DetailMode != legacyReviewDetailMode(completed.Execution.ConsoleOutput) {
		return StructuredRunResultComparisonReviewDetailMismatch
	}
	return StructuredRunResultComparisonMatch
}

var legacyApplySummaryPattern = regexp.MustCompile(
	`(?m)^\s*Apply complete! Resources: ([0-9]+) added, ([0-9]+) changed, ([0-9]+) destroyed\.\s*$`,
)

// CompareStructuredApplyResult compares typed apply facts with the legacy
// terminal summary used only during shadow migration.
func CompareStructuredApplyResult(completed CompletedRun) StructuredRunResultComparison {
	typedSuccess := completed.Result.Outcome == StepResultOutcomeSuccess
	legacySuccess := completed.Execution.Err == nil
	if typedSuccess != legacySuccess {
		return StructuredRunResultComparisonOutcomeMismatch
	}
	if !typedSuccess {
		return StructuredRunResultComparisonMatch
	}
	typedChanges := completed.Result.Changes
	if typedChanges == nil {
		return StructuredRunResultComparisonChangePresenceMismatch
	}
	if typedChanges.HasOutputOnlyChanges || typedChanges.Import > 0 || typedChanges.Forget > 0 {
		return StructuredRunResultComparisonLegacyUnavailable
	}
	match := legacyApplySummaryPattern.FindStringSubmatch(completed.Execution.ConsoleOutput)
	if match == nil {
		return StructuredRunResultComparisonLegacyUnavailable
	}
	legacyAdd, _ := strconv.Atoi(match[1])
	legacyChange, _ := strconv.Atoi(match[2])
	legacyDestroy, _ := strconv.Atoi(match[3])
	legacyHasChanges := legacyAdd > 0 || legacyChange > 0 || legacyDestroy > 0
	if typedChanges.HasChanges != legacyHasChanges {
		return StructuredRunResultComparisonChangePresenceMismatch
	}
	if typedChanges.Add != legacyAdd ||
		typedChanges.Change != legacyChange ||
		typedChanges.Destroy != legacyDestroy {
		return StructuredRunResultComparisonCountMismatch
	}
	return StructuredRunResultComparisonMatch
}

func legacyReviewDetailMode(output string) StepReviewDetailMode {
	mode := ""
	inlineBytes := 0
	maxBytes := 0
	inEnvelope := false
	for _, line := range strings.Split(output, "\n") {
		if line == "==ATLANTIS_PLAN_DIFF_V1==" {
			inEnvelope = true
			continue
		}
		if !inEnvelope {
			continue
		}
		if value, ok := strings.CutPrefix(line, "mode:"); ok {
			mode = value
			continue
		}
		if value, ok := strings.CutPrefix(line, "inline_bytes:"); ok {
			inlineBytes, _ = strconv.Atoi(value)
			continue
		}
		if value, ok := strings.CutPrefix(line, "max_bytes:"); ok {
			maxBytes, _ = strconv.Atoi(value)
		}
	}
	if mode == "url" || (mode == "url_if_oversize" && inlineBytes > maxBytes) {
		return StepReviewDetailModeURL
	}
	return StepReviewDetailModeInline
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

	workingRoot, err := os.OpenRoot(workingDir)
	if err != nil {
		return CompletedRun{}, fmt.Errorf("opening structured run result working directory: %w", err)
	}
	defer workingRoot.Close()

	resultBytes, err := readBoundedRegularResultFile(
		workingRoot,
		relativeResultPath,
		"structured run result",
		MaxStepResultBytes,
	)
	if err != nil {
		return CompletedRun{}, err
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
	reviewDetail, diagnosticDetail, err := readStepResultReferences(workingRoot, result)
	if err != nil {
		return CompletedRun{}, err
	}

	return CompletedRun{
		DiagnosticDetail: diagnosticDetail,
		Execution:        execution,
		Result:           result,
		ReviewDetail:     reviewDetail,
	}, nil
}

func readBoundedRegularResultFile(
	workingRoot *os.Root,
	relativePath string,
	description string,
	maxBytes int64,
) ([]byte, error) {
	pathInfo, err := workingRoot.Lstat(relativePath)
	if err != nil {
		return nil, fmt.Errorf("inspecting %s: %w", description, err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must be a regular file, not a symlink", description)
	}
	if !pathInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", description)
	}

	file, err := workingRoot.Open(relativePath)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", description, err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspecting opened %s: %w", description, err)
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must remain a regular file", description)
	}
	if !os.SameFile(pathInfo, openedInfo) {
		return nil, fmt.Errorf("%s changed during validation", description)
	}
	if openedInfo.Size() > maxBytes {
		return nil, fmt.Errorf("%s exceeds maximum size of %d bytes", description, maxBytes)
	}

	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", description, err)
	}
	if int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds maximum size of %d bytes", description, maxBytes)
	}
	return content, nil
}

func readStepResultReferences(
	workingRoot *os.Root,
	result StepResultV1,
) (string, string, error) {
	var reviewDetail string
	if result.Review != nil && result.Review.InlineDetailPath != "" {
		content, err := readBoundedRegularResultFile(
			workingRoot,
			result.Review.InlineDetailPath,
			"structured run result review detail",
			MaxStepResultDetailBytes,
		)
		if err != nil {
			return "", "", err
		}
		reviewDetail = string(content)
	}
	var diagnosticDetail string
	if result.Diagnostic != nil && result.Diagnostic.DetailPath != "" {
		content, err := readBoundedRegularResultFile(
			workingRoot,
			result.Diagnostic.DetailPath,
			"structured run result diagnostic detail",
			MaxStepResultDetailBytes,
		)
		if err != nil {
			return "", "", err
		}
		diagnosticDetail = string(content)
	}
	return reviewDetail, diagnosticDetail, nil
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
	if changes.HasOutputOnlyChanges && hasChangeCount {
		return fmt.Errorf("structured run result output-only changes must not include resource counts")
	}
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
	if strings.TrimSpace(string(diagnostic.Code)) == "" {
		return fmt.Errorf("structured run result diagnostic code is required")
	}
	if !diagnostic.Code.IsValid() {
		return fmt.Errorf(
			"invalid structured run result diagnostic code %q",
			diagnostic.Code,
		)
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
