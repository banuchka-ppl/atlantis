// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package vcs

import (
	"errors"

	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
)

var ErrNativeResultCommentUpsertUnsupported = errors.New("native result comment upsert unsupported")

type NativeResultCommentAction string

const (
	NativeResultCommentCreated NativeResultCommentAction = "created"
	NativeResultCommentUpdated NativeResultCommentAction = "updated"
)

type NativeResultMarkerState string

const (
	NativeResultMarkerPublished    NativeResultMarkerState = "published"
	NativeResultMarkerNotPublished NativeResultMarkerState = "not_published"
	NativeResultMarkerNotRequested NativeResultMarkerState = "not_requested"
)

// NativeResultCommentPublication contains only durable facts returned by the
// VCS. CommentIDs includes IDs created before a later split fragment failed.
type NativeResultCommentPublication struct {
	Action                  NativeResultCommentAction
	CommentIDs              []int64
	RootCommentID           int64
	TerminalCommentID       int64
	NativeResultMarkerState NativeResultMarkerState
}

type NativeResultCommentUpserter interface {
	UpsertNativeResultComment(logger logging.SimpleLogging, repo models.Repo, pullNum int, comment string, command string, marker string) (NativeResultCommentPublication, error)
}

var ErrNativeResultTrailerCommentUnsupported = errors.New("native result trailer comment unsupported")

// NativeResultTrailerCommenter posts a rendered command result with a native
// result marker trailer, guaranteeing the marker rides intact on the final
// comment instead of being sliced apart by VCS comment-length splitting.
type NativeResultTrailerCommenter interface {
	CreateCommentWithNativeResultTrailer(logger logging.SimpleLogging, repo models.Repo, pullNum int, comment string, command string, marker string) (NativeResultCommentPublication, error)
}
