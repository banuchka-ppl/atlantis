// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
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

type diagnosticEvidenceReaderStub struct {
	evidence []byte
	calls    int
}

func (r *diagnosticEvidenceReaderStub) Read(context.Context, string) ([]byte, error) {
	r.calls++
	return r.evidence, nil
}

type diagnosticEvidenceS3ObjectGetterStub struct {
	objects map[string][]byte
	keys    []string
}

func (g *diagnosticEvidenceS3ObjectGetterStub) GetObject(_ context.Context, key string, maxBytes int64) ([]byte, error) {
	g.keys = append(g.keys, key)
	content, ok := g.objects[key]
	if !ok || int64(len(content)) > maxBytes {
		return nil, errDiagnosticEvidenceUnavailable
	}
	return append([]byte(nil), content...), nil
}

func TestJobsController_GetProjectDiagnosticEvidenceReturnsExactAuthenticatedRun(t *testing.T) {
	root := t.TempDir()
	writeDiagnosticEvidence(t, root, diagnosticJobID, diagnosticLogPath, "first exact raw diagnostic\n")
	writeDiagnosticEvidence(t, root, otherDiagnosticJobID, otherDiagnosticLogPath, "later raw diagnostic\n")
	fallback := &diagnosticEvidenceReaderStub{evidence: []byte("fallback must not replace local evidence\n")}
	controller := &JobsController{
		DiagnosticEvidenceAuthenticator: diagnosticEvidenceAuthenticatorStub{},
		DiagnosticEvidenceFallback:      fallback,
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
	Equals(t, 0, fallback.calls)
}

func TestJobsController_GetProjectDiagnosticEvidenceFallsBackToExactS3Run(t *testing.T) {
	evidence := []byte("S3 exact raw diagnostic\n")
	digest := sha256.Sum256(evidence)
	manifest := []byte(fmt.Sprintf(
		`{"job_id":"%s","schema_version":1,"sha256":"%x","size_bytes":%d}`,
		diagnosticJobID,
		digest,
		len(evidence),
	))
	getter := &diagnosticEvidenceS3ObjectGetterStub{objects: map[string][]byte{
		diagnosticEvidenceS3Key(diagnosticJobID, "manifest.json"):  manifest,
		diagnosticEvidenceS3Key(diagnosticJobID, "diagnostic.log"): evidence,
	}}
	controller := &JobsController{
		DiagnosticEvidenceAuthenticator: diagnosticEvidenceAuthenticatorStub{},
		DiagnosticEvidenceFallback: &diagnosticEvidenceS3Reader{
			objects: getter,
		},
		DiagnosticEvidenceRoot: t.TempDir(),
		KeyGenerator:           JobIDKeyGenerator{},
		Logger:                 logging.NewNoopLogger(t),
	}

	recorder := requestDiagnosticEvidence(controller, diagnosticJobID)

	Equals(t, http.StatusOK, recorder.Code)
	Equals(t, string(evidence), recorder.Body.String())
	Equals(t, []string{
		diagnosticEvidenceS3Key(diagnosticJobID, "manifest.json"),
		diagnosticEvidenceS3Key(diagnosticJobID, "diagnostic.log"),
	}, getter.keys)
}

func TestJobsController_GetProjectDiagnosticEvidenceRejectsCorruptS3Run(t *testing.T) {
	evidence := []byte("raw diagnostic must not leak\n")
	manifest := []byte(fmt.Sprintf(
		`{"job_id":"%s","schema_version":1,"sha256":"%064d","size_bytes":%d}`,
		diagnosticJobID,
		0,
		len(evidence),
	))
	getter := &diagnosticEvidenceS3ObjectGetterStub{objects: map[string][]byte{
		diagnosticEvidenceS3Key(diagnosticJobID, "manifest.json"):  manifest,
		diagnosticEvidenceS3Key(diagnosticJobID, "diagnostic.log"): evidence,
	}}
	controller := &JobsController{
		DiagnosticEvidenceAuthenticator: diagnosticEvidenceAuthenticatorStub{},
		DiagnosticEvidenceFallback: &diagnosticEvidenceS3Reader{
			objects: getter,
		},
		DiagnosticEvidenceRoot: t.TempDir(),
		KeyGenerator:           JobIDKeyGenerator{},
		Logger:                 logging.NewNoopLogger(t),
	}

	recorder := requestDiagnosticEvidence(controller, diagnosticJobID)

	Equals(t, http.StatusNotFound, recorder.Code)
	Assert(t, recorder.Body.String() != string(evidence), "corrupt S3 evidence leaked")
}

func TestNewS3DiagnosticEvidenceReaderValidatesCLIArguments(t *testing.T) {
	tests := []struct {
		name    string
		bucket  string
		region  string
		wantErr bool
	}{
		{name: "valid", bucket: "agi-sandbox-atlantis-plans-sbox-use1", region: "us-east-1"},
		{name: "option-like bucket", bucket: "--endpoint-url", region: "us-east-1", wantErr: true},
		{name: "ambiguous bucket", bucket: "logs..example", region: "us-east-1", wantErr: true},
		{name: "option-like region", bucket: "logs-example", region: "--profile", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewS3DiagnosticEvidenceReader(test.bucket, test.region)

			Equals(t, test.wantErr, err != nil)
		})
	}
}

func TestBoundedCommandOutputDiscardsOverflowWithoutGrowing(t *testing.T) {
	output := &boundedCommandOutput{limit: 5}

	firstWritten, firstErr := output.Write([]byte("abc"))
	secondWritten, secondErr := output.Write([]byte("defgh"))

	Equals(t, 3, firstWritten)
	Equals(t, 5, secondWritten)
	Ok(t, firstErr)
	Ok(t, secondErr)
	Equals(t, "abcde", output.content.String())
	Assert(t, output.overflow, "expected output exceeding the cap to be rejected")
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
