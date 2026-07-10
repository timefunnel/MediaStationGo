package service

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/ShukeBta/MediaStationGo/internal/model"
)

const adultDerivedPosterStrategy = "adult-right-half-poster-v1"

var adultDerivedPosterSize = image.Pt(600, 900)

func (s *ScraperService) deriveAdultPosterIfNeeded(ctx context.Context, m *model.Media, lib *model.Library, match *Match) {
	if s == nil || s.images == nil || match == nil || !adultScrapeNeedsDerivedPoster(lib, match) {
		return
	}
	if strings.TrimSpace(match.PosterURL) != "" || strings.TrimSpace(match.BackdropURL) == "" {
		return
	}
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	data, _, err := s.images.Fetch(fetchCtx, match.BackdropURL)
	cancel()
	if err != nil {
		s.log.Warn("adult poster derivation skipped; backdrop fetch failed",
			zap.String("media_id", mediaIDForLog(m)),
			zap.String("backdrop", match.BackdropURL),
			zap.Error(err))
		return
	}
	posterPath, err := s.writeAdultPosterFromBackdrop(match.BackdropURL, data)
	if err != nil {
		s.log.Warn("adult poster derivation skipped; poster generation failed",
			zap.String("media_id", mediaIDForLog(m)),
			zap.String("backdrop", match.BackdropURL),
			zap.Error(err))
		return
	}
	match.PosterURL = posterPath
}

func adultScrapeNeedsDerivedPoster(lib *model.Library, match *Match) bool {
	if match == nil {
		return false
	}
	if match.NSFW || strings.EqualFold(match.MediaType, "adult") {
		return true
	}
	return lib != nil && strings.EqualFold(lib.Type, "adult")
}

func (s *ScraperService) writeAdultPosterFromBackdrop(backdropURL string, data []byte) (string, error) {
	if s == nil || s.images == nil {
		return "", errors.New("image proxy missing")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	bounds := img.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return "", errors.New("invalid image bounds")
	}
	if bounds.Dx() <= bounds.Dy() {
		return "", errors.New("backdrop is not landscape")
	}
	crop := image.Rect(bounds.Min.X+bounds.Dx()/2, bounds.Min.Y, bounds.Max.X, bounds.Max.Y)
	targetRatio := float64(adultDerivedPosterSize.X) / float64(adultDerivedPosterSize.Y)
	cropRatio := float64(crop.Dx()) / float64(crop.Dy())
	if cropRatio > targetRatio {
		desiredW := int(float64(crop.Dy()) * targetRatio)
		if desiredW < 1 {
			desiredW = 1
		}
		crop.Min.X = crop.Max.X - desiredW
	} else if cropRatio < targetRatio {
		desiredH := int(float64(crop.Dx()) / targetRatio)
		if desiredH < 1 {
			desiredH = 1
		}
		extra := crop.Dy() - desiredH
		if extra > 0 {
			crop.Min.Y += extra / 2
			crop.Max.Y = crop.Min.Y + desiredH
		}
	}

	sum := sha1.Sum([]byte(adultDerivedPosterStrategy + "\n" + backdropURL + "\n" + hex.EncodeToString(sha1Bytes(data))))
	name := hex.EncodeToString(sum[:])[:24] + ".jpg"
	dir := filepath.Join(s.images.cacheDir, "adult-posters")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return filepath.Abs(path)
	}

	out := image.NewRGBA(image.Rect(0, 0, adultDerivedPosterSize.X, adultDerivedPosterSize.Y))
	draw.CatmullRom.Scale(out, out.Bounds(), img, crop, draw.Over, nil)
	tmp, err := os.CreateTemp(dir, "adult-poster-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	encodeErr := jpeg.Encode(tmp, out, &jpeg.Options{Quality: 90})
	closeErr := tmp.Close()
	if encodeErr != nil {
		_ = os.Remove(tmpPath)
		return "", encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", closeErr
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return filepath.Abs(path)
}

func sha1Bytes(data []byte) []byte {
	sum := sha1.Sum(data)
	return sum[:]
}

func mediaIDForLog(m *model.Media) string {
	if m == nil {
		return ""
	}
	return m.ID
}
