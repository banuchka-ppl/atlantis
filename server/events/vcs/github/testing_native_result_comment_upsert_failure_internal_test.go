// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"errors"
	"testing"

	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/vcs"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

const testingNativeResultCommentUpsertFailureTargetSpec = "testing:ppl-ai/agi#123:plan"

func TestTestingNativeResultCommentUpsertFailureClientDisabled(t *testing.T) {
	delegate := newRecordingNativeResultCommentUpsertClient()
	client, err := newTestingNativeResultCommentUpsertFailureClient(delegate, "")
	Ok(t, err)

	_, err = client.UpsertNativeResultComment(logging.NewNoopLogger(t), models.Repo{FullName: "ppl-ai/agi"}, 123, "comment", "plan", "marker")
	Ok(t, err)
	Equals(t, []nativeResultCommentUpsertCall{{
		repo:    "ppl-ai/agi",
		pullNum: 123,
		comment: "comment",
		command: "plan",
		marker:  "marker",
	}}, delegate.upsertCalls)
}

func TestTestingNativeResultCommentUpsertFailureClientMatchesExactlyOnce(t *testing.T) {
	delegate := newRecordingNativeResultCommentUpsertClient()
	client, err := newTestingNativeResultCommentUpsertFailureClient(delegate, testingNativeResultCommentUpsertFailureTargetSpec)
	Ok(t, err)
	logger := logging.NewNoopLogger(t)

	wrongSelectors := []struct {
		repo    string
		pullNum int
		command string
	}{
		{repo: "ppl-ai/space", pullNum: 123, command: "plan"},
		{repo: "ppl-ai/agi", pullNum: 124, command: "plan"},
		{repo: "ppl-ai/agi", pullNum: 123, command: "apply"},
	}
	for _, selector := range wrongSelectors {
		_, err = client.UpsertNativeResultComment(logger, models.Repo{FullName: selector.repo}, selector.pullNum, "comment", selector.command, "marker")
		Ok(t, err)
	}
	Equals(t, []nativeResultCommentUpsertCall{
		{repo: "ppl-ai/space", pullNum: 123, comment: "comment", command: "plan", marker: "marker"},
		{repo: "ppl-ai/agi", pullNum: 124, comment: "comment", command: "plan", marker: "marker"},
		{repo: "ppl-ai/agi", pullNum: 123, comment: "comment", command: "apply", marker: "marker"},
	}, delegate.upsertCalls)

	_, err = client.UpsertNativeResultComment(logger, models.Repo{FullName: "ppl-ai/agi"}, 123, "first", "plan", "first-marker")
	Assert(t, errors.Is(err, errPPLXTestingNativeResultCommentUpsertFailure), "expected one-shot injected failure, got %v", err)
	Equals(t, len(wrongSelectors), len(delegate.upsertCalls))

	_, err = client.UpsertNativeResultComment(logger, models.Repo{FullName: "ppl-ai/agi"}, 123, "second", "plan", "second-marker")
	Ok(t, err)
	Equals(t, nativeResultCommentUpsertCall{
		repo:    "ppl-ai/agi",
		pullNum: 123,
		comment: "second",
		command: "plan",
		marker:  "second-marker",
	}, delegate.upsertCalls[len(delegate.upsertCalls)-1])
}

func TestParseTestingNativeResultCommentUpsertFailureTargetRejectsUnsafeSelectors(t *testing.T) {
	unsafeTargets := []string{
		"ppl-ai/agi#123:plan",
		"testing:ppl-ai/space#123:plan",
		"testing:ppl-ai/agi#0:plan",
		"testing:ppl-ai/agi#0123:plan",
		"testing:ppl-ai/agi#+123:plan",
		"testing:ppl-ai/agi#not-a-number:plan",
		"testing:ppl-ai/agi#123:apply",
		"testing:ppl-ai/agi#123:plan:extra",
		"testing:ppl-ai/agi#123#456:plan",
	}
	for _, target := range unsafeTargets {
		_, err := parseTestingNativeResultCommentUpsertFailureTarget(target)
		Assert(t, err != nil, "expected %q to be rejected", target)
	}
}

type recordingNativeResultCommentUpsertClient struct {
	*vcs.NotConfiguredVCSClient
	upsertCalls []nativeResultCommentUpsertCall
}

type nativeResultCommentUpsertCall struct {
	repo    string
	pullNum int
	comment string
	command string
	marker  string
}

func newRecordingNativeResultCommentUpsertClient() *recordingNativeResultCommentUpsertClient {
	return &recordingNativeResultCommentUpsertClient{
		NotConfiguredVCSClient: &vcs.NotConfiguredVCSClient{Host: models.Github},
	}
}

func (c *recordingNativeResultCommentUpsertClient) CreateCommentWithNativeResultTrailer(_ logging.SimpleLogging, _ models.Repo, _ int, _ string, _ string, _ string) (vcs.NativeResultCommentPublication, error) {
	return vcs.NativeResultCommentPublication{}, nil
}

func (c *recordingNativeResultCommentUpsertClient) UpsertNativeResultComment(_ logging.SimpleLogging, repo models.Repo, pullNum int, comment string, command string, marker string) (vcs.NativeResultCommentPublication, error) {
	c.upsertCalls = append(c.upsertCalls, nativeResultCommentUpsertCall{
		repo:    repo.FullName,
		pullNum: pullNum,
		comment: comment,
		command: command,
		marker:  marker,
	})
	return vcs.NativeResultCommentPublication{}, nil
}
