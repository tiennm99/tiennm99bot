// Package frames holds the drawing and encoding shared by the animations the
// bot renders in process: the embedded label font, text layout, soft
// shadows, star shapes, pixel blending, and the ffmpeg H.264 encoder.
package frames

import (
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// labelFont is Noto Sans Bold, which covers Vietnamese. See assets/OFL.txt.
//
//go:embed assets/NotoSans-Bold.ttf
var labelFont []byte

var parseFont = sync.OnceValues(func() (*opentype.Font, error) { return opentype.Parse(labelFont) })

// Ellipsis ends a label that WrapText had to cut short.
const Ellipsis = "…"

// Face returns the label font at size pixels. Close it when done.
func Face(size float64) (font.Face, error) {
	parsed, err := parseFont()
	if err != nil {
		return nil, fmt.Errorf("frames: parse font: %w", err)
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil, fmt.Errorf("frames: font face: %w", err)
	}
	return face, nil
}

// TextWidth is the advance width of s in pixels.
func TextWidth(face font.Face, s string) float64 {
	return float64(font.MeasureString(face, s)) / 64
}

// WrapText breaks text into lines no wider than width, breaking inside a word
// only when the word alone is too wide, and clamps it to maxLines with an
// ellipsis.
func WrapText(face font.Face, text string, width float64, maxLines int) []string {
	fits := func(s string) bool { return TextWidth(face, s) <= width }
	var lines []string
	current := ""
	for _, word := range strings.FieldsFunc(text, unicode.IsSpace) {
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if fits(candidate) {
			current = candidate
			continue
		}
		if current != "" {
			lines = append(lines, current)
		}
		current = ""
		for _, r := range word {
			if current != "" && !fits(current+string(r)) {
				lines = append(lines, current)
				current = ""
			}
			current += string(r)
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	if len(lines) <= maxLines {
		return lines
	}
	last := []rune(lines[maxLines-1])
	for len(last) > 0 && !fits(string(last)+Ellipsis) {
		last = last[:len(last)-1]
	}
	return append(lines[:maxLines-1], strings.TrimRightFunc(string(last), unicode.IsSpace)+Ellipsis)
}

// PaintMask fills c through mask, offset by off, over dst.
func PaintMask(dst *image.RGBA, mask *image.Alpha, off image.Point, c color.NRGBA) {
	draw.DrawMask(dst, dst.Bounds().Add(off), image.NewUniform(c), image.Point{}, mask, image.Point{}, draw.Over)
}

// BlurAlpha approximates a Gaussian blur of standard deviation sigma with
// three box blurs.
func BlurAlpha(src *image.Alpha, sigma float64) *image.Alpha {
	radius := int(math.Round((math.Sqrt(1+4*sigma*sigma) - 1) / 2))
	out := image.NewAlpha(src.Bounds())
	copy(out.Pix, src.Pix)
	if radius < 1 {
		return out
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	tmp := make([]uint8, len(out.Pix))
	for range 3 {
		boxBlur(out.Pix, tmp, w, h, radius, 1, w)
		boxBlur(tmp, out.Pix, h, w, radius, w, 1)
	}
	return out
}

// boxBlur averages runs of length 2*radius+1 along one axis: each of `lines`
// lines holds `n` samples `step` apart, and consecutive lines are `stride`
// apart.
func boxBlur(src, dst []uint8, n, lines, radius, step, stride int) {
	span := 2*radius + 1
	for line := range lines {
		base := line * stride
		sum := 0
		for i := -radius; i <= radius; i++ {
			sum += int(src[base+min(max(i, 0), n-1)*step])
		}
		for i := range n {
			// #nosec G115 — the mean of uint8 samples fits in a uint8.
			dst[base+i*step] = uint8(sum / span)
			sum += int(src[base+min(i+radius+1, n-1)*step]) - int(src[base+max(i-radius, 0)*step])
		}
	}
}

// starPath is a five-pointed star in a 24-unit box centred on (12, 12).
var starPath = [][2]float64{
	{12, 1.6}, {15.1, 8.2}, {22.3, 9.1}, {17, 14.1}, {18.4, 21.2},
	{12, 17.7}, {5.6, 21.2}, {7, 14.1}, {1.7, 9.1}, {8.9, 8.2},
}

// FillStar draws a five-pointed star size pixels across, centred on
// (cx, cy), in c at opacity. raster is reused between calls.
func FillStar(raster *vector.Rasterizer, dst *image.RGBA, cx, cy, size float64, c color.NRGBA, opacity float64) {
	if size < 1 || opacity <= 0 {
		return
	}
	box := image.Rect(int(cx-size/2)-1, int(cy-size/2)-1, int(cx+size/2)+2, int(cy+size/2)+2).Intersect(dst.Bounds())
	if box.Empty() {
		return
	}
	raster.Reset(box.Dx(), box.Dy())
	scale := size / 24
	for i, p := range starPath {
		x := float32(cx + (p[0]-12)*scale - float64(box.Min.X))
		y := float32(cy + (p[1]-12)*scale - float64(box.Min.Y))
		if i == 0 {
			raster.MoveTo(x, y)
		} else {
			raster.LineTo(x, y)
		}
	}
	raster.ClosePath()
	c.A = uint8(math.Round(float64(c.A) * math.Min(1, opacity)))
	raster.Draw(dst, box, image.NewUniform(c), image.Point{})
}

// Blend mixes an opaque RGBA pixel toward c by alpha.
func Blend(px []uint8, c color.NRGBA, alpha float64) {
	a := math.Min(1, alpha)
	px[0] = uint8(float64(px[0]) + (float64(c.R)-float64(px[0]))*a + 0.5)
	px[1] = uint8(float64(px[1]) + (float64(c.G)-float64(px[1]))*a + 0.5)
	px[2] = uint8(float64(px[2]) + (float64(c.B)-float64(px[2]))*a + 0.5)
	px[3] = 0xff
}

// DrawRadial blends c over dst in a disc around (cx, cy): fully at alpha up
// to inner, fading linearly to nothing at outer.
func DrawRadial(dst *image.RGBA, cx, cy, inner, outer float64, c color.NRGBA, alpha float64) {
	if alpha <= 0 || outer <= 0 {
		return
	}
	box := image.Rect(int(cx-outer), int(cy-outer), int(cx+outer)+1, int(cy+outer)+1).Intersect(dst.Bounds())
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			a := alpha
			if d > inner {
				a *= math.Max(0, math.Min(1, (outer-d)/(outer-inner)))
			}
			if a > 0 {
				Blend(dst.Pix[dst.PixOffset(x, y):], c, a)
			}
		}
	}
}

// Fill blends c over the whole of dst at alpha.
func Fill(dst *image.RGBA, c color.NRGBA, alpha float64) {
	for i := 0; i < len(dst.Pix); i += 4 {
		Blend(dst.Pix[i:], c, alpha)
	}
}

// TextLayer is text painted once for reuse across frames: white glyphs over
// a soft glow and a drop shadow, padded by Pad pixels on every side so the
// blur has room.
type TextLayer struct {
	Image *image.RGBA
	Pad   int
}

// RenderTextLayer lays lines out width pixels wide, lineHeight apart, either
// centred or left-aligned, with a glow of standard deviation glowSigma and a
// dark drop shadow of shadowSigma.
func RenderTextLayer(face font.Face, lines []string, width, lineHeight float64, centered bool, glow color.NRGBA, glowSigma, shadowSigma float64) TextLayer {
	metrics := face.Metrics()
	ascent, descent := float64(metrics.Ascent)/64, float64(metrics.Descent)/64
	baseline := (lineHeight-(ascent+descent))/2 + ascent
	pad := int(math.Ceil(glowSigma * 3))
	w := int(math.Ceil(width)) + 2*pad
	h := int(math.Ceil(lineHeight*float64(len(lines)))) + 2*pad

	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	drawer := font.Drawer{Dst: mask, Src: image.Opaque, Face: face}
	for i, line := range lines {
		x := float64(pad)
		if centered {
			x += (width - TextWidth(face, line)) / 2
		}
		drawer.Dot = fixed.Point26_6{
			X: fixed.Int26_6(x * 64),
			Y: fixed.Int26_6((float64(pad) + float64(i)*lineHeight + baseline) * 64),
		}
		drawer.DrawString(line)
	}

	layer := image.NewRGBA(mask.Bounds())
	PaintMask(layer, BlurAlpha(mask, glowSigma), image.Point{}, glow)
	PaintMask(layer, BlurAlpha(mask, shadowSigma), image.Point{Y: 2}, color.NRGBA{0, 0, 0, 204})
	PaintMask(layer, mask, image.Point{}, color.NRGBA{0xff, 0xff, 0xff, 0xff})
	return TextLayer{Image: layer, Pad: pad}
}

// Draw blends the layer over dst with its text box's top-left at (x, y).
func (l TextLayer) Draw(dst *image.RGBA, x, y, opacity float64) {
	if opacity <= 0 {
		return
	}
	at := image.Pt(int(math.Round(x))-l.Pad, int(math.Round(y))-l.Pad)
	alpha := uint8(math.Round(255 * math.Min(1, opacity)))
	draw.DrawMask(dst, l.Image.Bounds().Add(at), l.Image, image.Point{}, image.NewUniform(color.Alpha{alpha}), image.Point{}, draw.Over)
}
