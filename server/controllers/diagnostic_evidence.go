// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/google/uuid"
)

const (
	diagnosticEvidenceSchemaVersion = 1
	diagnosticEvidenceIndexMaxBytes = 4 * 1024
	diagnosticEvidenceMetaMaxBytes  = 64 * 1024
	diagnosticEvidenceLogMaxBytes   = 8 * 1024 * 1024
)

var errDiagnosticEvidenceUnavailable = errors.New("diagnostic evidence unavailable")

// DiagnosticEvidenceAuthenticator authenticates a human request for raw evidence.
type DiagnosticEvidenceAuthenticator interface {
	Authenticate(request *http.Request) error
}

type diagnosticEvidenceIndex struct {
	SchemaVersion int    `json:"schema_version"`
	JobID         string `json:"job_id"`
	LogPath       string `json:"log_path"`
}

type diagnosticEvidenceMetadata struct {
	JobID string `json:"job_id"`
}

// GetProjectDiagnosticEvidence serves one exact retained run after origin authentication.
func (j *JobsController) GetProjectDiagnosticEvidence(w http.ResponseWriter, request *http.Request) {
	if j.DiagnosticEvidenceAuthenticator == nil || j.DiagnosticEvidenceRoot == "" {
		http.NotFound(w, request)
		return
	}
	if err := j.DiagnosticEvidenceAuthenticator.Authenticate(request); err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	jobID, err := j.KeyGenerator.Generate(request)
	if err != nil || !isCanonicalDiagnosticJobID(jobID) {
		http.NotFound(w, request)
		return
	}
	evidence, err := readDiagnosticEvidence(j.DiagnosticEvidenceRoot, jobID)
	if err != nil {
		http.NotFound(w, request)
		return
	}

	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=atlantis-%s-diagnostic.log", jobID))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(evidence)
}

func readDiagnosticEvidence(rootPath, jobID string) ([]byte, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, errDiagnosticEvidenceUnavailable
	}
	defer root.Close()

	indexBytes, err := readBoundedRegularFile(root, path.Join(".evidence", jobID+".json"), diagnosticEvidenceIndexMaxBytes)
	if err != nil {
		return nil, errDiagnosticEvidenceUnavailable
	}
	var index diagnosticEvidenceIndex
	if err := decodeStrictJSON(indexBytes, &index); err != nil ||
		index.SchemaVersion != diagnosticEvidenceSchemaVersion ||
		index.JobID != jobID ||
		!safeDiagnosticLogPath(index.LogPath) {
		return nil, errDiagnosticEvidenceUnavailable
	}

	metadataBytes, err := readBoundedRegularFile(root, diagnosticMetadataPath(index.LogPath), diagnosticEvidenceMetaMaxBytes)
	if err != nil {
		return nil, errDiagnosticEvidenceUnavailable
	}
	var metadata diagnosticEvidenceMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil || metadata.JobID != jobID {
		return nil, errDiagnosticEvidenceUnavailable
	}

	evidence, err := readBoundedRegularFile(root, index.LogPath, diagnosticEvidenceLogMaxBytes)
	if err != nil {
		return nil, errDiagnosticEvidenceUnavailable
	}
	return evidence, nil
}

func readBoundedRegularFile(root *os.Root, name string, maxBytes int64) ([]byte, error) {
	if err := rejectSymlinkComponents(root, name); err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, errDiagnosticEvidenceUnavailable
	}
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(content)) > maxBytes {
		return nil, errDiagnosticEvidenceUnavailable
	}
	return content, nil
}

func rejectSymlinkComponents(root *os.Root, name string) error {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.Contains(name, `\`) {
		return errDiagnosticEvidenceUnavailable
	}
	parts := strings.Split(name, "/")
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errDiagnosticEvidenceUnavailable
		}
		info, err := root.Lstat(path.Join(parts[:index+1]...))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errDiagnosticEvidenceUnavailable
		}
		if index < len(parts)-1 && !info.IsDir() {
			return errDiagnosticEvidenceUnavailable
		}
	}
	return nil
}

func decodeStrictJSON(content []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errDiagnosticEvidenceUnavailable
	}
	return nil
}

func safeDiagnosticLogPath(logPath string) bool {
	return logPath != "" &&
		path.Clean(logPath) == logPath &&
		!path.IsAbs(logPath) &&
		!strings.Contains(logPath, `\`) &&
		!strings.HasPrefix(logPath, ".evidence/") &&
		path.Ext(logPath) == ".log"
}

func diagnosticMetadataPath(logPath string) string {
	return strings.TrimSuffix(logPath, path.Ext(logPath)) + ".meta.json"
}

func isCanonicalDiagnosticJobID(jobID string) bool {
	parsed, err := uuid.Parse(jobID)
	return err == nil && parsed.String() == jobID
}
