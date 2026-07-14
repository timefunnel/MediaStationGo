package service

import (
	"context"
	"fmt"
	"path"
	"strings"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

type cloudScanImportRequest struct {
	provider      string
	candidates    []cloudCandidate
	existingMedia map[string]existingCloudMedia
	writeBatch    *localMediaWriteBatch
	probeBudget   *int
	defaultRootID string
	progress      *cloudScanProgressState
	result        *ScanResult
}

type cloudScanImportResult struct {
	seen              map[string]struct{}
	touchedLibraryIDs []string
	scopeLibraryIDs   []string
}

type cloudLibraryScanCompletion struct {
	libraryID         string
	provider          string
	touchedLibraryIDs []string
	result            *ScanResult
	progress          *cloudScanProgressState
	autoScrape        bool
}

type cloudScanRootTarget struct {
	scanDir         string
	displayDir      string
	targetFileNames map[string]struct{}
}

func (s *ScannerService) scanCloudLibrary(ctx context.Context, lib *model.Library, mount CloudMountInfo, autoScrape bool) (*ScanResult, error) {
	return s.scanCloudLibraryWithRoot(ctx, lib, mount, "", autoScrape)
}

func (s *ScannerService) scanCloudLibraryRoot(ctx context.Context, lib *model.Library, root *model.LibraryRoot, mount CloudMountInfo, autoScrape bool) (*ScanResult, error) {
	return s.scanCloudLibraryWithRoot(ctx, lib, mount, libraryRootID(root), autoScrape)
}

func (s *ScannerService) scanCloudLibraryRootTargets(ctx context.Context, lib *model.Library, root *model.LibraryRoot, mount CloudMountInfo, targets []cloudScanRootTarget, autoScrape bool) (*ScanResult, error) {
	res := &ScanResult{LibraryID: lib.ID}
	if s.storage == nil {
		return res, fmt.Errorf("cloud storage service unavailable")
	}
	if len(targets) == 0 {
		return res, nil
	}

	cfg, err := s.repo.StorageConfig.Get(ctx, mount.Provider)
	if err != nil || cfg == nil {
		return res, fmt.Errorf("storage config not found: %s", mount.Provider)
	}
	if !cfg.Enabled {
		return res, fmt.Errorf("storage %s is disabled", mount.Provider)
	}
	typ := mount.Provider
	autoCategoryRoot := cloudRootMountNeedsAutoCategory(mount) && s.cloudAutoCategoryEnabled(ctx)
	scopeIDs := s.cloudScanLibraryScopeIDs(ctx, lib, mount)
	progress := newCloudScanProgressState()
	progress.publish(s, lib.ID, res, "listing", true)

	candidates := make([]cloudCandidate, 0, len(targets))
	for _, target := range targets {
		if err := s.warmCloudScanTargetAncestors(ctx, typ, mount.ScanDir, target.scanDir); err != nil {
			return res, err
		}
		targetCandidates, err := s.collectCloudScanCandidates(ctx, lib, cloudScanCandidateRequest{
			provider:         typ,
			rootDir:          target.scanDir,
			rootDisplayDir:   target.displayDir,
			refreshRoot:      true,
			targetFileNames:  target.targetFileNames,
			autoCategoryRoot: autoCategoryRoot,
			progress:         progress,
			result:           res,
		})
		if err != nil {
			return res, err
		}
		candidates = append(candidates, targetCandidates...)
	}
	existingMedia, err := s.existingCloudMediaSnapshotForLibraries(ctx, scopeIDs)
	if err != nil {
		s.log.Warn("load existing cloud media snapshot failed", zap.String("library_id", lib.ID), zap.Error(err))
		existingMedia = nil
	}
	sortCloudCandidatesByRefreshPriority(candidates, existingMedia)
	writeBatch := newLocalMediaWriteBatch(s, ctx, res, 100)
	probeBudget := maxCloudMediaProbeQueuePerScan
	imported, err := s.importCloudScanCandidates(ctx, lib, cloudScanImportRequest{
		provider:      typ,
		candidates:    candidates,
		existingMedia: existingMedia,
		writeBatch:    writeBatch,
		probeBudget:   &probeBudget,
		defaultRootID: libraryRootID(root),
		progress:      progress,
		result:        res,
	})
	if err != nil {
		return res, err
	}
	scopeIDs = appendUniqueLibraryIDs(scopeIDs, imported.scopeLibraryIDs...)
	writeBatch.Flush()
	s.completeCloudLibraryScan(ctx, cloudLibraryScanCompletion{libraryID: lib.ID, provider: mount.Provider, touchedLibraryIDs: imported.touchedLibraryIDs, result: res, progress: progress, autoScrape: autoScrape})
	return res, nil
}

func (s *ScannerService) scanCloudLibraryWithRoot(ctx context.Context, lib *model.Library, mount CloudMountInfo, defaultRootID string, autoScrape bool) (*ScanResult, error) {
	res := &ScanResult{LibraryID: lib.ID}
	if s.storage == nil {
		return res, fmt.Errorf("cloud storage service unavailable")
	}

	cfg, err := s.repo.StorageConfig.Get(ctx, mount.Provider)
	if err != nil || cfg == nil {
		return res, fmt.Errorf("storage config not found: %s", mount.Provider)
	}
	if !cfg.Enabled {
		return res, fmt.Errorf("storage %s is disabled", mount.Provider)
	}
	typ := mount.Provider
	rootDir := mount.ScanDir
	rootDisplayDir := mount.DisplayDir
	autoCategoryRoot := cloudRootMountNeedsAutoCategory(mount) && s.cloudAutoCategoryEnabled(ctx)
	scopeIDs := s.cloudScanLibraryScopeIDs(ctx, lib, mount)
	progress := newCloudScanProgressState()
	progress.publish(s, lib.ID, res, "listing", true)
	candidates, err := s.collectCloudScanCandidates(ctx, lib, cloudScanCandidateRequest{
		provider:         typ,
		rootDir:          rootDir,
		rootDisplayDir:   rootDisplayDir,
		autoCategoryRoot: autoCategoryRoot,
		progress:         progress,
		result:           res,
	})
	if err != nil {
		return res, err
	}
	existingMedia, err := s.existingCloudMediaSnapshotForLibraries(ctx, scopeIDs)
	if err != nil {
		s.log.Warn("load existing cloud media snapshot failed", zap.String("library_id", lib.ID), zap.Error(err))
		existingMedia = nil
	}
	sortCloudCandidatesByRefreshPriority(candidates, existingMedia)
	writeBatch := newLocalMediaWriteBatch(s, ctx, res, 100)
	probeBudget := maxCloudMediaProbeQueuePerScan
	imported, err := s.importCloudScanCandidates(ctx, lib, cloudScanImportRequest{
		provider:      typ,
		candidates:    candidates,
		existingMedia: existingMedia,
		writeBatch:    writeBatch,
		probeBudget:   &probeBudget,
		defaultRootID: defaultRootID,
		progress:      progress,
		result:        res,
	})
	if err != nil {
		return res, err
	}
	scopeIDs = appendUniqueLibraryIDs(scopeIDs, imported.scopeLibraryIDs...)
	writeBatch.Flush()
	var removed int64
	if defaultRootID != "" {
		removed, err = s.pruneMissingCloudMediaForRoot(ctx, lib.ID, defaultRootID, imported.seen)
	} else {
		removed, err = s.pruneMissingCloudMediaForLibraries(ctx, scopeIDs, imported.seen)
	}
	if err != nil {
		s.log.Warn("prune missing cloud media failed", zap.String("library_id", lib.ID), zap.Error(err))
	} else {
		res.Removed = removed
	}
	s.completeCloudLibraryScan(ctx, cloudLibraryScanCompletion{
		libraryID:         lib.ID,
		provider:          typ,
		touchedLibraryIDs: imported.touchedLibraryIDs,
		result:            res,
		progress:          progress,
		autoScrape:        autoScrape,
	})
	return res, nil
}

type cloudScanTarget struct {
	lib    *model.Library
	rootID string
}

func (s *ScannerService) importCloudScanCandidates(ctx context.Context, rootLib *model.Library, req cloudScanImportRequest) (cloudScanImportResult, error) {
	imported := cloudScanImportResult{
		seen:              make(map[string]struct{}),
		touchedLibraryIDs: []string{},
		scopeLibraryIDs:   []string{},
	}
	targetLibs := map[string]cloudScanTarget{"": {lib: rootLib, rootID: req.defaultRootID}}
	for _, candidate := range req.candidates {
		select {
		case <-ctx.Done():
			return imported, ctx.Err()
		default:
		}
		target := targetLibs[""]
		if candidate.categoryDisplayDir != "" {
			categoryKey := candidate.categoryDisplayDir + "\x00" + candidate.categoryScanDir
			if cached, ok := targetLibs[categoryKey]; ok {
				target = cached
			} else if categoryTarget, err := s.ensureCloudAutoCategoryTarget(ctx, rootLib, req.provider, candidate.categoryDisplayDir, candidate.categoryScanDir); err == nil && categoryTarget.Library != nil {
				target = cloudScanTarget{lib: categoryTarget.Library, rootID: categoryTarget.RootID}
				targetLibs[categoryKey] = target
				imported.scopeLibraryIDs = appendUniqueLibraryIDs(imported.scopeLibraryIDs, categoryTarget.Library.ID)
			} else if err != nil {
				s.log.Warn("ensure cloud auto category library failed",
					zap.String("library_id", rootLib.ID),
					zap.String("provider", req.provider),
					zap.String("category", candidate.categoryDisplayDir),
					zap.String("scan_dir", candidate.categoryScanDir),
					zap.Error(err))
			}
		}
		targetLib := target.lib
		if targetLib == nil {
			targetLib = rootLib
		}
		imported.touchedLibraryIDs = appendUniqueLibraryIDs(imported.touchedLibraryIDs, targetLib.ID)
		imported.seen[candidate.path] = struct{}{}
		s.ingestCloudFile(ctx, targetLib, target.rootID, req.provider, candidate.ref, candidate.path, candidate.name, candidate.size, candidate.localMeta, req.existingMedia, req.writeBatch, req.probeBudget, req.result)
		req.progress.publish(s, rootLib.ID, req.result, "importing", req.result.Visited == 1 || req.result.Visited%100 == 0)
	}
	return imported, nil
}

func (s *ScannerService) completeCloudLibraryScan(ctx context.Context, req cloudLibraryScanCompletion) {
	publishCloudScanFinished(s, req.libraryID, req.result, req.progress)
	s.invalidateMediaCache(ctx)
	targetIDs := appendUniqueLibraryIDs(req.touchedLibraryIDs, req.libraryID)
	for _, targetID := range targetIDs {
		s.maybeGenerateSTRMAfterScan(targetID)
	}
	if scanHasImportChanges(req.result) && req.autoScrape && s.scraper != nil && s.scraper.AnyEnabled() && s.autoScrapeEnabled(ctx) {
		for _, targetID := range targetIDs {
			s.startAutoScrape(ctx, targetID)
		}
	}
}

func scanHasImportChanges(res *ScanResult) bool {
	return res != nil && (res.Added > 0 || res.Updated > 0 || res.Removed > 0)
}

func cloudScanTargetsForOpenListPaths(mount CloudMountInfo, values []string) []cloudScanRootTarget {
	rootDisplay := normalizeCloudMountDir(mount.Provider, mount.DisplayDir)
	rootScan := normalizeCloudMountDir(mount.Provider, mount.ScanDir)
	out := make([]cloudScanRootTarget, 0, len(values))
	indexByDir := map[string]int{}
	for _, value := range values {
		displayDir := normalizeCloudMountDir(mount.Provider, value)
		if displayDir == "" {
			continue
		}
		targetFileName := ""
		if _, ok := videoExtensions[strings.ToLower(path.Ext(displayDir))]; ok {
			targetFileName = path.Base(displayDir)
			displayDir = normalizeCloudMountDir(mount.Provider, path.Dir(displayDir))
		}
		if displayDir == "" || (displayDir == rootDisplay && targetFileName == "") || !cloudScanDirSameOrChild(rootDisplay, displayDir) {
			continue
		}
		suffix := strings.Trim(strings.TrimPrefix(displayDir, rootDisplay), "/")
		scanDir := rootScan
		if suffix != "" {
			scanDir = strings.Trim(strings.TrimRight(rootScan, "/")+"/"+suffix, "/")
		}
		key := scanDir + "\x00" + displayDir
		if index, ok := indexByDir[key]; ok {
			if targetFileName == "" {
				out[index].targetFileNames = nil
			} else if out[index].targetFileNames != nil {
				out[index].targetFileNames[targetFileName] = struct{}{}
			}
			continue
		}
		target := cloudScanRootTarget{scanDir: scanDir, displayDir: displayDir}
		if targetFileName != "" {
			target.targetFileNames = map[string]struct{}{targetFileName: {}}
		}
		indexByDir[key] = len(out)
		out = append(out, target)
	}
	return out
}

func (s *ScannerService) warmCloudScanTargetAncestors(ctx context.Context, provider, rootDir, targetDir string) error {
	for _, dir := range cloudScanTargetAncestorDirs(rootDir, targetDir) {
		if _, err := s.storage.CloudListRefresh(ctx, provider, dir); err != nil {
			return err
		}
	}
	return nil
}

func cloudScanTargetAncestorDirs(rootDir, targetDir string) []string {
	rootDir = strings.Trim(normalizeCloudMountDir("", rootDir), "/")
	targetDir = strings.Trim(normalizeCloudMountDir("", targetDir), "/")
	if targetDir == "" || targetDir == rootDir || !cloudScanDirSameOrChild(rootDir, targetDir) {
		return nil
	}
	suffix := strings.Trim(strings.TrimPrefix(targetDir, rootDir), "/")
	if suffix == "" {
		return nil
	}
	parts := strings.Split(suffix, "/")
	ancestors := []string{rootDir}
	current := rootDir
	for _, part := range parts[:len(parts)-1] {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		current = strings.Trim(path.Join(current, part), "/")
		ancestors = append(ancestors, current)
	}
	return uniqueCloudScanDirs(ancestors)
}

func cloudScanDirSameOrChild(parent, child string) bool {
	parent = strings.Trim(parent, "/")
	child = strings.Trim(child, "/")
	if parent == "" {
		return child != ""
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
}

func uniqueCloudScanDirs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		key := strings.ToLower(strings.Trim(value, "/"))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, strings.Trim(value, "/"))
	}
	return out
}
