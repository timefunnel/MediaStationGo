package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// A 1200x675 opaque PNG can exceed the ordinary thumbnail's 2 MiB limit.
const folderCoverCacheFileMaxBytes = 4 << 20

type folderCoverFlight struct {
	done chan struct{}
	body []byte
	err  error
}

type folderCoverSourceVersion struct {
	path    string
	size    int64
	modTime time.Time
	exists  bool
}

// FetchFolderCover shares completed PNGs across preview, Web and Emby requests.
// Only metadata and source file stats are needed for a hit; no artwork is decoded.
func (p *ImageProxy) FetchFolderCover(ctx context.Context, tag string, artworks []EmbyFolderCoverArtwork, width, height int, render func() ([]byte, error)) (body []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := imageVariantCacheDigest("folder-cover-cache:v1", tag, strconv.Itoa(width), strconv.Itoa(height))
	p.folderCoverMu.Lock()
	if flight := p.folderCoverFlights[key]; flight != nil {
		p.folderCoverMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-flight.done:
			return flight.body, flight.err
		}
	}
	flight := &folderCoverFlight{done: make(chan struct{})}
	if p.folderCoverFlights == nil {
		p.folderCoverFlights = make(map[string]*folderCoverFlight)
	}
	p.folderCoverFlights[key] = flight
	p.folderCoverMu.Unlock()
	// Also release waiters if the renderer panics and the HTTP layer recovers.
	err = errors.New("folder cover generation did not complete")
	defer func() {
		p.folderCoverMu.Lock()
		flight.body, flight.err = body, err
		delete(p.folderCoverFlights, key)
		close(flight.done)
		p.folderCoverMu.Unlock()
	}()
	return p.fetchFolderCover(ctx, key, artworks, width, height, render)
}

func (p *ImageProxy) fetchFolderCover(ctx context.Context, key string, artworks []EmbyFolderCoverArtwork, width, height int, render func() ([]byte, error)) ([]byte, error) {
	sources, err := p.folderCoverSources(artworks)
	if err != nil {
		return nil, err
	}
	path := p.folderCoverCachePath(key, sources)
	if body, err := readFolderCoverCache(path, width, height); err != nil || body != nil {
		return body, err
	}
	body, err := render()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateFolderCoverPNG(body, width, height); err != nil {
		return nil, err
	}
	current, err := p.folderCoverSources(artworks)
	if err != nil {
		return nil, err
	}
	for i, source := range sources {
		// A cold remote fetch creates its original cache file during rendering.
		// Existing sources must remain unchanged to publish this derived image.
		if source.exists && source != current[i] {
			return nil, errors.New("folder cover artwork changed during rendering")
		}
	}
	if err := p.writeFolderCoverCache(p.folderCoverCachePath(key, current), body); err != nil {
		return nil, err
	}
	return body, nil
}

func readFolderCoverCache(path string, width, height int) (body []byte, err error) {
	file, err := os.Open(path) // #nosec G304 -- path is SHA-derived under the image variant cache.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open folder cover cache: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close folder cover cache: %w", closeErr))
		}
	}()
	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat folder cover cache: %w", err)
	}
	if !stat.Mode().IsRegular() || stat.Size() <= 0 || stat.Size() > folderCoverCacheFileMaxBytes {
		return nil, errors.New("invalid folder cover cache file")
	}
	if time.Since(stat.ModTime()) >= imageVariantCacheTTL {
		return nil, nil
	}
	body, err = io.ReadAll(io.LimitReader(file, folderCoverCacheFileMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read folder cover cache: %w", err)
	}
	if err := validateFolderCoverPNG(body, width, height); err != nil {
		return nil, err
	}
	return body, nil
}

func validateFolderCoverPNG(body []byte, width, height int) error {
	if len(body) == 0 || len(body) > folderCoverCacheFileMaxBytes {
		return fmt.Errorf("invalid folder cover cache size: %d", len(body))
	}
	config, err := png.DecodeConfig(bytes.NewReader(body))
	if err != nil || config.Width != width || config.Height != height {
		return fmt.Errorf("invalid folder cover PNG: dimensions=%dx%d, decode=%v", config.Width, config.Height, err)
	}
	return nil
}

func (p *ImageProxy) folderCoverSources(artworks []EmbyFolderCoverArtwork) ([]folderCoverSourceVersion, error) {
	sources := make([]folderCoverSourceVersion, 0, len(artworks))
	for _, art := range artworks {
		var path string
		if typ, ref, ok := ParseCloudArtworkURL(art.URL); ok {
			_, path, _ = p.cloudImageCachePaths(typ + ":" + ref)
		} else if isLocalImagePath(art.URL) {
			var err error
			path, err = filepath.Abs(filepath.Clean(art.URL))
			if err != nil || !p.isAllowedLocalPath(path) {
				return nil, errors.New("folder cover local image path is not allowed")
			}
		} else {
			var err error
			_, path, _, err = p.remoteImageCachePaths(art.URL)
			if err != nil {
				return nil, err
			}
		}
		source := folderCoverSourceVersion{path: path}
		if stat, err := os.Stat(path); err == nil {
			source.size, source.modTime, source.exists = stat.Size(), stat.ModTime(), true
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stat folder cover artwork: %w", err)
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func (p *ImageProxy) folderCoverCachePath(key string, sources []folderCoverSourceVersion) string {
	values := []string{key}
	for _, source := range sources {
		values = append(values, source.path, strconv.FormatBool(source.exists), strconv.FormatInt(source.size, 10), strconv.FormatInt(source.modTime.UnixNano(), 10))
	}
	return filepath.Join(p.imageVariantCacheDir(), "folder-covers", imageVariantCacheDigest(values...)+".png")
}

func (p *ImageProxy) writeFolderCoverCache(path string, body []byte) (err error) {
	if len(body) == 0 || len(body) > folderCoverCacheFileMaxBytes {
		return fmt.Errorf("invalid folder cover cache size: %d", len(body))
	}
	p.variantCacheMu.Lock()
	defer func() {
		p.variantCacheMu.Unlock()
		if err == nil {
			p.scheduleImageVariantCachePrune(false)
		}
	}()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create folder cover cache directory: %w", err)
	}
	var previousSize int64
	if stat, err := os.Stat(path); err == nil {
		previousSize = stat.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat folder cover cache for write: %w", err)
	}
	if p.variantCacheBytes-previousSize+int64(len(body)) > imageVariantCacheMaxBytes {
		total, err := p.pruneImageVariantCache(imageVariantCacheMaxBytes - int64(len(body)))
		if err != nil {
			return fmt.Errorf("prune folder cover cache: %w", err)
		}
		p.variantCacheBytes = total
		// Capacity pruning may remove the expired image being replaced.
		previousSize = 0
		if stat, err := os.Stat(path); err == nil {
			previousSize = stat.Size()
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat folder cover cache after prune: %w", err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "cover-*.tmp")
	if err != nil {
		return fmt.Errorf("create folder cover cache file: %w", err)
	}
	defer func() {
		if cleanupErr := os.Remove(tmp.Name()); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary folder cover: %w", cleanupErr))
		}
	}()
	_, writeErr := tmp.Write(body)
	if err := errors.Join(writeErr, tmp.Close()); err != nil {
		return fmt.Errorf("write folder cover cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("publish folder cover cache: %w", err)
	}
	p.variantCacheBytes += int64(len(body)) - previousSize
	return nil
}
