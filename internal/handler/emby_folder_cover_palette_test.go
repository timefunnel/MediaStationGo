package handler

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"
)

func TestEmbyFolderCoverPaletteRetainsColorAgainstCommonBeige(t *testing.T) {
	for _, tc := range []struct {
		name string
		rgb  color.RGBA
	}{
		{"pink", color.RGBA{180, 45, 123, 255}},
		{"blue", color.RGBA{40, 94, 185, 255}},
		{"teal", color.RGBA{35, 135, 135, 255}},
		{"green", color.RGBA{47, 127, 68, 255}},
		{"red", color.RGBA{181, 46, 54, 255}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			poster := image.NewRGBA(image.Rect(0, 0, 100, 150))
			draw.Draw(poster, poster.Bounds(), image.NewUniform(color.RGBA{180, 147, 120, 255}), image.Point{}, draw.Src)
			draw.Draw(poster, image.Rect(0, 90, 100, 120), image.NewUniform(tc.rgb), image.Point{}, draw.Src)
			hue, saturation := embyFolderCoverPalette(poster)
			wantHue, _ := embyFolderCoverRGBHue(float64(tc.rgb.R)/255, float64(tc.rgb.G)/255, float64(tc.rgb.B)/255)
			distance := math.Abs(hue - wantHue)
			distance = math.Min(distance, 1-distance)
			if distance > 0.035 || saturation < 0.50 || saturation > 0.63 {
				t.Fatalf("20%% colored region was washed into the common beige: h=%v s=%v want hue=%v", hue, saturation, wantHue)
			}
		})
	}
}

func TestEmbyFolderCoverPalettePreservesWarmAndNeutralArtwork(t *testing.T) {
	for _, rgb := range []color.RGBA{
		{193, 87, 47, 255}, {156, 108, 73, 255},
		{0, 0, 0, 255}, {145, 145, 145, 255}, {255, 255, 255, 255},
	} {
		poster := image.NewRGBA(image.Rect(0, 0, 32, 48))
		draw.Draw(poster, poster.Bounds(), image.NewUniform(rgb), image.Point{}, draw.Src)
		hue, saturation := embyFolderCoverPalette(poster)
		wantHue, sourceSaturation := embyFolderCoverRGBHue(float64(rgb.R)/255, float64(rgb.G)/255, float64(rgb.B)/255)
		if sourceSaturation == 0 {
			if saturation != 0 {
				t.Fatalf("achromatic artwork %v gained an invented color: h=%v s=%v", rgb, hue, saturation)
			}
		} else if math.Abs(hue-wantHue) > 0.01 {
			t.Fatalf("warm artwork %v lost its own hue: got %v want %v", rgb, hue, wantHue)
		}
	}
}

func TestEmbyFolderCoverPaletteIgnoresTinySaturatedMark(t *testing.T) {
	poster := image.NewRGBA(image.Rect(0, 0, 32, 48))
	draw.Draw(poster, poster.Bounds(), image.NewUniform(color.RGBA{170, 150, 138, 255}), image.Point{}, draw.Src)
	draw.Draw(poster, image.Rect(12, 20, 16, 26), image.NewUniform(color.RGBA{0, 0, 255, 255}), image.Point{}, draw.Src)
	hue, _ := embyFolderCoverPalette(poster)
	if hue > 0.15 {
		t.Fatalf("a blue mark occupying less than 2%% displaced the warm palette: hue=%v", hue)
	}
	draw.Draw(poster, poster.Bounds(), image.NewUniform(color.RGBA{145, 145, 145, 255}), image.Point{}, draw.Src)
	draw.Draw(poster, image.Rect(12, 20, 16, 26), image.NewUniform(color.RGBA{0, 0, 255, 255}), image.Point{}, draw.Src)
	if _, saturation := embyFolderCoverPalette(poster); saturation != 0 {
		t.Fatalf("a tiny colored mark should not color an otherwise gray poster: s=%v", saturation)
	}
}

func TestEmbyFolderCoverBackgroundKeepsTitleContrast(t *testing.T) {
	linear := func(v float64) float64 {
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	luminance := func(c color.RGBA) float64 {
		return 0.2126*linear(float64(c.R)/255) + 0.7152*linear(float64(c.G)/255) + 0.0722*linear(float64(c.B)/255)
	}
	foreground := luminance(color.RGBA{247, 244, 242, 255})
	for _, rgb := range []color.RGBA{
		{255, 0, 0, 255}, {255, 255, 0, 255}, {0, 255, 0, 255},
		{0, 255, 255, 255}, {0, 0, 255, 255}, {255, 0, 255, 255},
		{145, 145, 145, 255},
	} {
		poster := image.NewRGBA(image.Rect(0, 0, 32, 48))
		draw.Draw(poster, poster.Bounds(), image.NewUniform(rgb), image.Point{}, draw.Src)
		background := image.NewRGBA(image.Rect(0, 0, 320, 180))
		drawEmbyFolderCoverBackground(background, poster)
		for y := 36; y < 144; y++ {
			for x := 14; x < 127; x++ {
				contrast := (foreground + 0.05) / (luminance(background.RGBAAt(x, y)) + 0.05)
				if contrast < 4.5 {
					t.Fatalf("palette %v has insufficient title contrast at (%d,%d): %.2f", rgb, x, y, contrast)
				}
			}
		}
	}
}
