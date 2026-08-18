// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	diagnosticEvidenceSchemaVersion = 1
	diagnosticEvidenceIndexMaxBytes = 4 * 1024
	diagnosticEvidenceMetaMaxBytes  = 64 * 1024
	diagnosticEvidenceLogMaxBytes   = 8 * 1024 * 1024
	diagnosticEvidenceS3Prefix      = "diagnostics/v1/jobs"
	diagnosticEvidenceS3Timeout     = 10 * time.Second
)

var errDiagnosticEvidenceUnavailable = errors.New("diagnostic evidence unavailable")

var (
	diagnosticEvidenceS3BucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	diagnosticEvidenceS3RegionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,30}[a-z0-9]$`)
	diagnosticEvidenceSHA256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

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

type diagnosticEvidenceS3Manifest struct {
	JobID         string `json:"job_id"`
	SchemaVersion int    `json:"schema_version"`
	SHA256        string `json:"sha256"`
	SizeBytes     int64  `json:"size_bytes"`
}

// DiagnosticEvidenceReader reads one exact retained run from a fallback store.
type DiagnosticEvidenceReader interface {
	Read(ctx context.Context, jobID string) ([]byte, error)
}

type diagnosticEvidenceS3ObjectGetter interface {
	GetObject(ctx context.Context, key string, maxBytes int64) ([]byte, error)
}

type diagnosticEvidenceS3Reader struct {
	objects diagnosticEvidenceS3ObjectGetter
}

type awsCLIDiagnosticEvidenceS3ObjectGetter struct {
	bucket string
	region string
}

type boundedCommandOutput struct {
	content  bytes.Buffer
	limit    int64
	overflow bool
}

// NewS3DiagnosticEvidenceReader uses the pod's AWS credentials to read exact
// diagnostic objects. Bucket and region are validated before becoming CLI args.
func NewS3DiagnosticEvidenceReader(bucket, region string) (DiagnosticEvidenceReader, error) {
	if !diagnosticEvidenceS3BucketPattern.MatchString(bucket) ||
		strings.Contains(bucket, "..") ||
		strings.Contains(bucket, ".-") ||
		strings.Contains(bucket, "-.") {
		return nil, fmt.Errorf("invalid diagnostic evidence S3 bucket")
	}
	if !diagnosticEvidenceS3RegionPattern.MatchString(region) {
		return nil, fmt.Errorf("invalid diagnostic evidence S3 region")
	}
	return &diagnosticEvidenceS3Reader{
		objects: &awsCLIDiagnosticEvidenceS3ObjectGetter{
			bucket: bucket,
			region: region,
		},
	}, nil
}

func (r *diagnosticEvidenceS3Reader) Read(ctx context.Context, jobID string) ([]byte, error) {
	if !isCanonicalDiagnosticJobID(jobID) {
		return nil, errDiagnosticEvidenceUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, diagnosticEvidenceS3Timeout)
	defer cancel()

	manifestBytes, err := r.objects.GetObject(
		ctx,
		diagnosticEvidenceS3Key(jobID, "manifest.json"),
		diagnosticEvidenceIndexMaxBytes,
	)
	if err != nil {
		return nil, errDiagnosticEvidenceUnavailable
	}
	var manifest diagnosticEvidenceS3Manifest
	if err := decodeStrictJSON(manifestBytes, &manifest); err != nil ||
		manifest.SchemaVersion != diagnosticEvidenceSchemaVersion ||
		manifest.JobID != jobID ||
		!diagnosticEvidenceSHA256Pattern.MatchString(manifest.SHA256) ||
		manifest.SizeBytes < 0 ||
		manifest.SizeBytes > diagnosticEvidenceLogMaxBytes {
		return nil, errDiagnosticEvidenceUnavailable
	}

	evidence, err := r.objects.GetObject(
		ctx,
		diagnosticEvidenceS3Key(jobID, "diagnostic.log"),
		diagnosticEvidenceLogMaxBytes,
	)
	if err != nil || int64(len(evidence)) != manifest.SizeBytes {
		return nil, errDiagnosticEvidenceUnavailable
	}
	digest := sha256.Sum256(evidence)
	if fmt.Sprintf("%x", digest) != manifest.SHA256 {
		return nil, errDiagnosticEvidenceUnavailable
	}
	return evidence, nil
}

func (g *awsCLIDiagnosticEvidenceS3ObjectGetter) GetObject(
	ctx context.Context,
	key string,
	maxBytes int64,
) ([]byte, error) {
	output := &boundedCommandOutput{limit: maxBytes}
	command := exec.CommandContext(
		ctx,
		"aws",
		"s3",
		"cp",
		fmt.Sprintf("s3://%s/%s", g.bucket, key),
		"-",
		"--region",
		g.region,
		"--only-show-errors",
		"--no-progress",
	)
	command.Stdout = output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil || output.overflow {
		return nil, errDiagnosticEvidenceUnavailable
	}
	return output.content.Bytes(), nil
}

func (b *boundedCommandOutput) Write(content []byte) (int, error) {
	written := len(content)
	remaining := b.limit - int64(b.content.Len())
	if remaining <= 0 {
		b.overflow = b.overflow || written > 0
		return written, nil
	}
	kept := content
	if int64(len(kept)) > remaining {
		kept = kept[:remaining]
		b.overflow = true
	}
	_, _ = b.content.Write(kept)
	return written, nil
}

func diagnosticEvidenceS3Key(jobID, name string) string {
	return path.Join(diagnosticEvidenceS3Prefix, jobID, name)
}

// GetProjectDiagnosticEvidence serves one exact retained run after origin authentication.
func (j *JobsController) GetProjectDiagnosticEvidence(w http.ResponseWriter, request *http.Request) {
	if j.DiagnosticEvidenceAuthenticator == nil {
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
	var evidence []byte
	evidenceErr := errDiagnosticEvidenceUnavailable
	if j.DiagnosticEvidenceRoot != "" {
		evidence, evidenceErr = readDiagnosticEvidence(j.DiagnosticEvidenceRoot, jobID)
	}
	if evidenceErr != nil && j.DiagnosticEvidenceFallback != nil {
		evidence, evidenceErr = j.DiagnosticEvidenceFallback.Read(request.Context(), jobID)
	}
	if evidenceErr != nil {
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
