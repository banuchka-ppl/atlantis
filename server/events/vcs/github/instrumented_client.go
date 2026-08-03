// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"fmt"

	"github.com/google/go-github/v88/github"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/vcs"
	"github.com/runatlantis/atlantis/server/events/vcs/common"
	"github.com/runatlantis/atlantis/server/logging"
	"github.com/runatlantis/atlantis/server/metrics"
	"github.com/uber-go/tally/v4"
)

// NewInstrumentedGithubClient creates a client proxy responsible for gathering stats and logging
func NewInstrumentedGithubClient(client *Client, testingNativeResultCommentUpsertFailureTarget string, statsScope tally.Scope, logger logging.SimpleLogging) (IGithubClient, error) {
	scope := statsScope.SubScope("github")
	upsertClient, err := newTestingNativeResultCommentUpsertFailureClient(client, testingNativeResultCommentUpsertFailureTarget)
	if err != nil {
		return nil, fmt.Errorf("configuring testing native result comment upsert failure: %w", err)
	}

	instrumentedGHClient := &common.InstrumentedClient{
		Client:     upsertClient,
		StatsScope: scope,
		Logger:     logger,
	}

	return &InstrumentedGithubClient{
		InstrumentedClient: instrumentedGHClient,
		PullRequestGetter:  client,
		StatsScope:         scope,
		Logger:             logger,
	}, nil
}

//go:generate go tool pegomock generate --package mocks -o mocks/mock_github_pull_request_getter.go GithubPullRequestGetter

type GithubPullRequestGetter interface {
	GetPullRequest(logger logging.SimpleLogging, repo models.Repo, pullNum int) (*github.PullRequest, error)
}

// IGithubClient exists to bridge the gap between GithubPullRequestGetter and Client interface to allow
// for a single instrumented client
type IGithubClient interface {
	vcs.Client
	GithubPullRequestGetter
}

// InstrumentedGithubClient should delegate to the underlying InstrumentedClient for vcs provider-agnostic
// methods and implement solely any github specific interfaces.
type InstrumentedGithubClient struct {
	*common.InstrumentedClient
	PullRequestGetter GithubPullRequestGetter
	StatsScope        tally.Scope
	Logger            logging.SimpleLogging
}

func (c *InstrumentedGithubClient) GetPullRequest(logger logging.SimpleLogging, repo models.Repo, pullNum int) (*github.PullRequest, error) {
	scope := c.StatsScope.SubScope("get_pull_request")
	scope = common.SetGitScopeTags(scope, repo.FullName, pullNum)

	executionTime := scope.Timer(metrics.ExecutionTimeMetric).Start()
	defer executionTime.Stop()

	executionSuccess := scope.Counter(metrics.ExecutionSuccessMetric)
	executionError := scope.Counter(metrics.ExecutionErrorMetric)

	pull, err := c.PullRequestGetter.GetPullRequest(logger, repo, pullNum)

	if err != nil {
		executionError.Inc(1)
		logger.Err("Unable to get pull number for repo, error: %s", err.Error())
	} else {
		executionSuccess.Inc(1)
	}

	return pull, err

}
