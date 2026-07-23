// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/runatlantis/atlantis/server/events/command"
)

const (
	nativeResultCommentMarkerPrefix  = "<!-- atlantis-native-result:v2:"
	nativeResultCommentMarkerSuffix  = " -->"
	nativeResultCommentMarkerType    = "native_result"
	nativeResultCommentMarkerVersion = 2
)

type nativeResultCommentMarkerPayload struct {
	Type         string                             `json:"type"`
	Version      int                                `json:"version"`
	RepoOwner    string                             `json:"repo_owner"`
	RepoName     string                             `json:"repo_name"`
	PullNum      int                                `json:"pull_num"`
	HeadSHA      string                             `json:"head_sha,omitempty"`
	Command      string                             `json:"command"`
	SubCommand   string                             `json:"sub_command,omitempty"`
	Dir          string                             `json:"dir,omitempty"`
	Workspace    string                             `json:"workspace,omitempty"`
	ProjectName  string                             `json:"project_name,omitempty"`
	Autoplan     bool                               `json:"autoplan"`
	Outcome      string                             `json:"outcome"`
	ProjectTotal int                                `json:"project_total"`
	Projects     []nativeResultCommentMarkerProject `json:"projects"`
	Failures     []nativeResultCommentMarkerFailure `json:"failures"`
}

type nativeResultCommentMarkerProject struct {
	Dir         string `json:"dir,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
	Outcome     string `json:"outcome"`
}

type nativeResultCommentMarkerFailure struct {
	Dir             string `json:"dir,omitempty"`
	Workspace       string `json:"workspace,omitempty"`
	ProjectName     string `json:"project_name,omitempty"`
	Reason          string `json:"reason"`
	BlockingPullNum int    `json:"blocking_pull_num,omitempty"`
}

func appendNativeResultCommentMarker(comment string, marker string) string {
	return strings.TrimRight(comment, "\n") + "\n\n" + marker
}

func encodeNativeResultCommentMarker(ctx *command.Context, cmd PullCommand, result command.Result) (string, error) {
	payload := nativeResultCommentMarkerPayload{
		RepoOwner:    ctx.Pull.BaseRepo.Owner,
		RepoName:     ctx.Pull.BaseRepo.Name,
		PullNum:      ctx.Pull.Num,
		HeadSHA:      ctx.Pull.HeadCommit,
		Type:         nativeResultCommentMarkerType,
		Version:      nativeResultCommentMarkerVersion,
		Command:      cmd.CommandName().String(),
		SubCommand:   cmd.SubCommandName(),
		Dir:          cmd.Dir(),
		Autoplan:     cmd.IsAutoplan(),
		Outcome:      "success",
		ProjectTotal: len(result.ProjectResults),
		Projects:     make([]nativeResultCommentMarkerProject, 0, len(result.ProjectResults)),
		Failures:     make([]nativeResultCommentMarkerFailure, 0),
	}
	if result.HasErrors() {
		payload.Outcome = "error"
	}
	for _, projectResult := range result.ProjectResults {
		payload.Projects = append(payload.Projects, nativeResultCommentMarkerProject{
			Dir:         projectResult.RepoRelDir,
			Workspace:   projectResult.Workspace,
			ProjectName: projectResult.ProjectName,
			Outcome:     nativeResultCommentMarkerProjectOutcome(projectResult),
		})
		if projectResult.IsSuccessful() {
			continue
		}
		failure := nativeResultCommentMarkerFailure{
			Dir:         projectResult.RepoRelDir,
			Workspace:   projectResult.Workspace,
			ProjectName: projectResult.ProjectName,
			Reason:      "failure",
		}
		if projectResult.Error != nil {
			failure.Reason = "error"
		}
		if projectResult.FailureReason != "" {
			failure.Reason = string(projectResult.FailureReason)
			failure.BlockingPullNum = projectResult.BlockingPullNum
		}
		payload.Failures = append(payload.Failures, failure)
	}

	switch c := cmd.(type) {
	case *CommentCommand:
		payload.Workspace = c.Workspace
		payload.ProjectName = c.ProjectName
	case CommentCommand:
		payload.Workspace = c.Workspace
		payload.ProjectName = c.ProjectName
	}

	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return nativeResultCommentMarkerPrefix + base64.RawURLEncoding.EncodeToString(rawPayload) + nativeResultCommentMarkerSuffix, nil
}

func nativeResultCommentMarkerProjectOutcome(result command.ProjectResult) string {
	if result.Error != nil {
		return "error"
	}
	if result.FailureReason != "" {
		return string(result.FailureReason)
	}
	if result.Failure != "" {
		return "failure"
	}
	if result.PlanSuccess == nil {
		return "success"
	}
	if result.PlanSuccess.NoChanges() {
		return "no_changes"
	}
	return "changes"
}
