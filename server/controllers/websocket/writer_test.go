// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package websocket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gorillawebsocket "github.com/gorilla/websocket"
	"github.com/runatlantis/atlantis/server/jobs"
	"github.com/runatlantis/atlantis/server/logging"
)

func TestWriterBatchesOutputAndSendsCompletion(t *testing.T) {
	input := make(chan jobs.ProjectOutputEvent, 3)
	input <- jobs.ProjectOutputEvent{Type: jobs.ProjectOutputEventOutput, Data: "first line"}
	input <- jobs.ProjectOutputEvent{Type: jobs.ProjectOutputEventOutput, Data: "second line"}
	input <- jobs.ProjectOutputEvent{Type: jobs.ProjectOutputEventComplete, Status: jobs.JobStatusSucceeded}
	close(input)

	writer := NewWriter(logging.NewNoopLogger(t), false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := writer.Write(w, r, input); err != nil {
			t.Errorf("writing websocket output: %v", err)
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := gorillawebsocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dialing websocket: %v", err)
	}
	defer conn.Close()

	messageType, output, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	if messageType != gorillawebsocket.BinaryMessage {
		t.Fatalf("expected binary output, got message type %d", messageType)
	}
	if string(output) != "\rfirst line\n\rsecond line\n" {
		t.Fatalf("unexpected output %q", output)
	}

	messageType, completionPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("reading completion: %v", err)
	}
	if messageType != gorillawebsocket.TextMessage {
		t.Fatalf("expected text completion, got message type %d", messageType)
	}
	var completion jobs.ProjectOutputEvent
	if err := json.Unmarshal(completionPayload, &completion); err != nil {
		t.Fatalf("decoding completion: %v", err)
	}
	if completion.Type != jobs.ProjectOutputEventComplete || completion.Status != jobs.JobStatusSucceeded {
		t.Fatalf("unexpected completion %#v", completion)
	}

	_, _, err = conn.ReadMessage()
	if !gorillawebsocket.IsCloseError(err, gorillawebsocket.CloseNormalClosure) {
		t.Fatalf("expected normal websocket closure, got %v", err)
	}
}
