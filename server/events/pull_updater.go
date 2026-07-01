// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"errors"
	"slices"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/vcs"
)

type PullUpdater struct {
	HidePrevPlanComments              bool
	NativeResultCommentMarkersEnabled bool
	NativeResultCommentUpsertEnabled  bool
	VCSClient                         vcs.Client
	MarkdownRenderer                  *MarkdownRenderer
}

func (c *PullUpdater) updatePull(ctx *command.Context, cmd PullCommand, res command.Result) {
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
			return
		}

		res.ProjectResults = commentOnProjects
	}

	comment := c.MarkdownRenderer.Render(ctx, res, cmd)
	shouldUpsertNativeResult := c.shouldUpsertNativeResultComment(cmd)
	var marker string
	if c.NativeResultCommentMarkersEnabled || shouldUpsertNativeResult {
		var err error
		marker, err = encodeNativeResultCommentMarker(ctx, cmd)
		if err != nil {
			ctx.Log.Err("unable to encode native result comment marker: %s", err)
		}
	}

	upsertUnsupported := false
	if shouldUpsertNativeResult && marker != "" {
		if upserter, ok := c.VCSClient.(vcs.NativeResultCommentUpserter); ok {
			err := upserter.UpsertNativeResultComment(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num, comment, cmd.CommandName().String(), marker)
			if err == nil {
				return
			}
			if errors.Is(err, vcs.ErrNativeResultCommentUpsertUnsupported) {
				upsertUnsupported = true
			} else {
				ctx.Log.Err("unable to upsert native result comment: %s", err)
			}
		} else {
			upsertUnsupported = true
			ctx.Log.Debug("native result comment upsert unsupported for VCS client")
		}
	}

	if marker != "" && (c.NativeResultCommentMarkersEnabled || !upsertUnsupported) {
		comment = appendNativeResultCommentMarker(comment, marker)
	}
	if err := c.VCSClient.CreateComment(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull.Num, comment, cmd.CommandName().String()); err != nil {
		ctx.Log.Err("unable to comment: %s", err)
	}
}

func (c *PullUpdater) shouldUpsertNativeResultComment(cmd PullCommand) bool {
	return c.NativeResultCommentUpsertEnabled && cmd.CommandName() == command.Plan
}
