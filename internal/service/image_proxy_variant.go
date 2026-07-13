package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const maxImageVariantInputBytes = 32 << 20

type imageVariantOptions struct {
	maxWidth   int
	maxHeight  int
	quality    int
	hasQuality bool
}

func imageVariantFromRequest(r *http.Request) imageVariantOptions {
	if r == nil || r.URL == nil {
		return imageVariantOptions{}
	}
	q := r.URL.Query()
	return imageVariantOptions{
		maxWidth:   minPositiveQueryInt(q["maxWidth"], q["MaxWidth"], q["width"], q["Width"]),
		maxHeight:  minPositiveQueryInt(q["maxHeight"], q["MaxHeight"], q["height"], q["Height"]),
		quality:    firstPositiveQueryInt(q["quality"], q["Quality"]),
		hasQuality: hasPositiveQueryInt(q["quality"], q["Quality"]),
	}
}

func (o imageVariantOptions) enabled() bool {
	return o.maxWidth > 0 || o.maxHeight > 0 || o.hasQuality
}

func minPositiveQueryInt(values ...[]string) int {
	out := 0
	for _, group := range values {
		for _, value := range group {
			n, ok := parsePositiveImageVariantInt(value)
			if !ok {
				continue
			}
			if out == 0 || n < out {
				out = n
			}
		}
	}
	return out
}

func firstPositiveQueryInt(values ...[]string) int {
	for _, group := range values {
		for _, value := range group {
			if n, ok := parsePositiveImageVariantInt(value); ok {
				return n
			}
		}
	}
	return 0
}

func hasPositiveQueryInt(values ...[]string) bool {
	return firstPositiveQueryInt(values...) > 0
}

func parsePositiveImageVariantInt(raw string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	return n, err == nil && n > 0
}

func serveImageVariant(w http.ResponseWriter, r *http.Request, key string, modTime time.Time, data []byte, contentType, cacheControl string, variant imageVariantOptions) bool {
	out, outType, ok := buildImageVariant(data, contentType, variant)
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", outType)
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("ETag", imageVariantETag(key, variant, out))
	http.ServeContent(w, r, key+".variant", modTime, bytes.NewReader(out))
	return true
}

func buildImageVariant(data []byte, _ string, variant imageVariantOptions) ([]byte, string, bool) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", false
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, "", false
	}
	targetW, targetH := imageVariantTargetSize(width, height, variant)
	if targetW == width && targetH == height && !variant.hasQuality {
		return nil, "", false
	}
	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, xdraw.Over, nil)

	quality := variant.quality
	if quality <= 0 {
		quality = defaultImageVariantQuality
	}
	quality = clampImageQuality(quality)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, "", false
	}
	return buf.Bytes(), "image/jpeg", true
}

func imageVariantTargetSize(width, height int, variant imageVariantOptions) (int, int) {
	scale := 1.0
	if variant.maxWidth > 0 && width > variant.maxWidth {
		scale = math.Min(scale, float64(variant.maxWidth)/float64(width))
	}
	if variant.maxHeight > 0 && height > variant.maxHeight {
		scale = math.Min(scale, float64(variant.maxHeight)/float64(height))
	}
	if scale >= 1 {
		return width, height
	}
	targetW := int(math.Round(float64(width) * scale))
	targetH := int(math.Round(float64(height) * scale))
	if targetW < 1 {
		targetW = 1
	}
	if targetH < 1 {
		targetH = 1
	}
	return targetW, targetH
}

func clampImageQuality(value int) int {
	if value < 35 {
		return 35
	}
	if value > 95 {
		return 95
	}
	return value
}

func imageVariantETag(key string, variant imageVariantOptions, data []byte) string {
	quality := variant.quality
	if quality <= 0 {
		quality = defaultImageVariantQuality
	}
	h := sha256.New()
	_, _ = h.Write([]byte(strings.TrimSpace(key)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.Itoa(variant.maxWidth)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.Itoa(variant.maxHeight)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.Itoa(clampImageQuality(quality))))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(data)
	sum := h.Sum(nil)
	return `"imgv-` + hex.EncodeToString(sum[:12]) + `"`
}

const defaultImageVariantQuality = 82
