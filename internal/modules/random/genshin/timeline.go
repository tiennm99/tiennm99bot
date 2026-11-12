// Package genshin renders the /genshin meteor wish in-process: a pre-rendered
// clip per rarity (a meteor falling over a night sea, then a 3D rank emblem;
// built in Blender by tools/genshin-wish) with the white impact flash, the
// impact shake, the label, and its stars drawn over it in Go. ffmpeg decodes
// the clip and encodes the result.
package genshin

import (
	"image/color"
	"math"
	"unicode/utf8"
)

// Output frame size and timing. Telegram plays the silent H.264 MP4 as an
// animation.
const (
	Width           = 640
	Height          = 360
	FPS             = 30
	DurationSeconds = 7
	frameCount      = FPS * DurationSeconds
)

// Seconds from the start of the clip. The meteor lands at 2.5s, the white
// flash peaks and holds, and the clip cuts to the emblem reveal at
// revealStart, while the flash is opaque. They must match the clip.
const (
	flashStart   = 2.3
	flashPeak    = 2.7
	flashHoldEnd = 2.95
	flashEnd     = 3.5
	revealStart  = 2.95
	nameIn       = 3.6
	nameSettled  = 4.1
	starsStart   = 4.1
	starStep     = 0.18
)

// The rank emblem's centre, where the meteor strikes, as fractions of the
// frame. The clip is rendered with the same point.
const (
	emblemX = 0.66
	emblemY = 0.47
)

var (
	// starFill is the same gold at every rarity, as in the source game.
	starFill   = color.NRGBA{0xff, 0xcc, 0x33, 0xff}
	starStroke = color.NRGBA{0xff, 0xf6, 0xcf, 0xff}
	starFlareC = color.NRGBA{0xff, 0xf4, 0xc2, 0xff}
)

// palette is the rarity colour of the label glow and the 5★ flash tint.
type palette struct {
	glow  color.NRGBA
	flash color.NRGBA
}

var palettes = map[int]palette{
	3: {glow: color.NRGBA{0x6f, 0xb6, 0xff, 0xff}, flash: color.NRGBA{0xff, 0xff, 0xff, 0xff}},
	4: {glow: color.NRGBA{0xc8, 0x8c, 0xff, 0xff}, flash: color.NRGBA{0xff, 0xff, 0xff, 0xff}},
	5: {glow: color.NRGBA{0xff, 0xd3, 0x6b, 0xff}, flash: color.NRGBA{0xff, 0xf6, 0xdc, 0xff}},
}

// tier is the loudness drawn over the clip; the clip itself scales the
// meteor, rays, rings, and particles by tier.
type tier struct {
	shake     float64 // impact shake amplitude, as a fraction of height
	starFlare float64 // size multiplier of each star's pop flare
}

var tiers = map[int]tier{
	3: {shake: 0, starFlare: 1},
	4: {shake: 0.012, starFlare: 1.4},
	5: {shake: 0.03, starFlare: 2},
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func easeInOutQuad(t float64) float64 {
	if t < 0.5 {
		return 2 * t * t
	}
	u := -2*t + 2
	return 1 - u*u/2
}

func easeOutCubic(t float64) float64 {
	u := 1 - t
	return 1 - u*u*u
}

func linear(t float64) float64 { return t }

// interpolate maps t through the piecewise-linear curve (xs, ys), clamped at
// both ends, applying ease within each segment.
func interpolate(t float64, xs, ys []float64, ease func(float64) float64) float64 {
	if t <= xs[0] {
		return ys[0]
	}
	for i := 1; i < len(xs); i++ {
		if t <= xs[i] {
			p := ease((t - xs[i-1]) / (xs[i] - xs[i-1]))
			return ys[i-1] + (ys[i]-ys[i-1])*p
		}
	}
	return ys[len(ys)-1]
}

// flashOpacity is the white flash's opacity: it swells as the meteor lands,
// holds over the cut to the reveal, then fades.
func flashOpacity(t float64) float64 {
	return interpolate(t, []float64{flashStart, flashPeak, flashHoldEnd, flashEnd}, []float64{0, 1, 1, 0}, easeInOutQuad)
}

// nameProgress slides and fades the label in, 0 to 1.
func nameProgress(t float64) float64 {
	return interpolate(t, []float64{nameIn, nameSettled}, []float64{0, 1}, easeOutCubic)
}

// starRevealTimes gives the second each reveal star pops in, one per rarity
// level.
func starRevealTimes(rarity int) []float64 {
	times := make([]float64, rarity)
	for i := range times {
		times[i] = starsStart + float64(i)*starStep
	}
	return times
}

// labelFontSize shrinks long labels so they fit within two lines on the left
// half of the frame.
func labelFontSize(label string, height int) float64 {
	h := float64(height)
	switch n := utf8.RuneCountInString(label); {
	case n <= 8:
		return math.Round(h * 0.12)
	case n <= 16:
		return math.Round(h * 0.09)
	case n <= 28:
		return math.Round(h * 0.07)
	default:
		return math.Round(h * 0.055)
	}
}

// shakeOffset is the decaying impact shake in pixels: zero before start and
// once it settles 0.8s later.
func shakeOffset(t, start, amplitude float64) (x, y float64) {
	elapsed := t - start
	if amplitude <= 0 || elapsed < 0 || elapsed > 0.8 {
		return 0, 0
	}
	remaining := 1 - elapsed/0.8
	decay := amplitude * remaining * remaining
	return math.Sin(elapsed*71) * decay, math.Cos(elapsed*53) * decay
}
