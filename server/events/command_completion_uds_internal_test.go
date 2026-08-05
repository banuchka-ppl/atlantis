// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

const commandCompletionTestToken = "test-per-pod-token"

type receivedCommandCompletionRequest struct {
	method         string
	path           string
	authorization  string
	idempotencyKey string
	contentType    string
	body           []byte
}

func TestUDSCommandCompletionPublisherSendsAuthenticatedImmutableRequest(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "command-completion.sock")
	tokenPath := filepath.Join(tempDir, "token")
	Ok(t, os.WriteFile(tokenPath, []byte(commandCompletionTestToken+"\n"), 0600))
	listener, err := net.Listen("unix", socketPath)
	Ok(t, err)
	received := make(chan receivedCommandCompletionRequest, 1)
	httpServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		Ok(t, readErr)
		received <- receivedCommandCompletionRequest{
			method:         r.Method,
			path:           r.URL.Path,
			authorization:  r.Header.Get("Authorization"),
			idempotencyKey: r.Header.Get("Idempotency-Key"),
			contentType:    r.Header.Get("Content-Type"),
			body:           body,
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	go httpServer.Serve(listener) // nolint: errcheck
	t.Cleanup(func() { httpServer.Close() })

	publisher, err := NewUDSCommandCompletionPublisher(UDSCommandCompletionPublisherConfig{
		SocketPath:    socketPath,
		TokenFilePath: tokenPath,
		Logger:        logging.NewNoopLogger(t),
	})
	Ok(t, err)
	Ok(t, publisher.Start())
	completion := commandCompletionForTransport(t)
	expectedBody, err := EncodeCommandCompletion(completion)
	Ok(t, err)
	Equals(t, CommandCompletionPublishAccepted, publisher.Publish(completion))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	Ok(t, publisher.Shutdown(shutdownCtx))

	request := <-received
	Equals(t, http.MethodPost, request.method)
	Equals(t, "/v1/command-completions", request.path)
	Equals(t, "Bearer "+commandCompletionTestToken, request.authorization)
	Equals(t, completion.CommandRunID, request.idempotencyKey)
	Equals(t, "application/json", request.contentType)
	Equals(t, expectedBody, request.body)
}

func TestUDSCommandCompletionPublisherRetriesByteIdenticalRequest(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "command-completion.sock")
	tokenPath := filepath.Join(tempDir, "token")
	Ok(t, os.WriteFile(tokenPath, []byte(commandCompletionTestToken), 0600))
	requests := make(chan receivedCommandCompletionRequest, 2)
	var requestCount atomic.Int32
	server := startCommandCompletionTestServer(t, socketPath, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		requests <- receivedCommandCompletionRequest{
			idempotencyKey: r.Header.Get("Idempotency-Key"),
			body:           body,
		}
		if requestCount.Add(1) == 1 {
			http.Error(w, "retry", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	t.Cleanup(func() { server.Close() })

	publisher, err := NewUDSCommandCompletionPublisher(UDSCommandCompletionPublisherConfig{
		SocketPath:     socketPath,
		TokenFilePath:  tokenPath,
		MaxAttempts:    2,
		InitialBackoff: time.Nanosecond,
		MaximumBackoff: time.Nanosecond,
		Logger:         logging.NewNoopLogger(t),
	})
	Ok(t, err)
	publisher.wait = func(context.Context, time.Duration) error { return nil }
	publisher.jitter = func(time.Duration) time.Duration { return 0 }
	Ok(t, publisher.Start())
	completion := commandCompletionForTransport(t)
	Equals(t, CommandCompletionPublishAccepted, publisher.Publish(completion))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	Ok(t, publisher.Shutdown(shutdownCtx))

	first := <-requests
	second := <-requests
	Equals(t, first.idempotencyKey, second.idempotencyKey)
	Assert(t, bytes.Equal(first.body, second.body), "retry body changed between delivery attempts")
}

func TestUDSCommandCompletionPublisherClassifiesResponseStatuses(t *testing.T) {
	testCases := []struct {
		status           int
		expectedRequests int32
	}{
		{status: http.StatusBadRequest, expectedRequests: 1},
		{status: http.StatusUnauthorized, expectedRequests: 1},
		{status: http.StatusRequestTimeout, expectedRequests: 2},
		{status: http.StatusTooEarly, expectedRequests: 2},
		{status: http.StatusTooManyRequests, expectedRequests: 2},
		{status: http.StatusInternalServerError, expectedRequests: 2},
	}
	for _, testCase := range testCases {
		t.Run(http.StatusText(testCase.status), func(t *testing.T) {
			tempDir := t.TempDir()
			socketPath := filepath.Join(tempDir, "command-completion.sock")
			tokenPath := filepath.Join(tempDir, "token")
			Ok(t, os.WriteFile(tokenPath, []byte(commandCompletionTestToken), 0600))
			var requestCount atomic.Int32
			server := startCommandCompletionTestServer(t, socketPath, func(w http.ResponseWriter, _ *http.Request) {
				requestCount.Add(1)
				w.WriteHeader(testCase.status)
			})
			t.Cleanup(func() { server.Close() })

			publisher, err := NewUDSCommandCompletionPublisher(UDSCommandCompletionPublisherConfig{
				SocketPath:     socketPath,
				TokenFilePath:  tokenPath,
				MaxAttempts:    2,
				InitialBackoff: time.Nanosecond,
				MaximumBackoff: time.Nanosecond,
				Logger:         logging.NewNoopLogger(t),
			})
			Ok(t, err)
			publisher.wait = func(context.Context, time.Duration) error { return nil }
			publisher.jitter = func(time.Duration) time.Duration { return 0 }
			Ok(t, publisher.Start())
			Equals(t, CommandCompletionPublishAccepted, publisher.Publish(commandCompletionForTransport(t)))
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			Ok(t, publisher.Shutdown(shutdownCtx))
			Equals(t, testCase.expectedRequests, requestCount.Load())
		})
	}
}

func TestUDSCommandCompletionPublisherDoesNotRetryPermanentResponseOrFollowRedirect(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "command-completion.sock")
	tokenPath := filepath.Join(tempDir, "token")
	Ok(t, os.WriteFile(tokenPath, []byte(commandCompletionTestToken), 0600))
	var originalRequests atomic.Int32
	var redirectedRequests atomic.Int32
	server := startCommandCompletionTestServer(t, socketPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirectedRequests.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		originalRequests.Add(1)
		http.Redirect(w, r, "/redirected", http.StatusFound)
	})
	t.Cleanup(func() { server.Close() })

	publisher, err := NewUDSCommandCompletionPublisher(UDSCommandCompletionPublisherConfig{
		SocketPath:     socketPath,
		TokenFilePath:  tokenPath,
		MaxAttempts:    3,
		InitialBackoff: time.Nanosecond,
		MaximumBackoff: time.Nanosecond,
		Logger:         logging.NewNoopLogger(t),
	})
	Ok(t, err)
	publisher.wait = func(context.Context, time.Duration) error { return nil }
	publisher.jitter = func(time.Duration) time.Duration { return 0 }
	Ok(t, publisher.Start())
	Equals(t, CommandCompletionPublishAccepted, publisher.Publish(commandCompletionForTransport(t)))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	Ok(t, publisher.Shutdown(shutdownCtx))
	Equals(t, int32(1), originalRequests.Load())
	Equals(t, int32(0), redirectedRequests.Load())
}

func TestUDSCommandCompletionPublisherQueueAndShutdownAreBounded(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "command-completion.sock")
	tokenPath := filepath.Join(tempDir, "token")
	Ok(t, os.WriteFile(tokenPath, []byte(commandCompletionTestToken), 0600))
	requestStarted := make(chan struct{}, 1)
	releaseHandler := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHandler) }) }
	server := startCommandCompletionTestServer(t, socketPath, func(_ http.ResponseWriter, _ *http.Request) {
		requestStarted <- struct{}{}
		<-releaseHandler
	})
	t.Cleanup(func() {
		release()
		server.Close()
	})

	publisher, err := NewUDSCommandCompletionPublisher(UDSCommandCompletionPublisherConfig{
		SocketPath:     socketPath,
		TokenFilePath:  tokenPath,
		QueueCapacity:  1,
		RequestTimeout: time.Second,
		MaxAttempts:    1,
		Logger:         logging.NewNoopLogger(t),
	})
	Ok(t, err)
	Ok(t, publisher.Start())
	completion := commandCompletionForTransport(t)
	Equals(t, CommandCompletionPublishAccepted, publisher.Publish(completion))
	<-requestStarted
	Equals(t, CommandCompletionPublishAccepted, publisher.Publish(completion))
	Equals(t, CommandCompletionPublishQueueFull, publisher.Publish(completion))

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = publisher.Shutdown(shutdownCtx)
	Assert(t, errors.Is(err, context.DeadlineExceeded), "expected bounded shutdown deadline, got %v", err)
	Equals(t, CommandCompletionPublishClosed, publisher.Publish(completion))
	release()
	select {
	case <-publisher.done:
	case <-time.After(time.Second):
		t.Fatal("publisher worker remained live after bounded shutdown")
	}
}

func TestUDSCommandCompletionPublisherTreatsMissingOrUnsafeEndpointsAsRetryable(t *testing.T) {
	tempDir := t.TempDir()
	missingPath := filepath.Join(tempDir, "missing")
	secureTokenPath := filepath.Join(tempDir, "secure-token")
	worldReadableTokenPath := filepath.Join(tempDir, "world-token")
	symlinkTokenPath := filepath.Join(tempDir, "symlink-token")
	Ok(t, os.WriteFile(secureTokenPath, []byte(commandCompletionTestToken), 0600))
	Ok(t, os.WriteFile(worldReadableTokenPath, []byte(commandCompletionTestToken), 0644))
	Ok(t, os.Chmod(worldReadableTokenPath, 0644))
	Ok(t, os.Symlink(secureTokenPath, symlinkTokenPath))

	testCases := []struct {
		name       string
		socketPath string
		tokenPath  string
	}{
		{name: "missing token", socketPath: missingPath, tokenPath: missingPath},
		{name: "missing socket", socketPath: missingPath, tokenPath: secureTokenPath},
		{name: "world-readable token", socketPath: missingPath, tokenPath: worldReadableTokenPath},
		{name: "symlink token", socketPath: missingPath, tokenPath: symlinkTokenPath},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			publisher, err := NewUDSCommandCompletionPublisher(UDSCommandCompletionPublisherConfig{
				SocketPath:    testCase.socketPath,
				TokenFilePath: testCase.tokenPath,
				MaxAttempts:   1,
				Logger:        logging.NewNoopLogger(t),
			})
			Ok(t, err)
			Ok(t, publisher.Start())
			completion := commandCompletionForTransport(t)
			Equals(t, CommandCompletionPublishAccepted, publisher.Publish(completion))
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			Ok(t, publisher.Shutdown(shutdownCtx))
		})
	}
}

func startCommandCompletionTestServer(
	t *testing.T,
	socketPath string,
	handler http.HandlerFunc,
) *http.Server {
	t.Helper()
	listener, err := net.Listen("unix", socketPath)
	Ok(t, err)
	server := &http.Server{Handler: handler}
	go server.Serve(listener) // nolint: errcheck
	return server
}

func commandCompletionForTransport(t *testing.T) CommandCompletionV1 {
	t.Helper()
	ctx := commandCompletionContextForFinalizer(t)
	ctx.CommandRunID = finalizerRunID
	ctx.CommandStartedAt = time.Date(2026, time.August, 5, 15, 0, 0, 0, time.UTC)
	completion, err := BuildCommandCompletion(
		ctx,
		&CommentCommand{Name: command.Plan},
		command.Result{ProjectResults: []command.ProjectResult{successfulTypedPlanResult()}},
		VCSResultPublication{
			State:                   VCSResultPublicationSucceeded,
			Action:                  VCSResultPublicationCreated,
			CommentIDs:              []int64{1234},
			RootCommentID:           1234,
			TerminalCommentID:       1234,
			NativeResultMarkerState: NativeResultMarkerPublished,
		},
		ctx.CommandStartedAt.Add(time.Minute),
	)
	Ok(t, err)
	return completion
}
