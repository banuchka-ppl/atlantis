// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/vcs"
	"github.com/runatlantis/atlantis/server/logging"
)

const (
	// PPLXTestingNativeResultCommentUpsertFailureTargetEnv is the explicit testing-only fault selector.
	PPLXTestingNativeResultCommentUpsertFailureTargetEnv = "ATLANTIS_PPLX_TESTING_NATIVE_RESULT_COMMENT_UPSERT_FAILURE_TARGET"
	testingNativeResultCommentUpsertFailurePrefix        = "testing:"
	testingNativeResultCommentUpsertFailureRepo          = "ppl-ai/agi"
	testingNativeResultCommentUpsertFailureCommand       = "plan"
)

var errPPLXTestingNativeResultCommentUpsertFailure = errors.New("pplx testing fault injection: retryable native result comment upsert failure once")

type testingNativeResultCommentUpsertClient interface {
	vcs.Client
	vcs.NativeResultCommentUpserter
	vcs.NativeResultTrailerCommenter
}

type testingNativeResultCommentUpsertFailureTarget struct {
	repo    string
	pullNum int
	command string
}

type testingNativeResultCommentUpsertFailureClient struct {
	testingNativeResultCommentUpsertClient
	target   testingNativeResultCommentUpsertFailureTarget
	injected atomic.Bool
}

func newTestingNativeResultCommentUpsertFailureClient(client testingNativeResultCommentUpsertClient, targetSpec string) (testingNativeResultCommentUpsertClient, error) {
	if targetSpec == "" {
		return client, nil
	}

	target, err := parseTestingNativeResultCommentUpsertFailureTarget(targetSpec)
	if err != nil {
		return nil, err
	}

	return &testingNativeResultCommentUpsertFailureClient{
		testingNativeResultCommentUpsertClient: client,
		target:                                 target,
	}, nil
}

func parseTestingNativeResultCommentUpsertFailureTarget(targetSpec string) (testingNativeResultCommentUpsertFailureTarget, error) {
	targetSpec, ok := strings.CutPrefix(targetSpec, testingNativeResultCommentUpsertFailurePrefix)
	if !ok {
		return testingNativeResultCommentUpsertFailureTarget{}, fmt.Errorf("target must begin with %q", testingNativeResultCommentUpsertFailurePrefix)
	}

	repoAndPull, command, ok := strings.Cut(targetSpec, ":")
	if !ok || strings.Contains(command, ":") {
		return testingNativeResultCommentUpsertFailureTarget{}, errors.New("target must end with exactly one command selector")
	}
	repo, pullNumString, ok := strings.Cut(repoAndPull, "#")
	if !ok || strings.Contains(pullNumString, "#") {
		return testingNativeResultCommentUpsertFailureTarget{}, errors.New("target must contain exactly one pull request selector")
	}
	if repo != testingNativeResultCommentUpsertFailureRepo {
		return testingNativeResultCommentUpsertFailureTarget{}, fmt.Errorf("target repository must be %q", testingNativeResultCommentUpsertFailureRepo)
	}
	if command != testingNativeResultCommentUpsertFailureCommand {
		return testingNativeResultCommentUpsertFailureTarget{}, fmt.Errorf("target command must be %q", testingNativeResultCommentUpsertFailureCommand)
	}

	pullNum, err := strconv.Atoi(pullNumString)
	if err != nil || pullNum <= 0 || strconv.Itoa(pullNum) != pullNumString {
		return testingNativeResultCommentUpsertFailureTarget{}, errors.New("target pull request must be a positive integer")
	}

	return testingNativeResultCommentUpsertFailureTarget{
		repo:    repo,
		pullNum: pullNum,
		command: command,
	}, nil
}

func (c *testingNativeResultCommentUpsertFailureClient) UpsertNativeResultComment(logger logging.SimpleLogging, repo models.Repo, pullNum int, comment string, command string, marker string) error {
	if repo.FullName != c.target.repo || pullNum != c.target.pullNum || command != c.target.command {
		return c.testingNativeResultCommentUpsertClient.UpsertNativeResultComment(logger, repo, pullNum, comment, command, marker)
	}
	if !c.injected.CompareAndSwap(false, true) {
		return c.testingNativeResultCommentUpsertClient.UpsertNativeResultComment(logger, repo, pullNum, comment, command, marker)
	}

	logger.Warn("injecting one-shot testing native result comment upsert failure for repository %q pull request %d command %q", repo.FullName, pullNum, command)
	return errPPLXTestingNativeResultCommentUpsertFailure
}
