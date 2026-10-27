package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

func newExchangeFixture(t *testing.T, ids ...string) (*MediaProbeQueueService, *ProbeExchangeManifest) {
	t.Helper()
	queue, _ := newExplicitProbeQueue(t)
	if err := queue.repo.DB.AutoMigrate(&model.LibraryRoot{}, &model.MediaProbeExchange{}); err != nil {
		t.Fatal(err)
	}
	if err := queue.repo.DB.Create(&model.Library{Base: model.Base{ID: "library-a"}, Name: "Synthetic", Path: "cloud://openlist/Synthetic", Type: "movie"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := queue.repo.DB.Create(&model.Media{Base: model.Base{ID: id}, LibraryID: "library-a", Title: "Synthetic " + id, Path: "cloud://openlist/Synthetic/" + id + ".mkv", SizeBytes: 12000000}).Error; err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := queue.ExportExchangeManifest(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return queue, manifest
}

func exchangeTestBundle(m *ProbeExchangeManifest) *ProbeExchangeBundle {
	b := &ProbeExchangeBundle{Version: ProbeExchangeVersion, AccountID: "42", ManifestID: m.ID, ManifestDigest: m.Digest, BatchID: "synthetic-batch", Provenance: "synthetic unchanged transfer", PlanDigest: strings.Repeat("b", 64)}
	for _, item := range m.Items {
		b.Results = append(b.Results, ProbeExchangeResult{MediaID: item.MediaID, Fingerprint: item.Fingerprint, FileID: "201", SHA1: strings.Repeat("A", 40), SizeBytes: item.SizeBytes, Status: "completed", Source: "ffprobe", Metadata: &ProbeResult{DurationSec: 120, Width: 1920, Height: 1080, VideoCodec: "hevc", AudioCodec: "aac"}})
	}
	return b
}

func TestProbeExchangeAutomaticTransferProofIsValidatedAndAudited(t *testing.T) {
	queue, manifest := newExchangeFixture(t, "a")
	b := exchangeTestBundle(manifest)
	b.SourcesDigest = strings.Repeat("c", 64)
	b.Results[0].Transfer = &ProbeExchangeTransfer{SourceID: "source-a", SourceFileID: "401", SHA1: b.Results[0].SHA1, SizeBytes: b.Results[0].SizeBytes, DirectoryID: "301"}
	p, err := queue.PreviewExchange(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	b.Results[0].Transfer.SHA1 = strings.Repeat("D", 40)
	if _, err := queue.PreviewExchange(t.Context(), b); err == nil {
		t.Fatal("mismatched transfer identity accepted")
	}
	b.Results[0].Transfer.SHA1 = b.Results[0].SHA1
	if _, err := queue.ApplyExchange(t.Context(), b, p.Digest, true); err != nil {
		t.Fatal(err)
	}
	var audit model.MediaProbeExchange
	if err := queue.repo.DB.First(&audit, "kind = ?", "import").Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(audit.Payload, `"source_file_id":"401"`) || !strings.Contains(audit.Payload, `"sources_digest"`) {
		t.Fatal("transfer provenance not audited")
	}
}

func TestProbeExchangeExportKeepsCanonicalSnapshotAndNeverProbes(t *testing.T) {
	queue, manifest := newExchangeFixture(t, "a", "b")
	if len(manifest.Items) != 2 || manifest.Items[0].RelativePath != "a.mkv" || manifest.Items[0].RootID != "library-a" {
		t.Fatalf("unexpected export: %+v", manifest)
	}
	if err := manifest.CheckDigest(); err != nil {
		t.Fatal(err)
	}
	var record model.MediaProbeExchange
	if err := queue.repo.DB.First(&record, "id = ?", manifest.ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.Kind != "manifest" || record.Digest != manifest.Digest {
		t.Fatalf("snapshot not persisted: %+v", record)
	}
	manifest.Items[0].SizeBytes++
	if err := manifest.CheckDigest(); err == nil {
		t.Fatal("tampered export accepted")
	}
	var row model.Media
	if err := queue.repo.DB.First(&row, "id = ?", "a").Error; err != nil {
		t.Fatal(err)
	}
	if row.MediaProbeVersion != 0 || row.DurationSec != 0 {
		t.Fatal("export changed technical fields")
	}
}

func TestProbeExchangeImportPreviewsWritesAndIsIdempotent(t *testing.T) {
	queue, manifest := newExchangeFixture(t, "a")
	bundle := exchangeTestBundle(manifest)
	if err := queue.repo.DB.Create(&model.MediaProbeJob{MediaID: "a", Fingerprint: manifest.Items[0].Fingerprint, Status: "pending", Attempts: 2}).Error; err != nil {
		t.Fatal(err)
	}
	preview, err := queue.PreviewExchange(t.Context(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Counts["completed"] != 1 || preview.Changes[0].Before["width"] != 0 || preview.Changes[0].After["bit_rate"] != int64(800000) {
		t.Fatalf("bad preview: %+v", preview)
	}
	var before model.Media
	queue.repo.DB.First(&before, "id = ?", "a")
	if before.Width != 0 {
		t.Fatal("preview wrote media")
	}
	if _, err := queue.ApplyExchange(t.Context(), bundle, preview.Digest, false); err == nil {
		t.Fatal("unconfirmed import accepted")
	}
	if _, err := queue.ApplyExchange(t.Context(), bundle, preview.Digest, true); err != nil {
		t.Fatal(err)
	}
	var current model.Media
	if err := queue.repo.DB.First(&current, "id = ?", "a").Error; err != nil {
		t.Fatal(err)
	}
	if current.Width != 1920 || current.AudioCodec != "aac" || current.MediaProbeVersion != 1 || current.BitRate != 800000 || current.Path != before.Path || current.LibraryID != before.LibraryID || current.Title != before.Title {
		t.Fatalf("wrong import: %+v", current)
	}
	var job model.MediaProbeJob
	if err := queue.repo.DB.First(&job, "media_id = ?", "a").Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != "completed" || job.Attempts != 2 {
		t.Fatalf("queue state/attempts changed incorrectly: %+v", job)
	}
	again, err := queue.ApplyExchange(t.Context(), bundle, preview.Digest, true)
	if err != nil || !again.AlreadyApplied {
		t.Fatalf("repeat import not idempotent: %+v %v", again, err)
	}
	var audit model.MediaProbeExchange
	if err := queue.repo.DB.First(&audit, "kind = ?", "import").Error; err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Preview ProbeExchangePreview
		Bundle  ProbeExchangeBundle
	}
	if err := json.Unmarshal([]byte(audit.Payload), &stored); err != nil || stored.Preview.Changes[0].Before["width"] != float64(0) || stored.Bundle.Results[0].SHA1 == "" {
		t.Fatalf("incomplete audit: %v %s", err, audit.Payload)
	}
}

func TestProbeExchangeConflictsAndPartialVersionRemainExplicit(t *testing.T) {
	queue, manifest := newExchangeFixture(t, "changed", "codec", "partial")
	bundle := exchangeTestBundle(manifest)
	if err := queue.repo.DB.Model(&model.Media{}).Where("id = ?", "changed").Update("size_bytes", 7).Error; err != nil {
		t.Fatal(err)
	}
	if err := queue.repo.DB.Model(&model.Media{}).Where("id = ?", "codec").Update("video_codec", "h264").Error; err != nil {
		t.Fatal(err)
	}
	for index := range bundle.Results {
		if bundle.Results[index].MediaID == "partial" {
			bundle.Results[index].Metadata = &ProbeResult{DurationSec: 120}
			bundle.Results[index].Status = "partial"
		}
	}
	preview, err := queue.PreviewExchange(t.Context(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Counts["conflict"] != 2 || preview.Counts["partial"] != 1 {
		t.Fatalf("wrong statuses: %+v", preview.Counts)
	}
	if _, err := queue.ApplyExchange(t.Context(), bundle, preview.Digest, true); err != nil {
		t.Fatal(err)
	}
	var partial, conflict model.Media
	queue.repo.DB.First(&partial, "id = ?", "partial")
	queue.repo.DB.First(&conflict, "id = ?", "codec")
	if partial.DurationSec != 120 || partial.MediaProbeVersion != 0 || partial.Width != 0 || conflict.VideoCodec != "h264" || conflict.DurationSec != 0 {
		t.Fatalf("partial/conflict overwritten: %+v %+v", partial, conflict)
	}
}

func TestProbeExchangeRejectsForgedIdentityAndStalePreview(t *testing.T) {
	queue, manifest := newExchangeFixture(t, "a")
	for _, mutate := range []func(*ProbeExchangeBundle){
		func(b *ProbeExchangeBundle) { b.ManifestDigest = strings.Repeat("0", 64) },
		func(b *ProbeExchangeBundle) { b.Results = append(b.Results, b.Results[0]) },
		func(b *ProbeExchangeBundle) { b.Results[0].Fingerprint = strings.Repeat("f", 64) },
		func(b *ProbeExchangeBundle) { b.Results[0].SHA1 = "not-a-sha1" },
		func(b *ProbeExchangeBundle) { b.Results[0].MediaID = "not-exported" },
	} {
		bundle := exchangeTestBundle(manifest)
		mutate(bundle)
		if _, err := queue.PreviewExchange(t.Context(), bundle); err == nil {
			t.Fatal("forged identity accepted")
		}
	}
	bundle := exchangeTestBundle(manifest)
	preview, err := queue.PreviewExchange(t.Context(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.repo.DB.Model(&model.Media{}).Where("id = ?", "a").Update("duration_sec", 120).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := queue.ApplyExchange(t.Context(), bundle, preview.Digest, true); err == nil {
		t.Fatal("stale preview accepted")
	}
	var current model.Media
	queue.repo.DB.First(&current, "id = ?", "a")
	if current.Width != 0 {
		t.Fatal("stale import partially committed")
	}
}

func TestProbeExchangeSourceRootsAndCodecEquivalence(t *testing.T) {
	row := &model.Media{LibraryID: "lib", Path: "cloud://openlist/影片/目录/片名.mkv"}
	root, relative, err := exchangeRelativeSource(row, []model.Library{{Base: model.Base{ID: "lib"}, Path: "cloud://openlist/%E5%BD%B1%E7%89%87"}}, nil)
	if err != nil || root != "lib" || relative != "目录/片名.mkv" {
		t.Fatalf("unicode mapping: %s %s %v", root, relative, err)
	}
	if _, _, err := exchangeRelativeSource(row, []model.Library{{Base: model.Base{ID: "lib"}, Path: "cloud://openlist/影片外"}}, nil); err == nil {
		t.Fatal("prefix sibling treated as a root")
	}
	merged, err := mergeExchangeProbe(&ProbeResult{VideoCodec: "hevc", Container: "matroska,webm", BitRate: 1234}, &ProbeResult{VideoCodec: "h265", Container: "mkv", BitRate: 1200, Width: 1920})
	if err != nil || merged.BitRate != 1234 || merged.Container != "matroska,webm" || merged.Width != 1920 {
		t.Fatalf("equivalent values: %+v %v", merged, err)
	}
}
