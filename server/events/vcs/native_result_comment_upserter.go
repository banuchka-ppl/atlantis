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

var ErrNativeResultTrailerCommentUnsupported = errors.New("native result trailer comment unsupported")

// NativeResultTrailerCommenter posts a rendered command result with a native
// result marker trailer, guaranteeing the marker rides intact on the final
// comment instead of being sliced apart by VCS comment-length splitting.
type NativeResultTrailerCommenter interface {
	CreateCommentWithNativeResultTrailer(logger logging.SimpleLogging, repo models.Repo, pullNum int, comment string, command string, marker string) error
}
