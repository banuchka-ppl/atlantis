// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const maxCommandCompletionSocketPathBytes = 103

var commandCompletionRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type CommandCompletionMode string

const (
	CommandCompletionModeOff    CommandCompletionMode = "off"
	CommandCompletionModeShadow CommandCompletionMode = "shadow"
)

type CommandCompletionConfig struct {
	Mode                CommandCompletionMode
	RepositoryAllowlist []string
	SocketPath          string
	TokenFilePath       string
}

func ParseCommandCompletionConfig(
	modeValue string,
	repositoryAllowlistValue string,
	socketPath string,
	tokenFilePath string,
) (CommandCompletionConfig, error) {
	mode, err := parseCommandCompletionMode(modeValue)
	if err != nil {
		return CommandCompletionConfig{}, err
	}
	repositories, err := parseCommandCompletionRepositoryAllowlist(repositoryAllowlistValue)
	if err != nil {
		return CommandCompletionConfig{}, err
	}
	config := CommandCompletionConfig{
		Mode:                mode,
		RepositoryAllowlist: repositories,
		SocketPath:          socketPath,
		TokenFilePath:       tokenFilePath,
	}
	if mode == CommandCompletionModeOff {
		return config, nil
	}
	if len(repositories) == 0 {
		return CommandCompletionConfig{}, errors.New("command completion shadow mode requires an explicit repository allowlist")
	}
	if err := validateCommandCompletionAbsolutePath(socketPath, "Unix socket"); err != nil {
		return CommandCompletionConfig{}, err
	}
	if len(socketPath) > maxCommandCompletionSocketPathBytes {
		return CommandCompletionConfig{}, fmt.Errorf(
			"command completion Unix socket path exceeds %d bytes",
			maxCommandCompletionSocketPathBytes,
		)
	}
	if err := validateCommandCompletionAbsolutePath(tokenFilePath, "token file"); err != nil {
		return CommandCompletionConfig{}, err
	}
	if socketPath == tokenFilePath {
		return CommandCompletionConfig{}, errors.New("command completion Unix socket and token file paths must differ")
	}
	return config, nil
}

func parseCommandCompletionMode(value string) (CommandCompletionMode, error) {
	switch CommandCompletionMode(value) {
	case "", CommandCompletionModeOff:
		return CommandCompletionModeOff, nil
	case CommandCompletionModeShadow:
		return CommandCompletionModeShadow, nil
	default:
		return "", fmt.Errorf("invalid command completion mode %q: must be one of [off shadow]", value)
	}
}

func parseCommandCompletionRepositoryAllowlist(value string) ([]string, error) {
	var repositories []string
	seen := make(map[string]struct{})
	for _, rawRepository := range strings.Split(value, ",") {
		repository := strings.TrimSpace(rawRepository)
		if repository == "" {
			continue
		}
		if !commandCompletionRepositoryPattern.MatchString(repository) {
			return nil, fmt.Errorf(
				"invalid command completion repository %q: expected exact owner/name",
				repository,
			)
		}
		if _, ok := seen[repository]; ok {
			continue
		}
		seen[repository] = struct{}{}
		repositories = append(repositories, repository)
	}
	return repositories, nil
}

func validateCommandCompletionAbsolutePath(value string, description string) error {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return fmt.Errorf("command completion %s path must be absolute and clean", description)
	}
	return nil
}
