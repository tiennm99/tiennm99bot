package genshin

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
	"golang.org/x/image/math/f64"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
	"golang.org/x/text/unicode/norm"
)

// labelFont is Noto Sans Bold, which covers Vietnamese. See assets/OFL.txt.
//
//go:embed assets/NotoSans-Bold.ttf
var labelFont []byte

var parseFont = sync.OnceValues(func() (*opentype.Font, error) { return opentype.Parse(labelFont) })

const (
	labelLineHeight = 1.12
	labelMaxLines   = 2
	ellipsis        = "…"
)

// starPath is the reveal star's outline in a 24-unit box centred on (12, 12).
var starPath = [][2]float64{
	{12, 1.6}, {15.1, 8.2}, {22.3, 9.1}, {17, 14.1}, {18.4, 21.2},
	{12, 17.7}, {5.6, 21.2}, {7, 14.1}, {1.7, 9.1}, {8.9, 8.2},
}

// overlay draws everything that goes over the clip for one wish.
type overlay struct {
	rarity  int
	palette palette
	tier    tier
	// label is the pre-rendered name with its glow and drop shadow, padded by
	// labelPad on every side so the blur has room.
	label      *image.RGBA
	labelPad   int
	labelLines int
	fontSize   float64
	raster     *vector.Rasterizer
}

func newOverlay(label string, rarity int) (*overlay, error) {
	pal, ok := palettes[rarity]
	if !ok {
		return nil, fmt.Errorf("genshin: rarity %d out of range", rarity)
	}
	o := &overlay{rarity: rarity, palette: pal, tier: tiers[rarity], raster: vector.NewRasterizer(1, 1)}
	if err := o.renderLabel(norm.NFC.String(strings.TrimSpace(label))); err != nil {
		return nil, err
	}
	return o, nil
}

// renderLabel lays the name out in at most two lines across the left part of
// the frame and paints it once, white, over a rarity-coloured glow and a
// dark drop shadow.
func (o *overlay) renderLabel(label string) error {
	parsed, err := parseFont()
	if err != nil {
		return fmt.Errorf("genshin: parse font: %w", err)
	}
	o.fontSize = labelFontSize(label, Height)
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: o.fontSize, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return fmt.Errorf("genshin: font face: %w", err)
	}
	defer func() { _ = face.Close() }()

	boxWidth := Width * 0.44
	lines := wrapLabel(face, label, boxWidth)
	o.labelLines = len(lines)

	lineHeight := o.fontSize * labelLineHeight
	metrics := face.Metrics()
	ascent, descent := float64(metrics.Ascent)/64, float64(metrics.Descent)/64
	baseline := (lineHeight-(ascent+descent))/2 + ascent

	glowBlur := Height * 0.035
	shadowBlur := Height * 0.012
	o.labelPad = int(math.Ceil(glowBlur * 1.5))
	w := int(math.Ceil(boxWidth)) + 2*o.labelPad
	h := int(math.Ceil(lineHeight*float64(len(lines)))) + 2*o.labelPad

	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	drawer := font.Drawer{Dst: mask, Src: image.Opaque, Face: face}
	for i, line := range lines {
		drawer.Dot = fixed.P(o.labelPad, 0)
		drawer.Dot.Y = fixed.Int26_6((float64(o.labelPad) + float64(i)*lineHeight + baseline) * 64)
		drawer.DrawString(line)
	}

	layer := image.NewRGBA(mask.Bounds())
	glow := o.palette.glow
	glow.A = 204
	paintMask(layer, blurAlpha(mask, glowBlur/2), image.Point{}, glow)
	paintMask(layer, blurAlpha(mask, shadowBlur/2), image.Point{Y: 2}, color.NRGBA{0, 0, 0, 204})
	paintMask(layer, mask, image.Point{}, color.NRGBA{0xff, 0xff, 0xff, 0xff})
	o.label = layer
	return nil
}

// wrapLabel breaks label into lines no wider than width, breaking inside a
// word only when the word alone is too wide, and clamps it to two lines
// with an ellipsis.
func wrapLabel(face font.Face, label string, width float64) []string {
	fits := func(s string) bool { return float64(font.MeasureString(face, s))/64 <= width }
	var lines []string
	current := ""
	for _, word := range strings.FieldsFunc(label, unicode.IsSpace) {
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
	if len(lines) <= labelMaxLines {
		return lines
	}
	last := []rune(lines[labelMaxLines-1])
	for len(last) > 0 && !fits(string(last)+ellipsis) {
		last = last[:len(last)-1]
	}
	return append(lines[:labelMaxLines-1], strings.TrimRightFunc(string(last), unicode.IsSpace)+ellipsis)
}

// paintMask fills c through mask, offset by off, over dst.
func paintMask(dst *image.RGBA, mask *image.Alpha, off image.Point, c color.NRGBA) {
	draw.DrawMask(dst, dst.Bounds().Add(off), image.NewUniform(c), image.Point{}, mask, image.Point{}, draw.Over)
}

// blurAlpha approximates a Gaussian blur of standard deviation sigma with
// three box blurs.
func blurAlpha(src *image.Alpha, sigma float64) *image.Alpha {
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

// compose writes frame index's output to dst from the decoded clip frame src.
func (o *overlay) compose(dst, src *image.RGBA, index int) {
	t := float64(index) / FPS

	// The reveal is zoomed slightly so the shake never shows the frame edge.
	if t >= revealStart && o.tier.shake > 0 {
		dx, dy := shakeOffset(t, revealStart, o.tier.shake*Height)
		const s = 1.04
		cx, cy := float64(Width)/2, float64(Height)/2
		m := f64.Aff3{s, 0, cx - s*cx + dx, 0, s, cy - s*cy + dy}
		draw.ApproxBiLinear.Transform(dst, m, src, src.Bounds(), draw.Src, nil)
	} else {
		copy(dst.Pix, src.Pix)
	}

	o.drawName(dst, t)
	o.drawFlash(dst, t)
}

// drawName draws the label and its stars, sliding in from the left.
func (o *overlay) drawName(dst *image.RGBA, t float64) {
	progress := nameProgress(t)
	if progress <= 0 {
		return
	}
	left := Width*0.07 - (1-progress)*Width*0.06
	top := Height * 0.56
	at := image.Pt(int(math.Round(left))-o.labelPad, int(math.Round(top))-o.labelPad)
	opacity := uint8(math.Round(255 * progress))
	draw.DrawMask(dst, o.label.Bounds().Add(at), o.label, image.Point{}, image.NewUniform(color.Alpha{opacity}), image.Point{}, draw.Over)

	starSize := Height * 0.09
	rowTop := top + o.fontSize*labelLineHeight*float64(o.labelLines) + Height*0.025
	for i, appear := range starRevealTimes(o.rarity) {
		if t < appear {
			continue
		}
		pop := interpolate(t, []float64{appear, appear + 0.12, appear + 0.24}, []float64{0, 1.6, 1}, linear)
		flare := interpolate(t, []float64{appear, appear + 0.3}, []float64{1, 0}, linear)
		cx := left + float64(i)*starSize*1.12 + starSize/2
		cy := rowTop + starSize/2
		if flare > 0 {
			glow := starSize * 2.4 * o.tier.starFlare * pop
			// A CSS circle gradient reaches the box's corner; it fades out by 65%.
			drawRadial(dst, cx, cy, 0, glow*math.Sqrt2/2*0.65, starFlareC, 0.9*flare*progress)
		}
		size := starSize * pop
		o.fillStar(dst, cx, cy, size*1.05, starStroke, progress)
		o.fillStar(dst, cx, cy, size*0.985, starFill, progress)
	}
}

// fillStar draws the star outline scaled to size pixels around (cx, cy).
func (o *overlay) fillStar(dst *image.RGBA, cx, cy, size float64, c color.NRGBA, opacity float64) {
	if size < 1 {
		return
	}
	box := image.Rect(int(cx-size/2)-1, int(cy-size/2)-1, int(cx+size/2)+2, int(cy+size/2)+2).Intersect(dst.Bounds())
	if box.Empty() {
		return
	}
	o.raster.Reset(box.Dx(), box.Dy())
	scale := size / 24
	for i, p := range starPath {
		x := float32(cx + (p[0]-12)*scale - float64(box.Min.X))
		y := float32(cy + (p[1]-12)*scale - float64(box.Min.Y))
		if i == 0 {
			o.raster.MoveTo(x, y)
		} else {
			o.raster.LineTo(x, y)
		}
	}
	o.raster.ClosePath()
	c.A = uint8(math.Round(float64(c.A) * opacity))
	o.raster.Draw(dst, box, image.NewUniform(c), image.Point{})
}

// drawFlash draws the impact bloom and the full-frame flash.
func (o *overlay) drawFlash(dst *image.RGBA, t float64) {
	opacity := flashOpacity(t)
	if opacity <= 0 {
		return
	}
	radius := interpolate(t, []float64{flashStart, flashPeak}, []float64{0.1, 2.2}, linear) * Width
	// A CSS circle gradient over the bloom's square box ends at its corner.
	reach := radius * math.Sqrt2
	drawRadial(dst, Width*emblemX, Height*emblemY, reach*0.35, reach*0.7, color.NRGBA{0xff, 0xff, 0xff, 0xff}, math.Min(1, opacity*1.6))
	fill(dst, o.palette.flash, opacity)
}

// drawRadial blends c over dst in a disc around (cx, cy): fully at alpha up
// to inner, fading linearly to nothing at outer.
func drawRadial(dst *image.RGBA, cx, cy, inner, outer float64, c color.NRGBA, alpha float64) {
	if alpha <= 0 || outer <= 0 {
		return
	}
	box := image.Rect(int(cx-outer), int(cy-outer), int(cx+outer)+1, int(cy+outer)+1).Intersect(dst.Bounds())
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			a := alpha
			if d > inner {
				a *= clamp01((outer - d) / (outer - inner))
			}
			if a > 0 {
				blend(dst.Pix[dst.PixOffset(x, y):], c, a)
			}
		}
	}
}

// fill blends c over the whole of dst at alpha.
func fill(dst *image.RGBA, c color.NRGBA, alpha float64) {
	for i := 0; i < len(dst.Pix); i += 4 {
		blend(dst.Pix[i:], c, alpha)
	}
}

// blend mixes an opaque pixel toward c by alpha.
func blend(px []uint8, c color.NRGBA, alpha float64) {
	a := math.Min(1, alpha)
	px[0] = uint8(float64(px[0]) + (float64(c.R)-float64(px[0]))*a + 0.5)
	px[1] = uint8(float64(px[1]) + (float64(c.G)-float64(px[1]))*a + 0.5)
	px[2] = uint8(float64(px[2]) + (float64(c.B)-float64(px[2]))*a + 0.5)
	px[3] = 0xff
}
