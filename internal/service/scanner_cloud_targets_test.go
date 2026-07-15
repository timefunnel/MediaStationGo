package service

import "testing"

func TestCloudScanTargetsAllowExactVideoAtLibraryRoot(t *testing.T) {
	mount := CloudMountInfo{Provider: "openlist", DisplayDir: "115/电影", ScanDir: "115/电影"}
	targets := cloudScanTargetsForOpenListPaths(mount, []string{"/115/电影/Movie.mkv"})

	if len(targets) != 1 {
		t.Fatalf("targets = %#v, want one exact file target", targets)
	}
	if targets[0].scanDir != "115/电影" || targets[0].displayDir != "115/电影" || targets[0].exactFileName != "Movie.mkv" {
		t.Fatalf("target = %#v", targets[0])
	}
}

func TestCloudScanTargetsStillRejectWholeLibraryRoot(t *testing.T) {
	mount := CloudMountInfo{Provider: "openlist", DisplayDir: "115/电影", ScanDir: "115/电影"}
	targets := cloudScanTargetsForOpenListPaths(mount, []string{"/115/电影"})

	if len(targets) != 0 {
		t.Fatalf("targets = %#v, want root scan rejected", targets)
	}
}

func TestCloudScanTargetsKeepExactFileInsideChildDirectory(t *testing.T) {
	mount := CloudMountInfo{Provider: "openlist", DisplayDir: "115/电影", ScanDir: "115/电影"}
	targets := cloudScanTargetsForOpenListPaths(mount, []string{"/115/电影/Movie/Movie.mp4"})

	if len(targets) != 1 {
		t.Fatalf("targets = %#v, want one child file target", targets)
	}
	if targets[0].scanDir != "115/电影/Movie" || targets[0].displayDir != "115/电影/Movie" || targets[0].exactFileName != "Movie.mp4" {
		t.Fatalf("target = %#v", targets[0])
	}
}
