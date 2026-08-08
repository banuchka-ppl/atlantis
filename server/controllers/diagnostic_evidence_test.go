// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

const (
	diagnosticJobID        = "8f754d80-6f1e-44c8-a370-2f4752605d95"
	otherDiagnosticJobID   = "d5611749-2c73-436a-bca7-2dc9d10bc6df"
	diagnosticLogPath      = "ppl-ai/agi/pr-187442/project/example.plan.1.log"
	otherDiagnosticLogPath = "ppl-ai/agi/pr-187442/project/example.plan.2.log"
)

type diagnosticEvidenceAuthenticatorStub struct {
	err error
}

func (a diagnosticEvidenceAuthenticatorStub) Authenticate(*http.Request) error {
	return a.err
}

func TestJobsController_GetProjectDiagnosticEvidenceReturnsExactAuthenticatedRun(t *testing.T) {
	root := t.TempDir()
	writeDiagnosticEvidence(t, root, diagnosticJobID, diagnosticLogPath, "first exact raw diagnostic\n")
	writeDiagnosticEvidence(t, root, otherDiagnosticJobID, otherDiagnosticLogPath, "later raw diagnostic\n")
	controller := &JobsController{
		DiagnosticEvidenceAuthenticator: diagnosticEvidenceAuthenticatorStub{},
		DiagnosticEvidenceRoot:          root,
		KeyGenerator:                    JobIDKeyGenerator{},
		Logger:                          logging.NewNoopLogger(t),
	}

	recorder := requestDiagnosticEvidence(controller, diagnosticJobID)

	Equals(t, http.StatusOK, recorder.Code)
	Equals(t, "first exact raw diagnostic\n", recorder.Body.String())
	Equals(t, "text/plain; charset=utf-8", recorder.Header().Get("Content-Type"))
	Equals(t, "private, no-store", recorder.Header().Get("Cache-Control"))
	Equals(t, "default-src 'none'; sandbox", recorder.Header().Get("Content-Security-Policy"))
	Equals(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	Equals(t, "no-referrer", recorder.Header().Get("Referrer-Policy"))
}

func TestJobsController_GetProjectDiagnosticEvidenceFailsClosed(t *testing.T) {
	tests := []struct {
		name          string
		authenticator DiagnosticEvidenceAuthenticator
		jobID         string
		mutate        func(t *testing.T, root string)
		expectedCode  int
	}{
		{
			name:         "feature disabled",
			jobID:        diagnosticJobID,
			expectedCode: http.StatusNotFound,
		},
		{
			name:          "authentication rejected",
			authenticator: diagnosticEvidenceAuthenticatorStub{err: errors.New("invalid access assertion")},
			jobID:         diagnosticJobID,
			expectedCode:  http.StatusUnauthorized,
		},
		{
			name:          "noncanonical job id",
			authenticator: diagnosticEvidenceAuthenticatorStub{},
			jobID:         "8F754D80-6F1E-44C8-A370-2F4752605D95",
			expectedCode:  http.StatusNotFound,
		},
		{
			name:          "metadata names another job",
			authenticator: diagnosticEvidenceAuthenticatorStub{},
			jobID:         diagnosticJobID,
			mutate: func(t *testing.T, root string) {
				writePrivateFile(t, filepath.Join(root, diagnosticMetadataPath(diagnosticLogPath)), []byte(`{"job_id":"`+otherDiagnosticJobID+`"}`))
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:          "symlinked log",
			authenticator: diagnosticEvidenceAuthenticatorStub{},
			jobID:         diagnosticJobID,
			mutate: func(t *testing.T, root string) {
				logPath := filepath.Join(root, filepath.FromSlash(diagnosticLogPath))
				Ok(t, os.Remove(logPath))
				Ok(t, os.Symlink(filepath.Base(filepath.FromSlash(otherDiagnosticLogPath)), logPath))
			},
			expectedCode: http.StatusNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testRoot := t.TempDir()
			writeDiagnosticEvidence(t, testRoot, diagnosticJobID, diagnosticLogPath, "raw diagnostic\n")
			writeDiagnosticEvidence(t, testRoot, otherDiagnosticJobID, otherDiagnosticLogPath, "other diagnostic\n")
			if test.mutate != nil {
				test.mutate(t, testRoot)
			}
			controller := &JobsController{
				DiagnosticEvidenceAuthenticator: test.authenticator,
				DiagnosticEvidenceRoot:          testRoot,
				KeyGenerator:                    JobIDKeyGenerator{},
				Logger:                          logging.NewNoopLogger(t),
			}

			recorder := requestDiagnosticEvidence(controller, test.jobID)

			Equals(t, test.expectedCode, recorder.Code)
			Assert(t, recorder.Body.String() != "raw diagnostic\n", "raw diagnostic leaked on rejected request")
		})
	}
}

func requestDiagnosticEvidence(controller *JobsController, jobID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/jobs/"+jobID+"/diagnostic", nil)
	request = mux.SetURLVars(request, map[string]string{"job-id": jobID})
	recorder := httptest.NewRecorder()
	controller.GetProjectDiagnosticEvidence(recorder, request)
	return recorder
}

func writeDiagnosticEvidence(t *testing.T, root, jobID, logPath, content string) {
	t.Helper()
	writePrivateFile(t, filepath.Join(root, filepath.FromSlash(logPath)), []byte(content))
	writePrivateFile(t, filepath.Join(root, diagnosticMetadataPath(logPath)), []byte(`{"job_id":"`+jobID+`"}`))
	writePrivateFile(t, filepath.Join(root, ".evidence", jobID+".json"), []byte(`{"schema_version":1,"job_id":"`+jobID+`","log_path":"`+logPath+`"}`))
}

func writePrivateFile(t *testing.T, path string, content []byte) {
	t.Helper()
	Ok(t, os.MkdirAll(filepath.Dir(path), 0o700))
	Ok(t, os.WriteFile(path, content, 0o600))
}
