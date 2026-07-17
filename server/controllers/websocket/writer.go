// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package websocket

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/runatlantis/atlantis/server/jobs"
	"github.com/runatlantis/atlantis/server/logging"
)

const (
	outputFlushInterval = 25 * time.Millisecond
	maxOutputBatchSize  = 32 * 1024
)

func NewWriter(log logging.SimpleLogging, checkOrigin bool) *Writer {
	upgrader := websocket.Upgrader{
		CheckOrigin: checkOriginFunc(checkOrigin),
	}
	upgrader.CheckOrigin = func(r *http.Request) bool { return true }
	return &Writer{
		upgrader: upgrader,
		log:      log,
	}
}

type Writer struct {
	upgrader websocket.Upgrader
	log      logging.SimpleLogging
}

func (w *Writer) Write(rw http.ResponseWriter, r *http.Request, input chan jobs.ProjectOutputEvent) error {
	conn, err := w.upgrader.Upgrade(rw, r, nil)

	if err != nil {
		return fmt.Errorf("upgrading websocket connection: %w", err)
	}

	flushTicker := time.NewTicker(outputFlushInterval)
	defer flushTicker.Stop()

	var output strings.Builder
	status := jobs.JobStatusFinished

	flushOutput := func() error {
		if output.Len() == 0 {
			return nil
		}
		message := output.String()
		output.Reset()
		return conn.WriteMessage(websocket.BinaryMessage, []byte(message))
	}

	for {
		select {
		case event, ok := <-input:
			if !ok {
				if err := flushOutput(); err != nil {
					return fmt.Errorf("flushing ws output: %w", err)
				}
				if err := conn.WriteJSON(jobs.ProjectOutputEvent{Type: jobs.ProjectOutputEventComplete, Status: status}); err != nil {
					return fmt.Errorf("writing ws completion: %w", err)
				}
				if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "job complete")); err != nil {
					w.log.Warn("Failed to write ws close message: %s", err)
				}
				if err := conn.Close(); err != nil {
					w.log.Warn("Failed to close ws connection: %s", err)
				}
				return nil
			}

			switch event.Type {
			case jobs.ProjectOutputEventOutput:
				output.WriteString("\r")
				output.WriteString(event.Data)
				output.WriteString("\n")
				if output.Len() >= maxOutputBatchSize {
					if err := flushOutput(); err != nil {
						return fmt.Errorf("writing ws output: %w", err)
					}
				}
			case jobs.ProjectOutputEventComplete:
				status = event.Status
			}
		case <-flushTicker.C:
			if err := flushOutput(); err != nil {
				return fmt.Errorf("writing ws output: %w", err)
			}
		}
	}
}
