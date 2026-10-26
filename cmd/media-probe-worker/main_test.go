package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShukeBta/MediaStationGo/internal/service"
)

func hashForCLI(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func TestOfflineCommandsNeedNoCredentialsAndRejectOverwrite(t *testing.T) {
	dir := t.TempDir()
	manifestPath, batchPath, planPath := filepath.Join(dir, "manifest.json"), filepath.Join(dir, "batch.json"), filepath.Join(dir, "plan.json")
	manifest := service.ProbeExchangeManifest{Version: 1, ID: "synthetic", CreatedAt: time.Now().UTC(), Items: []service.ProbeExchangeItem{{MediaID: "media-a", LibraryID: "library-a", RootID: "root-a", RelativePath: "A.mkv", SizeBytes: 1000, Fingerprint: strings.Repeat("a", 64), Metadata: &service.ProbeResult{}}}}
	// Hash the canonical public wire format; no service internals or network.
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Digest = hashForCLI(data)
	if err := service.WriteProbeWorkerJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"init", "-manifest", manifestPath, "-out", batchPath}, &output); err != nil {
		t.Fatal(err)
	}
	var batch service.ProbeWorkerBatch
	if err := service.ReadProbeWorkerJSON(batchPath, &batch); err != nil {
		t.Fatal(err)
	}
	if batch.OriginalCopy || batch.AccountID != "" || len(batch.MediaIDs) != 1 || batch.Roots[0].DirectoryID != "SET_B_DIRECTORY_ID" {
		t.Fatalf("unsafe default template: %+v", batch)
	}
	batch.AccountID = "42"
	batch.OriginalCopy = true
	batch.Provenance = "synthetic unchanged transfer"
	batch.Roots[0].DirectoryID = "100"
	if err := service.WriteProbeWorkerJSON(batchPath, batch); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"plan", "-manifest", manifestPath, "-batch", batchPath, "-out", planPath}, &output); err != nil {
		t.Fatal(err)
	}
	var plan service.ProbeWorkerPlan
	if err := service.ReadProbeWorkerJSON(planPath, &plan); err != nil {
		t.Fatal(err)
	}
	if err := plan.Check(&manifest); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "proapi.115.com") || plan.Batch.Limits.ControlRequests != 32 {
		t.Fatal("plan did not show finite real targets and budget")
	}
	if err := run(context.Background(), []string{"init", "-manifest", manifestPath, "-out", batchPath}, &output); err == nil {
		t.Fatal("existing user file overwritten")
	}
}

func TestCredentialTemplateIsPrivateEmptyAndNeverPrinted(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "secret.json")
	var output bytes.Buffer
	if err := run(context.Background(), []string{"credentials-template", "-out", filename}, &output); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("credential template printed")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"access_token": ""`)) {
		t.Fatal("template contains a token")
	}
	if err := run(context.Background(), []string{"credentials-template", "-out", filename}, &output); err == nil {
		t.Fatal("credential file overwritten")
	}
}
