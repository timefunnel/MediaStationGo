package service

import (
	"fmt"
	"strings"
	"testing"
)

func TestProbeWorkerWholeLibraryBatchesCoverEveryGapOnce(t *testing.T) {
	f := newTransferFixture(t)
	for index := 0; index < 13; index++ {
		f.manifest.Items = append(f.manifest.Items, ProbeExchangeItem{MediaID: fmt.Sprintf("other-%d", index), LibraryID: "library-b", RootID: "root-b", RelativePath: fmt.Sprintf("%d.mkv", index), SizeBytes: 1000, Fingerprint: strings.Repeat("c", 64), Metadata: &ProbeResult{}})
	}
	f.manifest.Items = append(f.manifest.Items, ProbeExchangeItem{MediaID: "already-complete", LibraryID: "library-a", RootID: "root-a", RelativePath: "complete.mkv", SizeBytes: 1000, Fingerprint: strings.Repeat("d", 64), Metadata: &ProbeResult{DurationSec: 120, Width: 1920, Height: 1080, VideoCodec: "hevc", AudioCodec: "aac", BitRate: 1000}})
	f.manifest.Digest = ""
	var err error
	f.manifest.Digest, err = probeExchangeDigest(f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	template := ProbeWorkerBatchTemplate(f.manifest)
	if len(template.Roots) != 2 {
		t.Fatal("sample template omitted a whole-library root")
	}
	template.AccountID, template.OriginalCopy, template.Provenance = "42", true, "synthetic source mapping"
	for i := range template.Roots {
		template.Roots[i].DirectoryID = "100"
	}
	sources := ProbeWorkerSourcesTemplate(f.manifest)
	for id, source := range sources.Shares {
		source.ShareURL = "https://115.com/s/syntheticshare?password=x1y2"
		sources.Shares[id] = source
	}
	set, err := SplitProbeWorkerBatches(f.manifest, template, &sources, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Batches) != 4 {
		t.Fatalf("batch count=%d", len(set.Batches))
	}
	seen := map[string]bool{}
	for i, batch := range set.Batches {
		if len(batch.MediaIDs) > 4 || len(batch.MediaIDs) == 0 || batch.ID == template.ID {
			t.Fatal("invalid generated scope")
		}
		if _, err := set.Select(f.manifest, i); err != nil {
			t.Fatal(err)
		}
		if _, err := CreateProbeWorkerPlanWithSources(f.manifest, batch, &sources); err != nil {
			t.Fatal(err)
		}
		for _, id := range batch.MediaIDs {
			if seen[id] || id == "already-complete" {
				t.Fatal("duplicated or unnecessary media")
			}
			seen[id] = true
		}
	}
	if len(seen) != 15 || len(f.calls) != 0 {
		t.Fatal("whole-library generation lost scope or accessed network")
	}
	if _, err := set.Select(f.manifest, 4); err == nil {
		t.Fatal("out-of-range round accepted")
	}
	if err := set.CheckSources(&sources); err != nil {
		t.Fatal(err)
	}
	source := sources.Shares["root-b"]
	source.ShareURL = "https://115.com/s/changedshare"
	sources.Shares["root-b"] = source
	if err := set.CheckSources(&sources); err == nil {
		t.Fatal("source modification unnoticed")
	}
	if _, err := SplitProbeWorkerBatches(f.manifest, template, &sources, 101); err == nil {
		t.Fatal("unbounded batch accepted")
	}
	template.Roots = template.Roots[:1]
	if _, err := SplitProbeWorkerBatches(f.manifest, template, &sources, 4); err == nil {
		t.Fatal("unbound root silently skipped")
	}
}
