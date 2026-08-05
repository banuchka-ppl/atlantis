// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/runatlantis/atlantis/server/logging"
	tally "github.com/uber-go/tally/v4"
)

const (
	commandCompletionRoute                = "/v1/command-completions"
	defaultCommandCompletionQueueSize     = 256
	defaultCommandCompletionAttempts      = 5
	defaultCommandCompletionTokenBytes    = 4096
	defaultCommandCompletionResponseBytes = 4096
)

const (
	defaultCommandCompletionRequestTimeout = time.Second
	defaultCommandCompletionInitialBackoff = 100 * time.Millisecond
	defaultCommandCompletionMaximumBackoff = time.Second
)

type UDSCommandCompletionPublisherConfig struct {
	SocketPath       string
	TokenFilePath    string
	QueueCapacity    int
	RequestTimeout   time.Duration
	MaxAttempts      int
	InitialBackoff   time.Duration
	MaximumBackoff   time.Duration
	MaxResponseBytes int64
	Logger           logging.SimpleLogging
	Scope            tally.Scope
}

type commandCompletionDelivery struct {
	body           []byte
	idempotencyKey string
	command        string
	trigger        string
}

type commandCompletionDeliveryResult string

const (
	commandCompletionDeliveryAcknowledged commandCompletionDeliveryResult = "acknowledged"
	commandCompletionDeliveryRetryable    commandCompletionDeliveryResult = "retryable"
	commandCompletionDeliveryPermanent    commandCompletionDeliveryResult = "permanent_rejection"
	commandCompletionDeliveryExhausted    commandCompletionDeliveryResult = "retry_exhausted"
)

type UDSCommandCompletionPublisher struct {
	config    UDSCommandCompletionPublisherConfig
	client    *http.Client
	transport *http.Transport
	queue     chan commandCompletionDelivery
	stop      chan struct{}
	done      chan struct{}

	mu           sync.Mutex
	started      bool
	accepting    bool
	closed       bool
	workerCancel context.CancelFunc

	wait   func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

func NewUDSCommandCompletionPublisher(config UDSCommandCompletionPublisherConfig) (*UDSCommandCompletionPublisher, error) {
	if strings.TrimSpace(config.SocketPath) == "" {
		return nil, errors.New("command completion Unix socket path is required")
	}
	if strings.TrimSpace(config.TokenFilePath) == "" {
		return nil, errors.New("command completion token file path is required")
	}
	if config.QueueCapacity == 0 {
		config.QueueCapacity = defaultCommandCompletionQueueSize
	}
	if config.QueueCapacity < 1 {
		return nil, errors.New("command completion queue capacity must be positive")
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = defaultCommandCompletionRequestTimeout
	}
	if config.RequestTimeout < 1 {
		return nil, errors.New("command completion request timeout must be positive")
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = defaultCommandCompletionAttempts
	}
	if config.MaxAttempts < 1 {
		return nil, errors.New("command completion maximum attempts must be positive")
	}
	if config.InitialBackoff == 0 {
		config.InitialBackoff = defaultCommandCompletionInitialBackoff
	}
	if config.MaximumBackoff == 0 {
		config.MaximumBackoff = defaultCommandCompletionMaximumBackoff
	}
	if config.InitialBackoff < 1 || config.MaximumBackoff < config.InitialBackoff {
		return nil, errors.New("command completion backoff bounds are invalid")
	}
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = defaultCommandCompletionResponseBytes
	}
	if config.MaxResponseBytes < 1 {
		return nil, errors.New("command completion response bound must be positive")
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", config.SocketPath)
		},
		MaxIdleConns:        1,
		MaxIdleConnsPerHost: 1,
	}
	publisher := &UDSCommandCompletionPublisher{
		config:    config,
		transport: transport,
		client: &http.Client{
			Transport: transport,
			Timeout:   config.RequestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		queue: make(chan commandCompletionDelivery, config.QueueCapacity),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
		wait:  waitForCommandCompletionRetry,
		jitter: func(delay time.Duration) time.Duration {
			return time.Duration(rand.Int64N(int64(delay/2) + 1))
		},
	}
	return publisher, nil
}

func (p *UDSCommandCompletionPublisher) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("starting closed command completion publisher")
	}
	if p.started {
		return nil
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	p.workerCancel = cancel
	p.started = true
	p.accepting = true
	go p.run(workerCtx)
	return nil
}

func (p *UDSCommandCompletionPublisher) Publish(completion CommandCompletionV1) CommandCompletionPublishOutcome {
	body, err := EncodeCommandCompletion(completion)
	if err != nil {
		p.recordPublish(completion, CommandCompletionPublishClosed)
		return CommandCompletionPublishClosed
	}
	delivery := commandCompletionDelivery{
		body:           body,
		idempotencyKey: completion.CommandRunID,
		command:        completion.Command.Name,
		trigger:        completion.Command.Trigger,
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started || !p.accepting || p.closed {
		p.recordPublish(completion, CommandCompletionPublishClosed)
		return CommandCompletionPublishClosed
	}
	select {
	case p.queue <- delivery:
		p.recordPublish(completion, CommandCompletionPublishAccepted)
		return CommandCompletionPublishAccepted
	default:
		p.recordPublish(completion, CommandCompletionPublishQueueFull)
		return CommandCompletionPublishQueueFull
	}
}

func (p *UDSCommandCompletionPublisher) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	if !p.started {
		p.closed = true
		p.mu.Unlock()
		return nil
	}
	if !p.closed {
		p.accepting = false
		p.closed = true
		close(p.stop)
	}
	done := p.done
	cancel := p.workerCancel
	p.mu.Unlock()

	select {
	case <-done:
		p.transport.CloseIdleConnections()
		return nil
	case <-ctx.Done():
		cancel()
		p.transport.CloseIdleConnections()
		return ctx.Err()
	}
}

func (*UDSCommandCompletionPublisher) commandCompletionEnabled() bool { return true }

func (p *UDSCommandCompletionPublisher) run(ctx context.Context) {
	defer close(p.done)
	for {
		select {
		case delivery := <-p.queue:
			p.deliver(ctx, delivery)
		case <-p.stop:
			p.drain(ctx)
			return
		case <-ctx.Done():
			return
		}
	}
}

func (p *UDSCommandCompletionPublisher) drain(ctx context.Context) {
	for {
		select {
		case delivery := <-p.queue:
			p.deliver(ctx, delivery)
		case <-ctx.Done():
			return
		default:
			return
		}
	}
}

func (p *UDSCommandCompletionPublisher) deliver(ctx context.Context, delivery commandCompletionDelivery) {
	delay := p.config.InitialBackoff
	for attempt := 1; attempt <= p.config.MaxAttempts; attempt++ {
		result := p.send(ctx, delivery)
		p.recordDelivery(delivery, result, attempt)
		if result == commandCompletionDeliveryAcknowledged || result == commandCompletionDeliveryPermanent {
			return
		}
		if attempt == p.config.MaxAttempts {
			p.recordDelivery(delivery, commandCompletionDeliveryExhausted, attempt)
			if p.config.Logger != nil {
				p.config.Logger.Warn("command completion delivery exhausted retries for command %q trigger %q", delivery.command, delivery.trigger)
			}
			return
		}
		if err := p.wait(ctx, delay+p.jitter(delay)); err != nil {
			return
		}
		delay = min(delay*2, p.config.MaximumBackoff)
	}
}

func (p *UDSCommandCompletionPublisher) send(ctx context.Context, delivery commandCompletionDelivery) commandCompletionDeliveryResult {
	token, err := readCommandCompletionToken(p.config.TokenFilePath)
	if err != nil {
		return commandCompletionDeliveryRetryable
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"http://unix"+commandCompletionRoute,
		bytes.NewReader(delivery.body),
	)
	if err != nil {
		return commandCompletionDeliveryPermanent
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", delivery.idempotencyKey)
	response, err := p.client.Do(request)
	if err != nil {
		return commandCompletionDeliveryRetryable
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, p.config.MaxResponseBytes))
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return commandCompletionDeliveryAcknowledged
	}
	if response.StatusCode == http.StatusRequestTimeout ||
		response.StatusCode == http.StatusTooEarly ||
		response.StatusCode == http.StatusTooManyRequests ||
		response.StatusCode >= http.StatusInternalServerError {
		return commandCompletionDeliveryRetryable
	}
	return commandCompletionDeliveryPermanent
}

func readCommandCompletionToken(tokenPath string) (string, error) {
	pathInfo, err := os.Lstat(tokenPath)
	if err != nil {
		return "", fmt.Errorf("inspecting command completion token: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return "", errors.New("command completion token must be a regular file")
	}
	if pathInfo.Mode().Perm()&0077 != 0 {
		return "", errors.New("command completion token file must not be accessible by group or other")
	}
	if pathInfo.Size() > defaultCommandCompletionTokenBytes {
		return "", fmt.Errorf("command completion token exceeds maximum size of %d bytes", defaultCommandCompletionTokenBytes)
	}
	file, err := os.Open(tokenPath)
	if err != nil {
		return "", fmt.Errorf("opening command completion token: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspecting opened command completion token: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		return "", errors.New("command completion token changed during validation")
	}
	content, err := io.ReadAll(io.LimitReader(file, defaultCommandCompletionTokenBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading command completion token: %w", err)
	}
	if len(content) > defaultCommandCompletionTokenBytes {
		return "", fmt.Errorf("command completion token exceeds maximum size of %d bytes", defaultCommandCompletionTokenBytes)
	}
	token := strings.TrimSpace(string(content))
	if token == "" || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return "", errors.New("command completion token must be non-empty and contain no whitespace")
	}
	return token, nil
}

func waitForCommandCompletionRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *UDSCommandCompletionPublisher) recordPublish(completion CommandCompletionV1, outcome CommandCompletionPublishOutcome) {
	if p.config.Scope == nil {
		return
	}
	p.config.Scope.Tagged(map[string]string{
		"command": completion.Command.Name,
		"trigger": completion.Command.Trigger,
		"outcome": string(outcome),
	}).Counter("publish").Inc(1)
}

func (p *UDSCommandCompletionPublisher) recordDelivery(delivery commandCompletionDelivery, outcome commandCompletionDeliveryResult, attempt int) {
	if p.config.Scope == nil {
		return
	}
	p.config.Scope.Tagged(map[string]string{
		"attempt": strconv.Itoa(attempt),
		"command": delivery.command,
		"trigger": delivery.trigger,
		"outcome": string(outcome),
	}).Counter("delivery").Inc(1)
}
