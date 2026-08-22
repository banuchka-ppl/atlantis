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

type diagnosticEvidenceS3Fixture struct {
	objects     map[string][]byte
	commitKey   string
	manifestKey string
	logKey      string
}

func (g *diagnosticEvidenceS3ObjectGetterStub) GetObject(_ context.Context, key string, maxBytes int64) ([]byte, error) {
	g.keys = append(g.keys, key)
	content, ok := g.objects[key]
	if !ok || int64(len(content)) > maxBytes {
		return nil, errDiagnosticEvidenceUnavailable
	}
	return append([]byte(nil), content...), nil
}

func newDiagnosticEvidenceS3Fixture(evidence []byte) diagnosticEvidenceS3Fixture {
	generationDigest := sha256.Sum256(evidence)
	generation := fmt.Sprintf("%x", generationDigest)
	manifest := []byte(fmt.Sprintf(
		`{"generation":"%s","job_id":"%s","schema_version":2,"sha256":"%s","size_bytes":%d}`+"\n",
		generation,
		diagnosticJobID,
		generation,
		len(evidence),
	))
	manifestDigest := sha256.Sum256(manifest)
	commit := []byte(fmt.Sprintf(
		`{"generation":"%s","job_id":"%s","manifest_sha256":"%x","schema_version":2}`+"\n",
		generation,
		diagnosticJobID,
		manifestDigest,
	))
	commitKey := diagnosticEvidenceS3CommitKey(diagnosticJobID)
	manifestKey := diagnosticEvidenceS3GenerationKey(diagnosticJobID, generation, "manifest.json")
	logKey := diagnosticEvidenceS3GenerationKey(diagnosticJobID, generation, "diagnostic.log")
	return diagnosticEvidenceS3Fixture{
		objects: map[string][]byte{
			commitKey:   commit,
			manifestKey: manifest,
			logKey:      evidence,
		},
		commitKey:   commitKey,
		manifestKey: manifestKey,
		logKey:      logKey,
	}
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
	fixture := newDiagnosticEvidenceS3Fixture(evidence)
	getter := &diagnosticEvidenceS3ObjectGetterStub{objects: fixture.objects}
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
		fixture.commitKey,
		fixture.manifestKey,
		fixture.logKey,
	}, getter.keys)
}

func TestJobsController_GetProjectDiagnosticEvidenceRejectsIncompleteOrCrossPairedS3Run(t *testing.T) {
	evidence := []byte("raw diagnostic must not leak\n")
	tests := []struct {
		name   string
		mutate func(fixture diagnosticEvidenceS3Fixture)
	}{
		{
			name: "partial log",
			mutate: func(fixture diagnosticEvidenceS3Fixture) {
				fixture.objects[fixture.logKey] = evidence[:len(evidence)-1]
			},
		},
		{
			name: "partial manifest",
			mutate: func(fixture diagnosticEvidenceS3Fixture) {
				manifest := fixture.objects[fixture.manifestKey]
				fixture.objects[fixture.manifestKey] = manifest[:len(manifest)-1]
			},
		},
		{
			name: "cross-paired manifest",
			mutate: func(fixture diagnosticEvidenceS3Fixture) {
				other := newDiagnosticEvidenceS3Fixture([]byte("other generation\n"))
				fixture.objects[fixture.manifestKey] = other.objects[other.manifestKey]
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDiagnosticEvidenceS3Fixture(evidence)
			test.mutate(fixture)
			controller := &JobsController{
				DiagnosticEvidenceAuthenticator: diagnosticEvidenceAuthenticatorStub{},
				DiagnosticEvidenceFallback: &diagnosticEvidenceS3Reader{
					objects: &diagnosticEvidenceS3ObjectGetterStub{objects: fixture.objects},
				},
				DiagnosticEvidenceRoot: t.TempDir(),
				KeyGenerator:           JobIDKeyGenerator{},
				Logger:                 logging.NewNoopLogger(t),
			}

			recorder := requestDiagnosticEvidence(controller, diagnosticJobID)

			Equals(t, http.StatusNotFound, recorder.Code)
			Assert(t, recorder.Body.String() != string(evidence), "invalid S3 evidence leaked")
		})
	}
}

func TestDiagnosticEvidenceS3ReaderCommitIsTheOnlyVisibilityPoint(t *testing.T) {
	evidence := []byte("committed diagnostic\n")
	fixture := newDiagnosticEvidenceS3Fixture(evidence)
	stagedObjects := make(map[string][]byte)
	reader := &diagnosticEvidenceS3Reader{
		objects: &diagnosticEvidenceS3ObjectGetterStub{objects: stagedObjects},
	}

	_, err := reader.Read(context.Background(), diagnosticJobID)
	Assert(t, err != nil, "empty generation was readable")
	stagedObjects[fixture.logKey] = fixture.objects[fixture.logKey]
	_, err = reader.Read(context.Background(), diagnosticJobID)
	Assert(t, err != nil, "log-only generation was readable")
	stagedObjects[fixture.manifestKey] = fixture.objects[fixture.manifestKey]
	_, err = reader.Read(context.Background(), diagnosticJobID)
	Assert(t, err != nil, "uncommitted generation was readable")
	stagedObjects[fixture.commitKey] = fixture.objects[fixture.commitKey]

	actual, err := reader.Read(context.Background(), diagnosticJobID)

	Ok(t, err)
	Equals(t, string(evidence), string(actual))
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
