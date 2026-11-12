package genshin

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/vector"
	"golang.org/x/text/unicode/norm"

	"github.com/tiennm99/tiennm99bot/internal/modules/random/frames"
)

const (
	labelLineHeight = 1.12
	labelMaxLines   = 2
)

// overlay draws everything that goes over the clip for one wish.
type overlay struct {
	rarity  int
	palette palette
	tier    tier
	// label is the pre-rendered name with its glow and drop shadow.
	label      frames.TextLayer
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
	o.fontSize = labelFontSize(label, Height)
	face, err := frames.Face(o.fontSize)
	if err != nil {
		return err
	}
	defer func() { _ = face.Close() }()

	boxWidth := Width * 0.44
	lines := frames.WrapText(face, label, boxWidth, labelMaxLines)
	o.labelLines = len(lines)
	glow := o.palette.glow
	glow.A = 204
	o.label = frames.RenderTextLayer(face, lines, boxWidth, o.fontSize*labelLineHeight, false, glow, Height*0.035/2, Height*0.012/2)
	return nil
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
	o.label.Draw(dst, left, top, progress)

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
			frames.DrawRadial(dst, cx, cy, 0, glow*math.Sqrt2/2*0.65, starFlareC, 0.9*flare*progress)
		}
		size := starSize * pop
		frames.FillStar(o.raster, dst, cx, cy, size*1.05, starStroke, progress)
		frames.FillStar(o.raster, dst, cx, cy, size*0.985, starFill, progress)
	}
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
	frames.DrawRadial(dst, Width*emblemX, Height*emblemY, reach*0.35, reach*0.7, color.NRGBA{0xff, 0xff, 0xff, 0xff}, math.Min(1, opacity*1.6))
	frames.Fill(dst, o.palette.flash, opacity)
}
