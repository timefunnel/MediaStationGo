package handler

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/ShukeBta/MediaStationGo/internal/service"
)

const (
	embyFolderCoverGridLimit    = 4
	embyFolderCoverDefaultWidth = 960
	embyFolderCoverMinWidth     = 160
	embyFolderCoverMaxWidth     = 1200
	embyFolderCoverAspectRatio  = 16.0 / 9.0
)

func serveEmbyFolderCoverImage(svc *service.Container, c *gin.Context, id, imageType string) bool {
	if svc == nil || svc.Emby == nil || svc.ImageProxy == nil {
		return false
	}
	artworks, err := svc.Emby.FolderCoverArtwork(c.Request.Context(), id, imageType, embyFolderCoverGridLimit)
	if err != nil || len(artworks) == 0 {
		if err != nil && svc.Log != nil {
			svc.Log.Warn("library cover selection failed", zap.String("library_id", id), zap.Error(err))
		}
		return false
	}
	width, height := embyFolderCoverDimensions(c)
	body, err := renderLibraryCover(c.Request.Context(), svc, artworks, width, height)
	if err != nil {
		if svc.Log != nil {
			svc.Log.Warn("library cover rendering failed", zap.String("library_id", id), zap.Error(err))
		}
		return false
	}
	tag := service.EmbyFolderCoverTag(id, artworks)
	setEmbyFolderCoverHeaders(c, tag)
	c.Header("Content-Length", strconv.Itoa(len(body)))
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return true
	}
	c.Data(http.StatusOK, "image/png", body)
	return true
}

func fetchEmbyFolderArtwork(ctx context.Context, svc *service.Container, raw string) (image.Image, error) {
	raw = strings.TrimSpace(raw)
	if typ, ref, ok := service.ParseCloudArtworkURL(raw); ok {
		stableKey := typ + ":" + ref
		if data, _, cached := svc.ImageProxy.FetchCloudCached(stableKey); cached {
			return decodeEmbyFolderArtwork(data)
		}
		if svc.StorageCfg == nil {
			return nil, http.ErrMissingFile
		}
		link, err := svc.StorageCfg.CloudResolve(ctx, typ, ref, "")
		if err != nil {
			return nil, err
		}
		data, _, err := svc.ImageProxy.FetchCloudResolved(ctx, stableKey, link)
		if err != nil {
			return nil, err
		}
		return decodeEmbyFolderArtwork(data)
	}
	data, _, err := svc.ImageProxy.Fetch(ctx, raw)
	if err != nil {
		return nil, err
	}
	return decodeEmbyFolderArtwork(data)
}

func decodeEmbyFolderArtwork(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

func buildEmbyFolderCoverGallery(images []image.Image, width, height int) ([]byte, error) {
	if len(images) == 0 || len(images) > embyFolderCoverGridLimit {
		return nil, fmt.Errorf("folder cover requires 1 to %d artworks", embyFolderCoverGridLimit)
	}
	for _, img := range images {
		if img == nil || img.Bounds().Empty() {
			return nil, fmt.Errorf("folder cover artwork has no pixels")
		}
	}
	if width <= 0 {
		width = embyFolderCoverDefaultWidth
	}
	if height <= 0 {
		height = int(math.Round(float64(width) / embyFolderCoverAspectRatio))
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	drawEmbyFolderCoverBackground(dst, images[0])
	rects := embyFolderCoverPosterRects(width, height, images)
	for i, rect := range rects {
		radius := max(1, rect.Dx()/24)
		shadow := roundedEmbyFolderCoverMask(rect.Dx()+2*radius, rect.Dy()+2*radius, 2*radius)
		shadowRect := rect.Inset(-radius).Add(image.Pt(0, radius))
		draw.DrawMask(dst, shadowRect, &image.Uniform{C: color.NRGBA{A: 75}}, image.Point{}, shadow, image.Point{}, draw.Over)
		poster := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
		xdraw.CatmullRom.Scale(poster, poster.Bounds(), images[i], images[i].Bounds(), draw.Src, nil)
		mask := roundedEmbyFolderCoverMask(rect.Dx(), rect.Dy(), radius)
		draw.DrawMask(dst, rect, poster, image.Point{}, mask, image.Point{}, draw.Over)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Use the first poster's full-image mean color at 70% intensity. Premultiplied
// RGB averages transparent pixels over black; no extra artwork is requested.
func drawEmbyFolderCoverBackground(dst *image.RGBA, src image.Image) {
	bounds := src.Bounds()
	var red, green, blue uint64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := src.At(x, y).RGBA()
			red += uint64(r)
			green += uint64(g)
			blue += uint64(b)
		}
	}
	// RGBA returns 16-bit channels. Convert the mean to 8-bit and darken it.
	divisor := uint64(bounds.Dx()) * uint64(bounds.Dy()) * 257 * 10
	background := color.RGBA{uint8(red * 7 / divisor), uint8(green * 7 / divisor), uint8(blue * 7 / divisor), 255}
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: background}, image.Point{}, draw.Src)
}

// Preserve each source's aspect ratio, including libraries with landscape
// artwork. Sparse libraries use the same layout without repeating posters.
func embyFolderCoverPosterRects(width, height int, images []image.Image) []image.Rectangle {
	gap := max(2, int(math.Round(float64(width)*0.018)))
	ratios := make([]float64, len(images))
	totalRatio := 0.0
	for i, img := range images {
		ratios[i] = float64(img.Bounds().Dx()) / float64(img.Bounds().Dy())
		totalRatio += ratios[i]
	}
	availableWidth := float64(width)*0.90 - float64(gap*(len(images)-1))
	posterHeight := max(1, int(math.Floor(math.Min(float64(height)*0.80, availableWidth/totalRatio))))
	posterWidths := make([]int, len(images))
	totalWidth := gap * (len(images) - 1)
	for i, ratio := range ratios {
		posterWidths[i] = max(1, int(math.Round(float64(posterHeight)*ratio)))
		totalWidth += posterWidths[i]
	}
	x := (width - totalWidth) / 2
	rects := make([]image.Rectangle, 0, len(images))
	for i, posterWidth := range posterWidths {
		y := (height - posterHeight) / 2
		if len(images) > 1 {
			y += int(math.Round((float64(i) - float64(len(images)-1)/2) * float64(height) * 0.025))
		}
		rects = append(rects, image.Rect(x, y, x+posterWidth, y+posterHeight))
		x += posterWidth + gap
	}
	return rects
}

func roundedEmbyFolderCoverMask(width, height, radius int) *image.Alpha {
	mask := image.NewAlpha(image.Rect(0, 0, width, height))
	r := float64(min(radius, min(width, height)/2))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			dx := math.Max(r-px, math.Max(px-(float64(width)-r), 0))
			dy := math.Max(r-py, math.Max(py-(float64(height)-r), 0))
			coverage := math.Max(0, math.Min(1, r+0.5-math.Hypot(dx, dy)))
			mask.SetAlpha(x, y, color.Alpha{A: uint8(math.Round(coverage * 255))})
		}
	}
	return mask
}

func embyFolderCoverDimensions(c *gin.Context) (int, int) {
	width := queryMinPositiveInt(c, "maxWidth", "MaxWidth", "width", "Width")
	maxHeight := queryMinPositiveInt(c, "maxHeight", "MaxHeight", "height", "Height")
	if width <= 0 {
		width = embyFolderCoverDefaultWidth
	}
	if maxHeight > 0 {
		heightBoundedWidth := int(float64(maxHeight) * embyFolderCoverAspectRatio)
		if heightBoundedWidth > 0 && heightBoundedWidth < width {
			width = heightBoundedWidth
		}
	}
	width = clampInt(width, embyFolderCoverMinWidth, embyFolderCoverMaxWidth)
	height := int(math.Round(float64(width) / embyFolderCoverAspectRatio))
	if height < 1 {
		height = 1
	}
	return width, height
}

func queryMinPositiveInt(c *gin.Context, keys ...string) int {
	out := 0
	for _, key := range keys {
		for _, value := range c.QueryArray(key) {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				continue
			}
			if out == 0 || n < out {
				out = n
			}
		}
	}
	return out
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func setEmbyFolderCoverHeaders(c *gin.Context, tag string) {
	now := time.Now().UTC()
	c.Header("Content-Type", "image/png")
	c.Header("Cache-Control", "public, max-age=31536000")
	if tag != "" {
		c.Header("ETag", `"`+tag+`"`)
	}
	c.Header("Last-Modified", now.Format(http.TimeFormat))
	c.Header("Expires", now.Add(365*24*time.Hour).Format(http.TimeFormat))
	c.Header("Accept-Ranges", "bytes")
	c.Header("Cross-Origin-Resource-Policy", "cross-origin")
	c.Header("Access-Control-Allow-Origin", "*")
}
