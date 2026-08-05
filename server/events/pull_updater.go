// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"errors"
	"slices"
	"strings"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/vcs"
)

type PullUpdater struct {
	HidePrevPlanComments              bool
	NativeResultCommentMarkersEnabled bool
	NativeResultCommentUpsertEnabled  bool
	ResultPublicationFactsEnabled     bool
	VCSClient                         vcs.Client
	MarkdownRenderer                  *MarkdownRenderer
}

func (c *PullUpdater) updatePull(ctx *command.Context, cmd PullCommand, res command.Result) VCSResultPublication {
	// Log if we got any errors or failures.
	if res.Error != nil {
		ctx.Log.Err("%s", res.Error.Error())
	} else if res.Failure != "" {
		ctx.Log.Warn("%s", res.Failure)
	}

	// HidePrevCommandComments will hide old comments left from previous runs to reduce
	// clutter in a pull/merge request. This will not delete the comment, since the
	// comment trail may be useful in auditing or backtracing problems.
	if c.HidePrevPlanComments {
		ctx.Log.Debug("hiding previous plan comments for command: '%v', directory: '%v'", cmd.CommandName().TitleString(), cmd.Dir())
		if err := c.VCSClient.HidePrevCommandComments(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num, cmd.CommandName().TitleString(), cmd.Dir()); err != nil {
			ctx.Log.Err("unable to hide old comments: %s", err)
		}
	}

	if len(res.ProjectResults) > 0 {
		var commentOnProjects []command.ProjectResult
		for _, result := range res.ProjectResults {
			if slices.Contains(result.SilencePRComments, cmd.CommandName().String()) {
				ctx.Log.Debug("silenced command '%s' comment for project '%s'", cmd.CommandName().String(), result.ProjectName)
				continue
			}
			commentOnProjects = append(commentOnProjects, result)
		}

		if len(commentOnProjects) == 0 {
			return VCSResultPublication{
				State:                   VCSResultPublicationSuppressed,
				Action:                  VCSResultPublicationNone,
				NativeResultMarkerState: NativeResultMarkerNotRequested,
			}
		}

		res.ProjectResults = commentOnProjects
	}

	comment := c.MarkdownRenderer.Render(ctx, res, cmd)
	shouldUpsertNativeResult := c.shouldUpsertNativeResultComment(cmd)
	var marker string
	if c.NativeResultCommentMarkersEnabled || shouldUpsertNativeResult {
		var err error
		marker, err = encodeNativeResultCommentMarker(ctx, cmd, res)
		if err != nil {
			ctx.Log.Err("unable to encode native result comment marker: %s", err)
		}
	}

	upsertUnsupported := false
	if shouldUpsertNativeResult && marker != "" {
		if upserter, ok := c.VCSClient.(vcs.NativeResultCommentUpserter); ok {
			nativePublication, err := upserter.UpsertNativeResultComment(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num, comment, cmd.CommandName().String(), marker)
			if err == nil {
				return vcsResultPublication(nativePublication, nil)
			}
			if errors.Is(err, vcs.ErrNativeResultCommentUpsertUnsupported) {
				upsertUnsupported = true
			} else if len(nativePublication.CommentIDs) > 0 {
				ctx.Log.Err("unable to upsert native result comment after partial publication: %s", err)
				return vcsResultPublication(nativePublication, err)
			} else {
				ctx.Log.Err("unable to upsert native result comment: %s", err)
			}
		} else {
			upsertUnsupported = true
			ctx.Log.Debug("native result comment upsert unsupported for VCS client")
		}
	}

	if marker != "" && (c.NativeResultCommentMarkersEnabled || !upsertUnsupported) {
		if commenter, ok := c.VCSClient.(vcs.NativeResultTrailerCommenter); ok {
			nativePublication, err := commenter.CreateCommentWithNativeResultTrailer(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num, comment, cmd.CommandName().String(), marker)
			if err == nil {
				return vcsResultPublication(nativePublication, nil)
			}
			if !errors.Is(err, vcs.ErrNativeResultTrailerCommentUnsupported) {
				// Do not retry through CreateComment: the trailer commenter may
				// have already posted part of the split comment chain.
				ctx.Log.Err("unable to comment: %s", err)
				return vcsResultPublication(nativePublication, err)
			}
		}
		comment = appendNativeResultCommentMarker(comment, marker)
	}
	if marker == "" && c.ResultPublicationFactsEnabled {
		if commenter, ok := c.VCSClient.(vcs.NativeResultTrailerCommenter); ok {
			nativePublication, err := commenter.CreateCommentWithNativeResultTrailer(
				ctx.Log,
				ctx.Pull.BaseRepo,
				ctx.Pull.Num,
				comment,
				cmd.CommandName().String(),
				"",
			)
			if err == nil {
				return vcsResultPublication(nativePublication, nil)
			}
			if !errors.Is(err, vcs.ErrNativeResultTrailerCommentUnsupported) {
				ctx.Log.Err("unable to comment: %s", err)
				return vcsResultPublication(nativePublication, err)
			}
		}
	}
	if err := c.VCSClient.CreateComment(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num, comment, cmd.CommandName().String()); err != nil {
		ctx.Log.Err("unable to comment: %s", err)
		return VCSResultPublication{
			State:                   VCSResultPublicationFailed,
			Action:                  VCSResultPublicationNone,
			NativeResultMarkerState: nativeResultMarkerState(marker, false),
		}
	}
	return VCSResultPublication{
		State:                   VCSResultPublicationSucceeded,
		Action:                  VCSResultPublicationCreated,
		NativeResultMarkerState: nativeResultMarkerState(marker, strings.Contains(comment, marker)),
	}
}

func (c *PullUpdater) shouldUpsertNativeResultComment(cmd PullCommand) bool {
	return c.NativeResultCommentUpsertEnabled && cmd.CommandName() == command.Plan && !cmd.IsAutoplan()
}

func vcsResultPublication(publication vcs.NativeResultCommentPublication, publicationErr error) VCSResultPublication {
	state := VCSResultPublicationSucceeded
	action := VCSResultPublicationAction(publication.Action)
	if publicationErr != nil && len(publication.CommentIDs) > 0 {
		state = VCSResultPublicationPartial
	}
	if publicationErr != nil && len(publication.CommentIDs) == 0 {
		state = VCSResultPublicationFailed
		action = VCSResultPublicationNone
	}
	return VCSResultPublication{
		State:                   state,
		Action:                  action,
		CommentIDs:              slices.Clone(publication.CommentIDs),
		RootCommentID:           publication.RootCommentID,
		TerminalCommentID:       publication.TerminalCommentID,
		NativeResultMarkerState: NativeResultMarkerState(publication.NativeResultMarkerState),
	}
}

func nativeResultMarkerState(marker string, published bool) NativeResultMarkerState {
	if marker == "" {
		return NativeResultMarkerNotRequested
	}
	if published {
		return NativeResultMarkerPublished
	}
	return NativeResultMarkerNotPublished
}
