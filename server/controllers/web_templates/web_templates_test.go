// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package web_templates

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/jobs"
	. "github.com/runatlantis/atlantis/testing"
)

func TestIndexTemplate(t *testing.T) {
	err := IndexTemplate.Execute(io.Discard, IndexData{
		Locks: []LockIndexData{
			{
				LockPath:      "lock path",
				RepoFullName:  "repo full name",
				PullNum:       1,
				Path:          "path",
				Workspace:     "workspace",
				Time:          time.Now(),
				TimeFormatted: "2006-01-02 15:04:05",
			},
		},
		ApplyLock: ApplyLockData{
			Locked:        true,
			Time:          time.Now(),
			TimeFormatted: "2006-01-02 15:04:05",
		},
		AtlantisVersion: "v0.0.0",
		CleanedBasePath: "/path",
		PullToJobMapping: []jobs.PullInfoWithJobIDs{
			{
				Pull: jobs.PullInfo{
					PullNum:      1,
					Repo:         "repo",
					RepoFullName: "repo full name",
					ProjectName:  "project name",
					Path:         "path",
					Workspace:    "workspace",
				},
				JobIDInfos: []jobs.JobIDInfo{
					{JobID: "job id", JobIDUrl: "job id url", JobDescription: "job description", Time: time.Now(), TimeFormatted: "02-01-2006 15:04:05", JobStep: "job step"},
				},
			},
		},
	})
	Ok(t, err)
}

func TestLockTemplate(t *testing.T) {
	err := LockTemplate.Execute(io.Discard, LockDetailData{
		LockKeyEncoded:  "lock key encoded",
		LockKey:         "lock key",
		PullRequestLink: "https://example.com",
		LockedBy:        "locked by",
		Workspace:       "workspace",
		AtlantisVersion: "v0.0.0",
		CleanedBasePath: "/path",
		RepoOwner:       "repo owner",
		RepoName:        "repo name",
	})
	Ok(t, err)
}

func TestProjectJobsTemplate(t *testing.T) {
	var output bytes.Buffer
	err := ProjectJobsTemplate.Execute(&output, ProjectJobData{
		AtlantisVersion: "v0.0.0",
		ProjectPath:     "project path",
		CleanedBasePath: "/path",
	})
	Ok(t, err)
	Assert(t, strings.Contains(output.String(), "Terraform job"), "expected job heading")
	Assert(t, strings.Contains(output.String(), "project path"), "expected job ID")
	Assert(t, strings.Contains(output.String(), "Connection lost"), "expected explicit disconnect state")
	Assert(t, !strings.Contains(output.String(), "watermark"), "job viewer must not render the old watermark")
	Assert(t, !strings.Contains(output.String(), "atlantis-icon_512"), "job viewer must not render the bottom-right logo")
}

func TestProjectJobsErrorTemplate(t *testing.T) {
	err := ProjectJobsTemplate.Execute(io.Discard, ProjectJobsError{
		AtlantisVersion: "v0.0.0",
		ProjectPath:     "project path",
		CleanedBasePath: "/path",
	})
	Ok(t, err)
}

func TestGithubAppSetupTemplate(t *testing.T) {
	err := GithubAppSetupTemplate.Execute(io.Discard, GithubSetupData{
		Target:          "target",
		Manifest:        "manifest",
		ID:              1,
		Key:             "key",
		WebhookSecret:   "webhook secret",
		URL:             "https://example.com",
		CleanedBasePath: "/path",
	})
	Ok(t, err)
}
