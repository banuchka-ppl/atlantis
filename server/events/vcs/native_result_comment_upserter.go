// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package vcs

import (
	"errors"

	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
)

var ErrNativeResultCommentUpsertUnsupported = errors.New("native result comment upsert unsupported")

type NativeResultCommentUpserter interface {
	UpsertNativeResultComment(logger logging.SimpleLogging, repo models.Repo, pullNum int, comment string, command string, marker string) error
}
