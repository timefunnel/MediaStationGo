package handler

import (
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"sync"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

//go:embed assets/fonts/NotoSansCJKsc-Bold.otf
var embyFolderCoverFontData []byte

var embyFolderCoverFont = sync.OnceValues(func() (*opentype.Font, error) {
	return opentype.Parse(embyFolderCoverFontData)
})

func embyFolderCoverPalette(src image.Image) (float64, float64) {
	// Sample the entire artwork at a fixed size: color selection is independent
	// of output dimensions, while monochrome artwork naturally gives a neutral palette.
	sample := image.NewRGBA(image.Rect(0, 0, 32, 48))
	xdraw.CatmullRom.Scale(sample, sample.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	type colorBin struct {
		weight float64
		rgb    [3]float64
		count  int
	}
	var bins [24]colorBin
	for y := 0; y < 48; y++ {
		for x := 0; x < 32; x++ {
			c := sample.RGBAAt(x, y)
			r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
			hue, saturation := embyFolderCoverRGBHue(r, g, b)
			hi := max(r, g, b)
			// Dark pixels, whites and grays carry little usable chromatic identity.
			if hi < 0.18 || saturation < 0.18 || hi-min(r, g, b) < 0.08 {
				continue
			}
			// Give a meaningful colored region more influence than a broad washed-out
			// area (often skin, beige scenery or paper), without inventing a hue.
			weight := saturation * saturation * saturation * math.Sqrt(hi)
			index := int(math.Floor(hue*float64(len(bins))+0.5)) % len(bins)
			bin := &bins[index]
			bin.count++
			bin.weight += weight
			bin.rgb[0] += r * weight
			bin.rgb[1] += g * weight
			bin.rgb[2] += b * weight
		}
	}
	dominant, score := 0, 0.0
	for i, bin := range bins {
		previous, next := bins[(i+len(bins)-1)%len(bins)], bins[(i+1)%len(bins)]
		// Require a visible region, so isolated saturated pixels or a tiny logo
		// cannot become the entire background palette.
		if float64(bin.count+previous.count+next.count) < 0.04*32*48 {
			continue
		}
		value := bin.weight + 0.35*(previous.weight+next.weight)
		if value > score {
			dominant, score = i, value
		}
	}
	if score == 0 {
		// With no meaningful chromatic region, use neutral gray lighting.
		return 0, 0
	}
	var rgb [3]float64
	for offset := -1; offset <= 1; offset++ {
		coefficient := 0.35
		if offset == 0 {
			coefficient = 1
		}
		bin := bins[(dominant+offset+len(bins))%len(bins)]
		for channel := range rgb {
			rgb[channel] += bin.rgb[channel] * coefficient / score
		}
	}
	hue, saturation := embyFolderCoverRGBHue(rgb[0], rgb[1], rgb[2])
	return hue, 0.40 + 0.22*saturation
}

func embyFolderCoverRGBHue(r, g, b float64) (float64, float64) {
	hi, lo := max(r, g, b), min(r, g, b)
	span := hi - lo
	if span == 0 {
		return 0, 0
	}
	hue := 0.0
	switch hi {
	case r:
		hue = (g - b) / span
	case g:
		hue = (b-r)/span + 2
	default:
		hue = (r-g)/span + 4
	}
	hue = math.Mod(hue/6+1, 1)
	return hue, span / hi
}

func embyFolderCoverHSL(hue, saturation, lightness float64) [3]float64 {
	hue -= math.Floor(hue)
	chroma := (1 - math.Abs(2*lightness-1)) * saturation
	x := chroma * (1 - math.Abs(math.Mod(hue*6, 2)-1))
	var rgb [3]float64
	switch int(hue * 6) {
	case 0:
		rgb = [3]float64{chroma, x, 0}
	case 1:
		rgb = [3]float64{x, chroma, 0}
	case 2:
		rgb = [3]float64{0, chroma, x}
	case 3:
		rgb = [3]float64{0, x, chroma}
	case 4:
		rgb = [3]float64{x, 0, chroma}
	default:
		rgb = [3]float64{chroma, 0, x}
	}
	for i := range rgb {
		rgb[i] += lightness - chroma/2
	}
	return rgb
}

func blurEmbyFolderCoverAlpha(src *image.Alpha, radius int) *image.Alpha {
	bounds := src.Bounds()
	tmp, dst := image.NewAlpha(bounds), image.NewAlpha(bounds)
	window := 2*radius + 1
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		sum := 0
		for x := bounds.Min.X - radius; x <= bounds.Min.X+radius; x++ {
			sum += int(src.AlphaAt(x, y).A)
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			tmp.SetAlpha(x, y, color.Alpha{A: uint8(sum / window)})
			sum += int(src.AlphaAt(x+radius+1, y).A) - int(src.AlphaAt(x-radius, y).A)
		}
	}
	for x := bounds.Min.X; x < bounds.Max.X; x++ {
		sum := 0
		for y := bounds.Min.Y - radius; y <= bounds.Min.Y+radius; y++ {
			sum += int(tmp.AlphaAt(x, y).A)
		}
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			dst.SetAlpha(x, y, color.Alpha{A: uint8(sum / window)})
			sum += int(tmp.AlphaAt(x, y+radius+1).A) - int(tmp.AlphaAt(x, y-radius).A)
		}
	}
	return dst
}

func drawEmbyFolderCoverTitle(dst *image.RGBA, title string) error {
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return fmt.Errorf("媒体库名称为空，无法生成入口封面")
	}
	parsed, err := embyFolderCoverFont()
	if err != nil {
		return fmt.Errorf("媒体库封面字体加载失败：%w", err)
	}
	var buffer sfnt.Buffer
	for _, r := range title {
		glyph, err := parsed.GlyphIndex(&buffer, r)
		if err != nil {
			return fmt.Errorf("媒体库名称字形读取失败：%w", err)
		}
		if glyph == 0 {
			return fmt.Errorf("媒体库名称包含字体不支持的字符 %q", r)
		}
	}
	bounds := dst.Bounds()
	maxWidth := fixed.I(int(float64(bounds.Dx()) * 0.35))
	size := math.Max(1, float64(bounds.Dy())*0.15)
	minSize := math.Max(1, float64(bounds.Dy())*0.085)
	var face font.Face
	for {
		face, err = opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
		if err != nil {
			return fmt.Errorf("媒体库封面字体初始化失败：%w", err)
		}
		if font.MeasureString(face, title) <= maxWidth || size <= minSize {
			break
		}
		if err := face.Close(); err != nil {
			return err
		}
		size = math.Max(minSize, size-1)
	}
	defer face.Close()
	lines := embyFolderCoverTitleLines(title, face, maxWidth)
	metrics := face.Metrics()
	lineHeight := metrics.Height.Ceil()
	x := int(float64(bounds.Dx()) * 0.046)
	y := bounds.Dy()/2 + (metrics.Ascent.Ceil()-metrics.Descent.Ceil())/2 - (len(lines)-1)*lineHeight/2
	drawer := font.Drawer{Dst: dst, Face: face}
	for _, line := range lines {
		drawer.Src = &image.Uniform{C: color.NRGBA{A: 45}}
		drawer.Dot = fixed.P(x, y+max(1, bounds.Dy()/540))
		drawer.DrawString(line)
		drawer.Src = &image.Uniform{C: color.RGBA{247, 244, 242, 255}}
		drawer.Dot = fixed.P(x, y)
		drawer.DrawString(line)
		y += lineHeight
	}
	return nil
}

func embyFolderCoverTitleLines(title string, face font.Face, width fixed.Int26_6) []string {
	lines := []string{""}
	for _, r := range title {
		i := len(lines) - 1
		next := lines[i] + string(r)
		if lines[i] != "" && font.MeasureString(face, next) > width {
			if len(lines) == 2 {
				runes := []rune(lines[i])
				for len(runes) > 0 && font.MeasureString(face, string(runes)+"…") > width {
					runes = runes[:len(runes)-1]
				}
				lines[i] = string(runes) + "…"
				return lines
			}
			lines = append(lines, string(r))
		} else {
			lines[i] = next
		}
	}
	return lines
}
