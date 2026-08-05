// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"testing"

	"github.com/runatlantis/atlantis/server/events"
	. "github.com/runatlantis/atlantis/testing"
)

func TestParseCommandCompletionConfigKeepsOffModeDormant(t *testing.T) {
	config, err := events.ParseCommandCompletionConfig("", "", "", "")
	Ok(t, err)
	Equals(t, events.CommandCompletionModeOff, config.Mode)
	Equals(t, []string(nil), config.RepositoryAllowlist)
}

func TestParseCommandCompletionConfigRequiresBoundedShadowInputs(t *testing.T) {
	valid, err := events.ParseCommandCompletionConfig(
		"shadow",
		"ppl-ai/agi,ppl-ai/space",
		"/run/atlantis-command-completion/server.sock",
		"/run/atlantis-command-completion/token",
	)
	Ok(t, err)
	Equals(t, events.CommandCompletionModeShadow, valid.Mode)
	Equals(t, []string{"ppl-ai/agi", "ppl-ai/space"}, valid.RepositoryAllowlist)

	invalid := []struct {
		mode       string
		repos      string
		socketPath string
		tokenPath  string
	}{
		{mode: "required", repos: "ppl-ai/agi", socketPath: "/run/server.sock", tokenPath: "/run/token"},
		{mode: "shadow", socketPath: "/run/server.sock", tokenPath: "/run/token"},
		{mode: "shadow", repos: "ppl-ai/*", socketPath: "/run/server.sock", tokenPath: "/run/token"},
		{mode: "shadow", repos: "ppl-ai/agi", socketPath: "relative.sock", tokenPath: "/run/token"},
		{mode: "shadow", repos: "ppl-ai/agi", socketPath: "/run/server.sock", tokenPath: "relative-token"},
		{mode: "shadow", repos: "ppl-ai/agi", socketPath: "/run/shared", tokenPath: "/run/shared"},
	}
	for _, test := range invalid {
		_, err := events.ParseCommandCompletionConfig(test.mode, test.repos, test.socketPath, test.tokenPath)
		Assert(t, err != nil, "expected invalid config %#v to be rejected", test)
	}
}
