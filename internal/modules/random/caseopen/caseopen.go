// Package caseopen renders the /csgo case opening in process: a reel of item
// cards spins past a centre marker, slows down, and stops on the winner,
// which lights up while its name and stars appear below. Every frame is
// drawn in Go; ffmpeg encodes the MP4.
package caseopen

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
	"golang.org/x/text/unicode/norm"

	"github.com/tiennm99/tiennm99bot/internal/modules/random/frames"
)

// Output frame size and timing.
const (
	Width           = 640
	Height          = 360
	FPS             = 30
	DurationSeconds = 7
	frameCount      = FPS * DurationSeconds
)

// Seconds from the start of the clip.
const (
	fadeInEnd     = 0.3
	spinStart     = 0.15
	spinEnd       = 5.2
	highlightEnd  = 5.6
	resultIn      = 5.45
	resultSettled = 5.95
	starsStart    = 5.85
	starStep      = 0.1
	shineStart    = 5.5
	shineEnd      = 6.3
)

// Reel layout, in pixels.
const (
	cellW      = 132
	cellH      = 118
	cellGap    = 8
	pitch      = cellW + cellGap
	reelTop    = 76
	bandPad    = 12
	edgeFade   = 110
	reelLength = 38
	// winnerSlot leaves enough cells after the winner to fill the frame.
	winnerSlot = reelLength - 7
)

// renderTimeout bounds one opening; it renders in about a second.
const renderTimeout = 30 * time.Second

// Item is one entry that can land on the reel.
type Item struct {
	Label  string
	Rarity int // 3 (blue), 4 (purple), or 5 (gold)
}

var rarityColors = map[int]color.NRGBA{
	3: {0x4b, 0x69, 0xff, 0xff},
	4: {0x88, 0x47, 0xff, 0xff},
	5: {0xe4, 0xae, 0x39, 0xff},
}

var (
	backgroundTop    = color.NRGBA{0x16, 0x18, 0x1e, 0xff}
	backgroundBottom = color.NRGBA{0x0a, 0x0b, 0x0f, 0xff}
	bandColor        = color.NRGBA{0x1b, 0x1e, 0x25, 0xff}
	bandLine         = color.NRGBA{0x34, 0x38, 0x42, 0xff}
	markerColor      = color.NRGBA{0xff, 0xc8, 0x3c, 0xff}
	starColor        = color.NRGBA{0xff, 0xcc, 0x33, 0xff}
	black            = color.NRGBA{0, 0, 0, 0xff}
	white            = color.NRGBA{0xff, 0xff, 0xff, 0xff}
)

// Render returns the opening that lands on items[winner] as a silent H.264
// MP4 of Width x Height, DurationSeconds long. seed lays out the reel and
// where inside the winning card the marker stops.
func Render(ctx context.Context, items []Item, winner int, seed uint64) ([]byte, error) {
	if winner < 0 || winner >= len(items) {
		return nil, fmt.Errorf("caseopen: winner %d out of range", winner)
	}
	for _, item := range items {
		if strings.TrimSpace(item.Label) == "" {
			return nil, errors.New("caseopen: empty label")
		}
		if _, ok := rarityColors[item.Rarity]; !ok {
			return nil, fmt.Errorf("caseopen: rarity %d out of range", item.Rarity)
		}
	}
	scene, err := newScene(items, winner, seed)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	encoder, err := frames.NewEncoder(ctx, Width, Height, FPS)
	if err != nil {
		return nil, err
	}
	frame := image.NewRGBA(image.Rect(0, 0, Width, Height))
	for index := range frameCount {
		scene.draw(frame, float64(index)/FPS)
		if err := encoder.WriteFrame(frame); err != nil {
			encoder.Abort()
			return nil, err
		}
	}
	return encoder.Finish()
}

// scene is everything pre-rendered for one opening.
type scene struct {
	winner     Item
	reel       []int // item index on each reel slot
	cards      []*image.RGBA
	winnerGlow *image.RGBA
	glowPad    int
	background *image.RGBA
	band       *image.RGBA
	sums       []uint32
	finalShift float64
	result     frames.TextLayer
	resultW    float64
	resultH    float64
	raster     *vector.Rasterizer
}

func newScene(items []Item, winner int, seed uint64) (*scene, error) {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	s := &scene{
		winner: items[winner],
		reel:   make([]int, reelLength),
		raster: vector.NewRasterizer(1, 1),
		band:   image.NewRGBA(image.Rect(0, 0, Width, cellH)),
		sums:   make([]uint32, Width*cellH*4),
	}
	for i := range s.reel {
		s.reel[i] = rng.IntN(len(items))
	}
	s.reel[winnerSlot] = winner
	// The marker stops somewhere inside the winning card, never on its edge.
	landing := cellW * (0.2 + 0.6*rng.Float64())
	s.finalShift = winnerSlot*pitch + landing - Width/2

	labelFace, err := frames.Face(14)
	if err != nil {
		return nil, err
	}
	defer func() { _ = labelFace.Close() }()
	iconFace, err := frames.Face(52)
	if err != nil {
		return nil, err
	}
	defer func() { _ = iconFace.Close() }()
	s.cards = make([]*image.RGBA, len(items))
	for i, item := range items {
		s.cards[i] = drawCard(item, labelFace, iconFace)
	}
	s.winnerGlow, s.glowPad = drawGlow(rarityColors[s.winner.Rarity])
	s.background = drawBackground()

	resultFace, err := frames.Face(30)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resultFace.Close() }()
	s.resultW = Width * 0.8
	lines := frames.WrapText(resultFace, clean(s.winner.Label), s.resultW, 2)
	s.resultH = 30 * 1.15 * float64(len(lines))
	glow := rarityColors[s.winner.Rarity]
	glow.A = 220
	s.result = frames.RenderTextLayer(resultFace, lines, s.resultW, 30*1.15, true, glow, 7, 2.5)
	return s, nil
}

func clean(label string) string { return norm.NFC.String(strings.TrimSpace(label)) }

// shift is how far the reel has scrolled at t, easing out to a stop.
func (s *scene) shift(t float64) float64 {
	p := math.Max(0, math.Min(1, (t-spinStart)/(spinEnd-spinStart)))
	u := 1 - p
	return s.finalShift * (1 - u*u*u*u)
}

func (s *scene) draw(dst *image.RGBA, t float64) {
	copy(dst.Pix, s.background.Pix)
	s.drawReel(dst, t)
	s.drawHighlight(dst, t)
	s.drawMarker(dst)
	s.drawResult(dst, t)
	if t < fadeInEnd {
		frames.Fill(dst, black, 1-t/fadeInEnd)
	}
}

// drawReel draws the visible cards, averaging positions a few pixels apart
// over half of the last frame's travel while the reel moves fast, as a
// camera's motion blur.
func (s *scene) drawReel(dst *image.RGBA, t float64) {
	now := s.shift(t)
	spread := (now - s.shift(t-1.0/FPS)) * 0.5
	samples := max(1, min(32, int(spread/3)))
	if samples == 1 {
		s.paintBand(now)
		s.blitBand(dst)
		return
	}
	clear(s.sums)
	for j := range samples {
		s.paintBand(now - spread*float64(j)/float64(samples))
		for i, v := range s.band.Pix {
			s.sums[i] += uint32(v)
		}
	}
	n := uint32(samples)
	for i, v := range s.sums {
		// #nosec G115 — the mean of uint8 samples fits in a uint8.
		s.band.Pix[i] = uint8(v / n)
	}
	s.blitBand(dst)
}

// paintBand draws the cards into the band buffer with the reel scrolled by
// shift pixels.
func (s *scene) paintBand(shift float64) {
	draw.Draw(s.band, s.band.Bounds(), image.NewUniform(bandColor), image.Point{}, draw.Src)
	first := max(0, int(math.Floor(shift/pitch)))
	for slot := first; slot < reelLength; slot++ {
		x := int(math.Round(float64(slot*pitch) - shift))
		if x >= Width {
			break
		}
		card := s.cards[s.reel[slot]]
		draw.Draw(s.band, card.Bounds().Add(image.Pt(x, 0)), card, image.Point{}, draw.Src)
	}
}

// blitBand copies the band into the frame, darkening it toward both edges.
func (s *scene) blitBand(dst *image.RGBA) {
	for y := range cellH {
		row := dst.Pix[dst.PixOffset(0, reelTop+y):]
		copy(row[:Width*4], s.band.Pix[y*s.band.Stride:])
		for x := range edgeFade {
			fade := 1 - float64(x)/edgeFade
			fade *= fade
			frames.Blend(row[x*4:], bandColor, fade)
			frames.Blend(row[(Width-1-x)*4:], bandColor, fade)
		}
	}
}

// winnerRect is where the winning card sits once the reel stops.
func (s *scene) winnerRect() image.Rectangle {
	x := int(math.Round(winnerSlot*pitch - s.finalShift))
	return image.Rect(x, reelTop, x+cellW, reelTop+cellH)
}

// drawHighlight dims the other cards, rings the winner in its rarity colour,
// and sweeps a shine across a gold winner.
func (s *scene) drawHighlight(dst *image.RGBA, t float64) {
	p := math.Max(0, math.Min(1, (t-spinEnd)/(highlightEnd-spinEnd)))
	if p <= 0 {
		return
	}
	win := s.winnerRect()
	band := image.Rect(0, reelTop, Width, reelTop+cellH)
	for y := band.Min.Y; y < band.Max.Y; y++ {
		for x := band.Min.X; x < band.Max.X; x++ {
			if !image.Pt(x, y).In(win) {
				frames.Blend(dst.Pix[dst.PixOffset(x, y):], black, 0.6*p)
			}
		}
	}
	glowAt := win.Min.Sub(image.Pt(s.glowPad, s.glowPad))
	pulse := 0.75 + 0.25*math.Sin((t-spinEnd)*5)
	draw.DrawMask(dst, s.winnerGlow.Bounds().Add(glowAt), s.winnerGlow, image.Point{},
		image.NewUniform(color.Alpha{uint8(255 * p * pulse)}), image.Point{}, draw.Over)
	card := s.cards[s.reel[winnerSlot]]
	draw.Draw(dst, win, card, image.Point{}, draw.Src)

	if s.winner.Rarity == 5 && t >= shineStart && t <= shineEnd {
		// A soft diagonal band of light crossing the card.
		centre := -40 + (cellW+cellH+80)*(t-shineStart)/(shineEnd-shineStart)
		for y := range cellH {
			for x := range cellW {
				d := math.Abs(float64(x+y) - centre)
				if d < 22 {
					frames.Blend(dst.Pix[dst.PixOffset(win.Min.X+x, win.Min.Y+y):], white, 0.45*(1-d/22))
				}
			}
		}
	}
}

// drawMarker draws the centre line and its two pointers.
func (s *scene) drawMarker(dst *image.RGBA) {
	cx := Width / 2
	top, bottom := reelTop-bandPad, reelTop+cellH+bandPad
	draw.Draw(dst, image.Rect(cx-1, top, cx+1, bottom), image.NewUniform(markerColor), image.Point{}, draw.Over)
	for _, tip := range []struct{ base, point float64 }{{float64(top) - 2, float64(top) + 10}, {float64(bottom) + 2, float64(bottom) - 10}} {
		box := image.Rect(cx-10, int(math.Min(tip.base, tip.point))-1, cx+11, int(math.Max(tip.base, tip.point))+2)
		s.raster.Reset(box.Dx(), box.Dy())
		ox, oy := float32(box.Min.X), float32(box.Min.Y)
		s.raster.MoveTo(float32(cx)-9-ox, float32(tip.base)-oy)
		s.raster.LineTo(float32(cx)+9-ox, float32(tip.base)-oy)
		s.raster.LineTo(float32(cx)-ox, float32(tip.point)-oy)
		s.raster.ClosePath()
		s.raster.Draw(dst, box, image.NewUniform(markerColor), image.Point{})
	}
}

// drawResult slides the winner's name up under the reel and pops its stars.
func (s *scene) drawResult(dst *image.RGBA, t float64) {
	p := math.Max(0, math.Min(1, (t-resultIn)/(resultSettled-resultIn)))
	if p <= 0 {
		return
	}
	u := 1 - p
	ease := 1 - u*u*u
	top := float64(reelTop+cellH+bandPad) + 22 + (1-ease)*14
	s.result.Draw(dst, (Width-s.resultW)/2, top, ease)

	const starSize = 26.0
	rowWidth := float64(s.winner.Rarity)*starSize*1.15 - starSize*0.15
	cy := top + s.resultH + 8 + starSize/2
	for i := range s.winner.Rarity {
		appear := starsStart + float64(i)*starStep
		if t < appear {
			continue
		}
		q := (t - appear) / 0.2
		pop := 1.0
		if q < 0.5 {
			pop = 1.5 * q / 0.5
		} else if q < 1 {
			pop = 1.5 - 0.5*(q-0.5)/0.5
		}
		cx := (Width-rowWidth)/2 + float64(i)*starSize*1.15 + starSize/2
		frames.FillStar(s.raster, dst, cx, cy, starSize*pop, starColor, ease)
	}
}

// drawBackground paints the dark backdrop and the reel's band.
func drawBackground() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	for y := range Height {
		c := mix(backgroundTop, backgroundBottom, float64(y)/Height)
		draw.Draw(img, image.Rect(0, y, Width, y+1), image.NewUniform(c), image.Point{}, draw.Src)
	}
	// A soft spotlight on the middle of the stage.
	frames.DrawRadial(img, Width/2, reelTop+cellH/2, 0, Width*0.55, color.NRGBA{0x2a, 0x2f, 0x3c, 0xff}, 0.7)
	band := image.Rect(0, reelTop-bandPad, Width, reelTop+cellH+bandPad)
	draw.Draw(img, band, image.NewUniform(bandColor), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, band.Min.Y, Width, band.Min.Y+1), image.NewUniform(bandLine), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, band.Max.Y-1, Width, band.Max.Y), image.NewUniform(bandLine), image.Point{}, draw.Src)
	return img
}

// drawCard paints one item card: a dark tile washed with its rarity colour
// from the bottom, a rarity bar, the label's initial as its emblem, and the
// label underneath.
func drawCard(item Item, labelFace, iconFace font.Face) *image.RGBA {
	rarity := rarityColors[item.Rarity]
	card := image.NewRGBA(image.Rect(0, 0, cellW, cellH))
	for y := range cellH {
		c := mix(color.NRGBA{0x2c, 0x2f, 0x38, 0xff}, color.NRGBA{0x1e, 0x20, 0x27, 0xff}, float64(y)/cellH)
		if wash := (float64(y)/cellH - 0.45) / 0.55; wash > 0 {
			c = mix(c, rarity, 0.42*wash*wash)
		}
		draw.Draw(card, image.Rect(0, y, cellW, y+1), image.NewUniform(c), image.Point{}, draw.Src)
	}
	draw.Draw(card, image.Rect(0, cellH-5, cellW, cellH), image.NewUniform(rarity), image.Point{}, draw.Src)
	edge := color.NRGBA{0x3c, 0x40, 0x4b, 0xff}
	draw.Draw(card, image.Rect(0, 0, cellW, 1), image.NewUniform(edge), image.Point{}, draw.Src)
	draw.Draw(card, image.Rect(0, 0, 1, cellH-5), image.NewUniform(edge), image.Point{}, draw.Src)
	draw.Draw(card, image.Rect(cellW-1, 0, cellW, cellH-5), image.NewUniform(edge), image.Point{}, draw.Src)

	label := clean(item.Label)
	initial, _ := utf8.DecodeRuneInString(label)
	icon := string(unicode.ToUpper(initial))
	mask := image.NewAlpha(card.Bounds())
	drawCentered(mask, iconFace, icon, cellW/2, 58)
	frames.PaintMask(card, frames.BlurAlpha(mask, 6), image.Point{}, rarity)
	frames.PaintMask(card, mask, image.Point{}, mix(rarity, white, 0.55))

	text := image.NewAlpha(card.Bounds())
	for i, line := range frames.WrapText(labelFace, label, cellW-14, 2) {
		drawCentered(text, labelFace, line, cellW/2, 86+float64(i)*16)
	}
	frames.PaintMask(card, frames.BlurAlpha(text, 1.5), image.Point{Y: 1}, color.NRGBA{0, 0, 0, 220})
	frames.PaintMask(card, text, image.Point{}, color.NRGBA{0xee, 0xee, 0xf2, 0xff})
	return card
}

// drawGlow paints the rarity-coloured halo that rings the winning card.
func drawGlow(c color.NRGBA) (*image.RGBA, int) {
	const pad = 18
	mask := image.NewAlpha(image.Rect(0, 0, cellW+2*pad, cellH+2*pad))
	draw.Draw(mask, image.Rect(pad-3, pad-3, pad+cellW+3, pad+cellH+3), image.Opaque, image.Point{}, draw.Src)
	glow := image.NewRGBA(mask.Bounds())
	frames.PaintMask(glow, frames.BlurAlpha(mask, 6), image.Point{}, c)
	return glow, pad
}

// drawCentered draws s with its advance centred on cx and its baseline at y.
func drawCentered(dst *image.Alpha, face font.Face, s string, cx, y float64) {
	d := font.Drawer{Dst: dst, Src: image.Opaque, Face: face}
	d.Dot = fixed.Point26_6{X: fixed.Int26_6((cx - frames.TextWidth(face, s)/2) * 64), Y: fixed.Int26_6(y * 64)}
	d.DrawString(s)
}

// mix blends a toward b by t.
func mix(a, b color.NRGBA, t float64) color.NRGBA {
	lerp := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return color.NRGBA{lerp(a.R, b.R), lerp(a.G, b.G), lerp(a.B, b.B), 0xff}
}
