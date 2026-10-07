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
	"golang.org/x/image/math/f64"
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
	if svc == nil || svc.Repo == nil || svc.Repo.Library == nil || svc.Emby == nil || svc.ImageProxy == nil {
		return false
	}
	lib, err := svc.Repo.Library.FindByID(c.Request.Context(), id)
	if err != nil || lib == nil {
		if err != nil && svc.Log != nil {
			svc.Log.Warn("library cover name lookup failed", zap.String("library_id", id), zap.Error(err))
		}
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
	body, err := renderLibraryCover(c.Request.Context(), svc, lib.Name, artworks, width, height)
	if err != nil {
		if svc.Log != nil {
			svc.Log.Warn("library cover rendering failed", zap.String("library_id", id), zap.Error(err))
		}
		return false
	}
	tag := service.EmbyFolderCoverTag(id, lib.Name, artworks)
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

func buildEmbyFolderCoverGallery(images []image.Image, title string, width, height int) ([]byte, error) {
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
	for i, placement := range embyFolderCoverPosterPlacements(width, height, images) {
		rect := image.Rect(0, 0, placement.width, placement.height)
		radius := max(1, int(math.Round(float64(width)*0.008)))
		poster := image.NewRGBA(rect)
		xdraw.CatmullRom.Scale(poster, poster.Bounds(), images[i], images[i].Bounds(), draw.Src, nil)
		mask := roundedEmbyFolderCoverMask(rect.Dx(), rect.Dy(), radius)
		clipped := image.NewRGBA(rect)
		draw.DrawMask(clipped, rect, poster, image.Point{}, mask, image.Point{}, draw.Src)
		blur := max(1, width/160)
		padding := 3 * blur
		shadowBounds := image.Rect(0, 0, rect.Dx()+2*padding, rect.Dy()+2*padding)
		shadowMask := image.NewAlpha(shadowBounds)
		draw.Draw(shadowMask, rect.Add(image.Pt(padding, padding)), mask, image.Point{}, draw.Src)
		for pass := 0; pass < 3; pass++ {
			shadowMask = blurEmbyFolderCoverAlpha(shadowMask, blur)
		}
		shadow := image.NewRGBA(shadowBounds)
		draw.DrawMask(shadow, shadowBounds, &image.Uniform{C: color.NRGBA{A: 42}}, image.Point{}, shadowMask, image.Point{}, draw.Src)
		shadowPlacement := placement
		shadowPlacement.y += float64(height) * 0.012
		xdraw.CatmullRom.Transform(dst, shadowPlacement.matrix(shadowBounds.Dx(), shadowBounds.Dy()), shadow, shadowBounds, draw.Over, nil)
		xdraw.CatmullRom.Transform(dst, placement.matrix(rect.Dx(), rect.Dy()), clipped, rect, draw.Over, nil)
	}
	if err := drawEmbyFolderCoverTitle(dst, title); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// The first poster supplies a muted palette. Broad smooth lighting and stable,
// low-amplitude grain create the textured background without another image.
func drawEmbyFolderCoverBackground(dst *image.RGBA, src image.Image) {
	hue, saturation := embyFolderCoverPalette(src)
	dark := embyFolderCoverHSL(hue, saturation, 0.27)
	light := embyFolderCoverHSL(hue+0.025, saturation*0.80, 0.60)
	bounds := dst.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		ny := (float64(y-bounds.Min.Y) + 0.5) / float64(bounds.Dy())
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			nx := (float64(x-bounds.Min.X) + 0.5) / float64(bounds.Dx())
			glow := 0.13 * math.Exp(-math.Pow((nx-0.27)/0.37, 2)-math.Pow((ny-0.22)/0.55, 2))
			mix := min(1.0, max(0.0, 0.12+0.59*nx+0.19*(1-ny)+glow))
			// A broad left shade keeps white titles readable even on yellow/green
			// palettes; ease it out toward the artwork rather than desaturating it.
			shade := min(1.0, max(0.0, (nx-0.40)/0.40))
			shade = 0.67 + 0.33*shade*shade*(3-2*shade)
			seed := uint32(x-bounds.Min.X)*0x9e3779b1 ^ uint32(y-bounds.Min.Y)*0x85ebca77
			seed ^= seed >> 16
			seed *= 0x7feb352d
			seed ^= seed >> 15
			grain := float64(seed&255)/255*2.4 - 1.2
			var rgb [3]uint8
			for channel := range rgb {
				v := (dark[channel]*(1-mix)+light[channel]*mix)*255*shade + grain
				rgb[channel] = uint8(math.Round(min(255.0, max(0.0, v))))
			}
			dst.SetRGBA(x, y, color.RGBA{rgb[0], rgb[1], rgb[2], 255})
		}
	}
}

type embyFolderCoverPosterPlacement struct {
	x, y          float64
	width, height int
}

func (p embyFolderCoverPosterPlacement) matrix(width, height int) f64.Aff3 {
	angle := 15.0 * math.Pi / 180
	cos, sin := math.Cos(angle), math.Sin(angle)
	return f64.Aff3{cos, -sin, p.x - cos*float64(width)/2 + sin*float64(height)/2, sin, cos, p.y - sin*float64(width)/2 - cos*float64(height)/2}
}

// Each selected image appears once, at its original aspect ratio. The wall
// deliberately extends beyond the canvas; the library title keeps its own area.
func embyFolderCoverPosterPlacements(width, height int, images []image.Image) []embyFolderCoverPosterPlacement {
	w, h := float64(width), float64(height)
	positions := [][2]float64{{0.66, 0.18}, {0.915, 0.34}, {0.56, 0.82}, {0.815, 0.98}}
	maxWidth, maxHeight := 0.25*w, 0.61*h
	switch len(images) {
	case 1:
		positions = [][2]float64{{0.75, 0.50}}
		maxWidth, maxHeight = 0.40*w, 0.86*h
	case 2:
		positions = [][2]float64{{0.64, 0.36}, {0.91, 0.66}}
		maxWidth, maxHeight = 0.28*w, 0.78*h
	}
	placements := make([]embyFolderCoverPosterPlacement, 0, len(images))
	for i, img := range images {
		ratio := float64(img.Bounds().Dx()) / float64(img.Bounds().Dy())
		posterHeight := min(maxHeight, maxWidth/ratio)
		placements = append(placements, embyFolderCoverPosterPlacement{
			x: positions[i][0] * w, y: positions[i][1] * h,
			width: max(1, int(math.Round(posterHeight*ratio))), height: max(1, int(math.Round(posterHeight))),
		})
	}
	return placements
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
