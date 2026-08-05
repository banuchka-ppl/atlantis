// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
)

const (
	CommandCompletionSchemaVersion = 1
	MaxCommandCompletionBytes      = 1 << 20
	maxCommandDiagnosticBytes      = 4096
	commandCompletionType          = "command_completion"
)

var exactCommitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

type VCSResultPublicationState string

const (
	VCSResultPublicationSucceeded  VCSResultPublicationState = "succeeded"
	VCSResultPublicationPartial    VCSResultPublicationState = "partial"
	VCSResultPublicationFailed     VCSResultPublicationState = "failed"
	VCSResultPublicationSuppressed VCSResultPublicationState = "suppressed"
)

type VCSResultPublicationAction string

const (
	VCSResultPublicationCreated VCSResultPublicationAction = "created"
	VCSResultPublicationUpdated VCSResultPublicationAction = "updated"
	VCSResultPublicationNone    VCSResultPublicationAction = "none"
)

type NativeResultMarkerState string

const (
	NativeResultMarkerPublished    NativeResultMarkerState = "published"
	NativeResultMarkerNotPublished NativeResultMarkerState = "not_published"
	NativeResultMarkerNotRequested NativeResultMarkerState = "not_requested"
)

type VCSResultPublication struct {
	State                   VCSResultPublicationState
	Action                  VCSResultPublicationAction
	CommentIDs              []int64
	RootCommentID           int64
	TerminalCommentID       int64
	NativeResultMarkerState NativeResultMarkerState
}

type CommandCompletionV1 struct {
	Type           string                          `json:"type"`
	SchemaVersion  int                             `json:"schema_version"`
	CommandRunID   string                          `json:"command_run_id"`
	IdempotencyKey string                          `json:"idempotency_key"`
	StartedAt      time.Time                       `json:"started_at"`
	CompletedAt    time.Time                       `json:"completed_at"`
	Repository     CommandCompletionRepository     `json:"repository"`
	PullRequest    CommandCompletionPullRequest    `json:"pull_request"`
	Command        CommandCompletionCommand        `json:"command"`
	Execution      CommandCompletionExecution      `json:"execution"`
	VCSPublication CommandCompletionVCSPublication `json:"vcs_publication"`
}

type CommandCompletionRepository struct {
	VCSHost string `json:"vcs_host"`
	Owner   string `json:"owner"`
	Name    string `json:"name"`
}

type CommandCompletionPullRequest struct {
	Number  int    `json:"number"`
	HeadSHA string `json:"head_sha"`
}

type CommandCompletionCommand struct {
	Name           string                          `json:"name"`
	Trigger        string                          `json:"trigger"`
	Subcommand     string                          `json:"subcommand"`
	RequestedScope CommandCompletionRequestedScope `json:"requested_scope"`
}

type CommandCompletionRequestedScope struct {
	Dir         string `json:"dir"`
	Workspace   string `json:"workspace"`
	ProjectName string `json:"project_name"`
}

type CommandCompletionExecution struct {
	Outcome      string                       `json:"outcome"`
	ProjectTotal int                          `json:"project_total"`
	Projects     []CommandCompletionProject   `json:"projects"`
	Diagnostic   *CommandCompletionDiagnostic `json:"diagnostic,omitempty"`
}

type CommandCompletionProject struct {
	Dir         string                          `json:"dir"`
	Workspace   string                          `json:"workspace"`
	ProjectName string                          `json:"project_name"`
	Outcome     string                          `json:"outcome"`
	Changes     *CommandCompletionChangeSummary `json:"changes,omitempty"`
	Diagnostic  *CommandCompletionDiagnostic    `json:"diagnostic,omitempty"`
}

type CommandCompletionChangeSummary struct {
	HasChanges           bool `json:"has_changes"`
	HasOutputOnlyChanges bool `json:"has_output_only_changes"`
	Add                  int  `json:"add"`
	Change               int  `json:"change"`
	Destroy              int  `json:"destroy"`
	Import               int  `json:"import"`
	Forget               int  `json:"forget"`
}

type CommandCompletionDiagnostic struct {
	Code    models.ProjectRunDiagnosticCode `json:"code"`
	Summary string                          `json:"summary"`
}

type CommandCompletionVCSPublication struct {
	State                   VCSResultPublicationState  `json:"state"`
	Action                  VCSResultPublicationAction `json:"action"`
	NativeResultMarkerState NativeResultMarkerState    `json:"native_result_marker_state"`
	CommentIDs              []string                   `json:"comment_ids"`
	RootCommentID           string                     `json:"root_comment_id,omitempty"`
	TerminalCommentID       string                     `json:"terminal_comment_id,omitempty"`
}

func BuildCommandCompletion(
	ctx *command.Context,
	cmd PullCommand,
	result command.Result,
	publication VCSResultPublication,
	completedAt time.Time,
) (CommandCompletionV1, error) {
	if ctx == nil {
		return CommandCompletionV1{}, errors.New("command completion context is required")
	}
	if cmd == nil {
		return CommandCompletionV1{}, errors.New("command completion command is required")
	}

	projects := make([]CommandCompletionProject, 0, len(result.ProjectResults))
	for _, projectResult := range result.ProjectResults {
		if projectResult.Command != cmd.CommandName() {
			return CommandCompletionV1{}, fmt.Errorf(
				"command completion project command %q does not match aggregate command %q",
				projectResult.Command,
				cmd.CommandName(),
			)
		}
		project, err := buildCommandCompletionProject(projectResult)
		if err != nil {
			return CommandCompletionV1{}, err
		}
		projects = append(projects, project)
	}
	slices.SortFunc(projects, compareCommandCompletionProjectIdentity)
	for i := 1; i < len(projects); i++ {
		if projects[i-1].Dir == projects[i].Dir &&
			projects[i-1].Workspace == projects[i].Workspace &&
			projects[i-1].ProjectName == projects[i].ProjectName {
			return CommandCompletionV1{}, fmt.Errorf(
				"duplicate command completion project identity %q/%q/%q",
				projects[i].Dir,
				projects[i].Workspace,
				projects[i].ProjectName,
			)
		}
	}

	trigger := "comment"
	if ctx.API {
		trigger = "api"
	} else if ctx.Trigger == command.AutoTrigger {
		trigger = "autoplan"
	}
	if cmd.IsAutoplan() != (trigger == "autoplan") {
		return CommandCompletionV1{}, errors.New("command completion trigger conflicts with command type")
	}

	completion := CommandCompletionV1{
		Type:           commandCompletionType,
		SchemaVersion:  CommandCompletionSchemaVersion,
		CommandRunID:   ctx.CommandRunID,
		IdempotencyKey: ctx.CommandRunID,
		StartedAt:      ctx.CommandStartedAt.UTC(),
		CompletedAt:    completedAt.UTC(),
		Repository: CommandCompletionRepository{
			VCSHost: ctx.Pull.BaseRepo.VCSHost.Hostname,
			Owner:   ctx.Pull.BaseRepo.Owner,
			Name:    ctx.Pull.BaseRepo.Name,
		},
		PullRequest: CommandCompletionPullRequest{
			Number:  ctx.Pull.Num,
			HeadSHA: ctx.Pull.HeadCommit,
		},
		Command: CommandCompletionCommand{
			Name:       cmd.CommandName().String(),
			Trigger:    trigger,
			Subcommand: cmd.SubCommandName(),
			RequestedScope: CommandCompletionRequestedScope{
				Dir: cmd.Dir(),
			},
		},
		Execution: CommandCompletionExecution{
			Outcome:      "success",
			ProjectTotal: len(projects),
			Projects:     projects,
		},
		VCSPublication: buildCommandCompletionPublication(publication),
	}
	switch commentCommand := cmd.(type) {
	case *CommentCommand:
		completion.Command.RequestedScope.Workspace = commentCommand.Workspace
		completion.Command.RequestedScope.ProjectName = commentCommand.ProjectName
	case CommentCommand:
		completion.Command.RequestedScope.Workspace = commentCommand.Workspace
		completion.Command.RequestedScope.ProjectName = commentCommand.ProjectName
	}
	if result.HasErrors() {
		completion.Execution.Outcome = "error"
		if result.Error != nil || result.Failure != "" || len(projects) == 0 {
			completion.Execution.Diagnostic = &CommandCompletionDiagnostic{
				Code:    models.ProjectRunDiagnosticCodeInternalError,
				Summary: "command execution failed",
			}
		}
	}
	if _, err := EncodeCommandCompletion(completion); err != nil {
		return CommandCompletionV1{}, err
	}
	return completion, nil
}

func EncodeCommandCompletion(completion CommandCompletionV1) ([]byte, error) {
	if err := validateCommandCompletion(completion); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(completion)
	if err != nil {
		return nil, fmt.Errorf("encoding command completion: %w", err)
	}
	if len(encoded) > MaxCommandCompletionBytes {
		return nil, fmt.Errorf("command completion exceeds maximum size of %d bytes", MaxCommandCompletionBytes)
	}
	return encoded, nil
}

func buildCommandCompletionProject(result command.ProjectResult) (CommandCompletionProject, error) {
	if !isSafeProjectDir(result.RepoRelDir) {
		return CommandCompletionProject{}, fmt.Errorf("invalid command completion project directory %q", result.RepoRelDir)
	}
	if strings.TrimSpace(result.Workspace) == "" {
		return CommandCompletionProject{}, errors.New("command completion project workspace is required")
	}
	project := CommandCompletionProject{
		Dir:         result.RepoRelDir,
		Workspace:   result.Workspace,
		ProjectName: result.ProjectName,
		Outcome:     "error",
	}
	if result.ProjectRunResult != nil && result.ProjectRunResult.Changes != nil {
		changes := result.ProjectRunResult.Changes
		project.Changes = &CommandCompletionChangeSummary{
			HasChanges:           changes.HasChanges,
			HasOutputOnlyChanges: changes.HasOutputOnlyChanges,
			Add:                  changes.Add,
			Change:               changes.Change,
			Destroy:              changes.Destroy,
			Import:               changes.Import,
			Forget:               changes.Forget,
		}
	}
	if result.IsSuccessful() {
		if result.ProjectRunResult == nil || result.ProjectRunResult.Outcome != models.ProjectRunOutcomeSuccess {
			return CommandCompletionProject{}, fmt.Errorf(
				"successful command completion project %q/%q/%q requires an authoritative typed result",
				result.RepoRelDir,
				result.Workspace,
				result.ProjectName,
			)
		}
		if project.Changes == nil {
			return CommandCompletionProject{}, fmt.Errorf(
				"successful command completion project %q/%q/%q requires typed changes",
				result.RepoRelDir,
				result.Workspace,
				result.ProjectName,
			)
		}
		project.Outcome = "success"
		return project, nil
	}
	if result.ProjectRunResult != nil && result.ProjectRunResult.Outcome == models.ProjectRunOutcomeSuccess {
		return CommandCompletionProject{}, fmt.Errorf(
			"errored command completion project %q/%q/%q has a successful typed result",
			result.RepoRelDir,
			result.Workspace,
			result.ProjectName,
		)
	}
	project.Diagnostic = commandCompletionDiagnostic(result)
	return project, nil
}

func commandCompletionDiagnostic(result command.ProjectResult) *CommandCompletionDiagnostic {
	if result.ProjectRunResult != nil && result.ProjectRunResult.Diagnostic != nil {
		diagnostic := result.ProjectRunResult.Diagnostic
		return &CommandCompletionDiagnostic{
			Code:    diagnostic.Code,
			Summary: boundedCommandDiagnostic(diagnostic.Summary),
		}
	}
	if result.FailureReason == command.ProjectLockFailureReason {
		return &CommandCompletionDiagnostic{
			Code:    models.ProjectRunDiagnosticCodeProjectLocked,
			Summary: "project is locked",
		}
	}
	return &CommandCompletionDiagnostic{
		Code:    models.ProjectRunDiagnosticCodeInternalError,
		Summary: "project execution failed",
	}
}

func buildCommandCompletionPublication(publication VCSResultPublication) CommandCompletionVCSPublication {
	commentIDs := make([]string, len(publication.CommentIDs))
	for i, commentID := range publication.CommentIDs {
		commentIDs[i] = strconv.FormatInt(commentID, 10)
	}
	result := CommandCompletionVCSPublication{
		State:                   publication.State,
		Action:                  publication.Action,
		NativeResultMarkerState: publication.NativeResultMarkerState,
		CommentIDs:              commentIDs,
	}
	if publication.RootCommentID > 0 {
		result.RootCommentID = strconv.FormatInt(publication.RootCommentID, 10)
	}
	if publication.TerminalCommentID > 0 {
		result.TerminalCommentID = strconv.FormatInt(publication.TerminalCommentID, 10)
	}
	return result
}

func validateCommandCompletion(completion CommandCompletionV1) error {
	if completion.Type != commandCompletionType || completion.SchemaVersion != CommandCompletionSchemaVersion {
		return errors.New("invalid command completion type or schema version")
	}
	parsedRunID, err := uuid.Parse(completion.CommandRunID)
	if err != nil || parsedRunID.Version() != 7 || parsedRunID.String() != strings.ToLower(completion.CommandRunID) {
		return errors.New("command completion command_run_id must be a canonical UUIDv7")
	}
	if completion.IdempotencyKey != completion.CommandRunID {
		return errors.New("command completion idempotency_key must equal command_run_id")
	}
	if completion.StartedAt.IsZero() || completion.CompletedAt.IsZero() || completion.CompletedAt.Before(completion.StartedAt) {
		return errors.New("command completion timestamps are invalid")
	}
	if !isSafeCommandCompletionText(completion.Repository.VCSHost, false) ||
		!isSafeCommandCompletionText(completion.Repository.Owner, false) ||
		!isSafeCommandCompletionText(completion.Repository.Name, false) {
		return errors.New("command completion repository identity is required")
	}
	if completion.PullRequest.Number <= 0 ||
		!exactCommitPattern.MatchString(completion.PullRequest.HeadSHA) {
		return errors.New("command completion requires a pull request and exact 40-character head SHA")
	}
	if completion.Command.Name != command.Plan.String() && completion.Command.Name != command.Apply.String() {
		return errors.New("command completion supports only plan and apply commands")
	}
	if completion.Command.Trigger != "autoplan" && completion.Command.Trigger != "comment" && completion.Command.Trigger != "api" {
		return errors.New("invalid command completion trigger")
	}
	if !isSafeProjectDir(completion.Command.RequestedScope.Dir) ||
		!isSafeCommandCompletionText(completion.Command.Subcommand, true) ||
		!isSafeCommandCompletionText(completion.Command.RequestedScope.Workspace, true) ||
		!isSafeCommandCompletionText(completion.Command.RequestedScope.ProjectName, true) {
		return errors.New("invalid command completion command scope")
	}
	if completion.Execution.Outcome != "success" && completion.Execution.Outcome != "error" {
		return errors.New("invalid command completion execution outcome")
	}
	if completion.Execution.ProjectTotal != len(completion.Execution.Projects) {
		return errors.New("command completion project_total does not match projects")
	}
	hasProjectError := false
	for i, project := range completion.Execution.Projects {
		if err := validateCommandCompletionProject(project); err != nil {
			return err
		}
		if project.Outcome == "error" {
			hasProjectError = true
		}
		if i > 0 && compareCommandCompletionProjectIdentity(completion.Execution.Projects[i-1], project) >= 0 {
			return errors.New("command completion projects must have unique identities in deterministic order")
		}
	}
	if err := validateCommandCompletionDiagnostic(completion.Execution.Diagnostic); err != nil {
		return err
	}
	if completion.Execution.Outcome == "success" && (hasProjectError || completion.Execution.Diagnostic != nil) {
		return errors.New("successful command completion execution conflicts with an error")
	}
	if completion.Execution.Outcome == "error" && !hasProjectError && completion.Execution.Diagnostic == nil {
		return errors.New("errored command completion execution requires a diagnostic")
	}
	if err := validateCommandCompletionPublication(completion.VCSPublication); err != nil {
		return err
	}
	return nil
}

func validateCommandCompletionPublication(publication CommandCompletionVCSPublication) error {
	switch publication.NativeResultMarkerState {
	case NativeResultMarkerPublished, NativeResultMarkerNotPublished, NativeResultMarkerNotRequested:
	default:
		return errors.New("invalid command completion native-result marker state")
	}
	switch publication.State {
	case VCSResultPublicationSucceeded:
		if publication.Action != VCSResultPublicationCreated && publication.Action != VCSResultPublicationUpdated {
			return errors.New("successful command completion publication requires a create or update action")
		}
		if publication.RootCommentID == "" || publication.TerminalCommentID == "" || len(publication.CommentIDs) == 0 {
			return errors.New("successful command completion publication requires root and terminal comment IDs")
		}
	case VCSResultPublicationPartial:
		if publication.Action != VCSResultPublicationCreated || len(publication.CommentIDs) == 0 ||
			publication.RootCommentID == "" || publication.TerminalCommentID != "" ||
			publication.NativeResultMarkerState == NativeResultMarkerPublished {
			return errors.New("partial command completion publication requires created IDs without a terminal ID")
		}
	case VCSResultPublicationFailed:
		if publication.Action != VCSResultPublicationNone || len(publication.CommentIDs) != 0 ||
			publication.RootCommentID != "" || publication.TerminalCommentID != "" ||
			publication.NativeResultMarkerState == NativeResultMarkerPublished {
			return errors.New("failed command completion publication must not claim published comments")
		}
	case VCSResultPublicationSuppressed:
		if publication.Action != VCSResultPublicationNone || len(publication.CommentIDs) != 0 ||
			publication.RootCommentID != "" || publication.TerminalCommentID != "" ||
			publication.NativeResultMarkerState != NativeResultMarkerNotRequested {
			return errors.New("suppressed command completion publication must have no action or comment IDs")
		}
	default:
		return errors.New("invalid command completion publication state")
	}
	seen := make(map[string]struct{}, len(publication.CommentIDs))
	for _, commentID := range publication.CommentIDs {
		id, err := strconv.ParseInt(commentID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != commentID {
			return errors.New("command completion comment IDs must be positive canonical decimals")
		}
		if _, ok := seen[commentID]; ok {
			return errors.New("command completion comment IDs must be unique")
		}
		seen[commentID] = struct{}{}
	}
	if publication.RootCommentID != "" {
		if _, ok := seen[publication.RootCommentID]; !ok || publication.RootCommentID != publication.CommentIDs[0] {
			return errors.New("command completion root comment ID must be the first published comment")
		}
	}
	if publication.TerminalCommentID != "" {
		if _, ok := seen[publication.TerminalCommentID]; !ok ||
			publication.TerminalCommentID != publication.CommentIDs[len(publication.CommentIDs)-1] {
			return errors.New("command completion terminal comment ID must be the final published comment")
		}
	}
	return nil
}

func validateCommandCompletionProject(project CommandCompletionProject) error {
	if !isSafeProjectDir(project.Dir) ||
		!isSafeCommandCompletionText(project.Workspace, false) ||
		!isSafeCommandCompletionText(project.ProjectName, true) {
		return errors.New("invalid command completion project identity")
	}
	if project.Outcome != "success" && project.Outcome != "error" {
		return errors.New("invalid command completion project outcome")
	}
	if project.Changes != nil {
		if err := validateCommandCompletionChanges(*project.Changes); err != nil {
			return err
		}
	}
	if err := validateCommandCompletionDiagnostic(project.Diagnostic); err != nil {
		return err
	}
	if project.Outcome == "success" && (project.Changes == nil || project.Diagnostic != nil) {
		return errors.New("successful command completion project requires changes without a diagnostic")
	}
	if project.Outcome == "error" && project.Diagnostic == nil {
		return errors.New("errored command completion project requires a diagnostic")
	}
	return nil
}

func validateCommandCompletionChanges(changes CommandCompletionChangeSummary) error {
	if changes.Add < 0 || changes.Change < 0 || changes.Destroy < 0 || changes.Import < 0 || changes.Forget < 0 {
		return errors.New("command completion change counts must not be negative")
	}
	hasCount := changes.Add > 0 || changes.Change > 0 || changes.Destroy > 0 || changes.Import > 0 || changes.Forget > 0
	if changes.HasOutputOnlyChanges && (!changes.HasChanges || hasCount) {
		return errors.New("command completion output-only changes are inconsistent")
	}
	if changes.HasChanges != (hasCount || changes.HasOutputOnlyChanges) {
		return errors.New("command completion change presence is inconsistent with counts")
	}
	return nil
}

func validateCommandCompletionDiagnostic(diagnostic *CommandCompletionDiagnostic) error {
	if diagnostic == nil {
		return nil
	}
	if !diagnostic.Code.IsValid() ||
		!utf8.ValidString(diagnostic.Summary) ||
		diagnostic.Summary == "" ||
		diagnostic.Summary != strings.TrimSpace(diagnostic.Summary) ||
		len(diagnostic.Summary) > maxCommandDiagnosticBytes {
		return errors.New("invalid command completion diagnostic")
	}
	return nil
}

func compareCommandCompletionProjectIdentity(a, b CommandCompletionProject) int {
	if cmp := strings.Compare(a.Dir, b.Dir); cmp != 0 {
		return cmp
	}
	if cmp := strings.Compare(a.Workspace, b.Workspace); cmp != 0 {
		return cmp
	}
	return strings.Compare(a.ProjectName, b.ProjectName)
}

func isSafeCommandCompletionText(value string, allowEmpty bool) bool {
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	return allowEmpty || strings.TrimSpace(value) != ""
}

func isSafeProjectDir(dir string) bool {
	if dir == "" {
		return true
	}
	cleaned := path.Clean(dir)
	return !path.IsAbs(dir) && cleaned == dir && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func boundedCommandDiagnostic(summary string) string {
	summary = strings.TrimSpace(strings.ToValidUTF8(summary, "�"))
	if summary == "" {
		return "project execution failed"
	}
	for len(summary) > maxCommandDiagnosticBytes {
		_, size := utf8.DecodeLastRuneInString(summary)
		summary = summary[:len(summary)-size]
	}
	return summary
}
