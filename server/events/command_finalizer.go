// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/events/command"
)

type CommandCompletionPublishOutcome string

const (
	CommandCompletionPublishAccepted  CommandCompletionPublishOutcome = "accepted"
	CommandCompletionPublishDisabled  CommandCompletionPublishOutcome = "disabled"
	CommandCompletionPublishQueueFull CommandCompletionPublishOutcome = "queue_full"
	CommandCompletionPublishClosed    CommandCompletionPublishOutcome = "closed"
)

type CommandCompletionPublisher interface {
	Start() error
	Publish(CommandCompletionV1) CommandCompletionPublishOutcome
	Shutdown(context.Context) error
}

type commandCompletionPublisherActivation interface {
	commandCompletionEnabled() bool
}

// DisabledCommandCompletionPublisher is the default adapter. It deliberately
// owns no worker, queue, serializer, or filesystem resource.
type DisabledCommandCompletionPublisher struct{}

func (DisabledCommandCompletionPublisher) Start() error { return nil }

func (DisabledCommandCompletionPublisher) Publish(CommandCompletionV1) CommandCompletionPublishOutcome {
	return CommandCompletionPublishDisabled
}

func (DisabledCommandCompletionPublisher) Shutdown(context.Context) error { return nil }

func (DisabledCommandCompletionPublisher) commandCompletionEnabled() bool { return false }

// InMemoryCommandCompletionPublisher records immutable values for interface
// tests without involving the production transport.
type InMemoryCommandCompletionPublisher struct {
	mu      sync.Mutex
	started bool
	closed  bool
	events  []CommandCompletionV1
}

func (p *InMemoryCommandCompletionPublisher) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("starting closed command completion publisher")
	}
	p.started = true
	return nil
}

func (p *InMemoryCommandCompletionPublisher) Publish(completion CommandCompletionV1) CommandCompletionPublishOutcome {
	encoded, err := EncodeCommandCompletion(completion)
	if err != nil {
		return CommandCompletionPublishClosed
	}
	var immutable CommandCompletionV1
	if err := json.Unmarshal(encoded, &immutable); err != nil {
		return CommandCompletionPublishClosed
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started || p.closed {
		return CommandCompletionPublishClosed
	}
	p.events = append(p.events, immutable)
	return CommandCompletionPublishAccepted
}

func (p *InMemoryCommandCompletionPublisher) Shutdown(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *InMemoryCommandCompletionPublisher) Events() []CommandCompletionV1 {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]CommandCompletionV1, 0, len(p.events))
	for _, event := range p.events {
		encoded, err := json.Marshal(event)
		if err != nil {
			continue
		}
		var clone CommandCompletionV1
		if err := json.Unmarshal(encoded, &clone); err == nil {
			result = append(result, clone)
		}
	}
	return result
}

func (*InMemoryCommandCompletionPublisher) commandCompletionEnabled() bool { return true }

type CommandFinalizer struct {
	publisher           CommandCompletionPublisher
	repositoryAllowlist map[string]struct{}
	enabled             bool
	newRunID            func() (string, error)
	now                 func() time.Time
}

func NewCommandFinalizer(
	publisher CommandCompletionPublisher,
	repositoryAllowlist []string,
) *CommandFinalizer {
	enabled := publisher != nil
	if activation, ok := publisher.(commandCompletionPublisherActivation); ok {
		enabled = activation.commandCompletionEnabled()
	}
	allowlist := make(map[string]struct{}, len(repositoryAllowlist))
	for _, repository := range repositoryAllowlist {
		allowlist[repository] = struct{}{}
	}
	return &CommandFinalizer{
		publisher:           publisher,
		repositoryAllowlist: allowlist,
		enabled:             enabled,
		newRunID: func() (string, error) {
			id, err := uuid.NewV7()
			return id.String(), err
		},
		now: time.Now,
	}
}

// Begin allocates command-wide identity before project execution. Repeated
// calls are harmless so wrappers may safely share this seam.
func (f *CommandFinalizer) Begin(ctx *command.Context, cmd PullCommand) {
	if !f.isEligible(ctx, cmd) || ctx.CommandRunID != "" {
		return
	}
	runID, err := f.newRunID()
	if err != nil {
		ctx.Log.Warn("unable to allocate command completion identity: %s", err)
		return
	}
	ctx.CommandRunID = runID
	ctx.CommandStartedAt = f.now().UTC()
}

// Finalize builds one immutable aggregate event after VCS result publication.
// Delivery outcomes are operational only and never mutate execution state.
func (f *CommandFinalizer) Finalize(
	ctx *command.Context,
	cmd PullCommand,
	result command.Result,
	publication VCSResultPublication,
) {
	if f == nil || !f.enabled || ctx == nil || ctx.CommandRunID == "" {
		return
	}
	completion, err := BuildCommandCompletion(ctx, cmd, result, publication, f.now())
	if err != nil {
		ctx.Log.Warn("not publishing invalid command completion: %s", err)
		return
	}
	outcome := f.publisher.Publish(completion)
	if outcome == CommandCompletionPublishAccepted || outcome == CommandCompletionPublishDisabled {
		return
	}
	ctx.Log.Warn("command completion publisher returned %q", outcome)
}

func (f *CommandFinalizer) isEligible(ctx *command.Context, cmd PullCommand) bool {
	if f == nil || !f.enabled || f.publisher == nil || ctx == nil || cmd == nil {
		return false
	}
	if _, ok := f.repositoryAllowlist[ctx.Pull.BaseRepo.FullName]; !ok {
		return false
	}
	if ctx.Pull.Num <= 0 || !exactCommitPattern.MatchString(ctx.Pull.HeadCommit) {
		return false
	}
	return cmd.CommandName() == command.Plan || cmd.CommandName() == command.Apply
}
