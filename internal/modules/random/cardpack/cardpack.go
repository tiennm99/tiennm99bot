// Package cardpack renders the /gacha card-pack wish in process. A clip per
// rarity, pre-rendered in Blender by scripts/blender/gacha-pack.py, plays the
// whole opening: a foil pack turns once and is torn down its right side,
// light and sparks pour out, the pack slides off the card, and the card spins
// toward the camera and lands face up with its rank letter and stars. Go draws the label onto the moving card, following the
// card corners the script tracked for every frame, and adds the flashes and
// glints on top. ffmpeg decodes the clip and encodes the result.
package cardpack

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/tiennm99/tiennm99bot/internal/modules/random/frames"
)

// Output frame size and timing. They must match the clips and tracks.
const (
	Width           = 540
	Height          = 960
	FPS             = 30
	DurationSeconds = 7
	frameCount      = FPS * DurationSeconds
)

// renderTimeout bounds one wish; a render takes a second or two.
const renderTimeout = 30 * time.Second

// assets holds a clip and its track per rarity. The clips are encoded with
// +faststart so ffmpeg can read them from a pipe.
//
//go:embed assets/pack-3.mp4 assets/pack-4.mp4 assets/pack-5.mp4
//go:embed assets/track-3.json assets/track-4.json assets/track-5.json
var assets embed.FS

// look is a rarity's colours and how loud its effects are.
type look struct {
	glow, core          color.NRGBA
	textTop, textBottom color.NRGBA
	flash               float64
	glints              int
}

var looks = map[int]look{
	3: {
		glow: color.NRGBA{0x40, 0x8c, 0xff, 0xff}, core: color.NRGBA{0xd9, 0xed, 0xff, 0xff},
		textTop: color.NRGBA{0xff, 0xff, 0xff, 0xff}, textBottom: color.NRGBA{0xa8, 0xc8, 0xff, 0xff},
		flash: 0.35, glints: 6,
	},
	4: {
		glow: color.NRGBA{0xa0, 0x50, 0xff, 0xff}, core: color.NRGBA{0xf2, 0xdc, 0xff, 0xff},
		textTop: color.NRGBA{0xff, 0xff, 0xff, 0xff}, textBottom: color.NRGBA{0xdc, 0xb8, 0xff, 0xff},
		flash: 0.5, glints: 10,
	},
	5: {
		glow: color.NRGBA{0xff, 0x9c, 0x24, 0xff}, core: color.NRGBA{0xff, 0xf2, 0xcc, 0xff},
		textTop: color.NRGBA{0xff, 0xfa, 0xe6, 0xff}, textBottom: color.NRGBA{0xff, 0xc4, 0x4a, 0xff},
		flash: 0.7, glints: 16,
	},
}

// Render returns the wish revealing label at rarity stars (3 to 5) as a
// silent H.264 MP4 of Width x Height, DurationSeconds long. seed places the
// glints, so two rolls of the same label differ a little.
func Render(ctx context.Context, label string, rarity int, seed uint64) ([]byte, error) {
	label = norm.NFC.String(strings.TrimSpace(label))
	if label == "" {
		return nil, errors.New("cardpack: label empty")
	}
	lk, ok := looks[rarity]
	if !ok {
		return nil, fmt.Errorf("cardpack: rarity %d out of range", rarity)
	}
	t, err := tracks[rarity]()
	if err != nil {
		return nil, err
	}
	clip, err := assets.ReadFile(fmt.Sprintf("assets/pack-%d.mp4", rarity))
	if err != nil {
		return nil, fmt.Errorf("cardpack: clip %d: %w", rarity, err)
	}
	tex, err := paintLabel(label, lk)
	if err != nil {
		return nil, err
	}
	o := &overlay{look: lk, track: t, label: tex, glints: placeGlints(t, lk, seed)}

	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	encoder, err := frames.NewEncoder(ctx, Width, Height, FPS)
	if err != nil {
		return nil, err
	}
	src, err := frames.OpenClip(ctx, clip, Width, Height, FPS)
	if err != nil {
		encoder.Abort()
		return nil, err
	}
	pipeErr := o.pipe(src, encoder)
	if err := src.Close(); err != nil && pipeErr == nil {
		pipeErr = fmt.Errorf("cardpack: %w", err)
	}
	if pipeErr != nil {
		encoder.Abort()
		return nil, pipeErr
	}
	return encoder.Finish()
}

// overlay draws everything that goes over the clip for one wish.
type overlay struct {
	look   look
	track  *track
	label  *labelTexture
	glints []glint
}

func (o *overlay) pipe(clip *frames.Clip, encoder *frames.Encoder) error {
	frame := image.NewRGBA(image.Rect(0, 0, Width, Height))
	for index := range frameCount {
		if err := clip.Next(frame); err != nil {
			return fmt.Errorf("cardpack: %w", err)
		}
		o.compose(frame, index)
		if err := encoder.WriteFrame(frame); err != nil {
			return err
		}
	}
	return nil
}

// compose draws frame index's overlay onto the clip frame in place.
func (o *overlay) compose(dst *image.RGBA, index int) {
	t := float64(index) / FPS
	f := o.track.Frames[index]
	o.drawLabel(dst, f)
	o.drawGlints(dst, f, t)
	o.drawFlashes(dst, f, t)
}

// drawFlashes adds the burst of light where the pack tears and the bigger
// one as the card lands, in a single pass over the frame.
func (o *overlay) drawFlashes(dst *image.RGBA, f trackFrame, t float64) {
	var glows []glow
	wash := 0.0
	if a := pulse(t, o.track.Tear+0.03, 0.04, 0.35) * o.look.flash * 0.8; a > flashCutoff {
		x, y := o.track.TearAt[0], o.track.TearAt[1]
		glows = append(glows, newGlow(x, y, Width*0.75, o.look.glow, a), newGlow(x, y, Width*0.3, o.look.core, a))
	}
	if a := pulse(t, o.track.Land, 0.03, 0.45) * o.look.flash; a > flashCutoff {
		x, y := f.Card.centre()
		glows = append(glows, newGlow(x, y, Height*0.9, o.look.glow, a*0.8), newGlow(x, y, Width*0.55, o.look.core, a))
		wash = a * 0.45
	}
	if len(glows) > 0 {
		screenLights(dst, glows, color.NRGBA{0xff, 0xff, 0xff, 0xff}, wash)
	}
}

// pulse rises linearly over rise seconds from start and decays
// exponentially with time constant decay.
func pulse(t, start, rise, decay float64) float64 {
	switch {
	case t < start:
		return 0
	case t < start+rise:
		return (t - start) / rise
	default:
		return math.Exp(-(t - start - rise) / decay)
	}
}

// glint is a star of light that flares on the landed card.
type glint struct {
	u, v  float64 // where on the card, in UVs with v running down
	start float64
	life  float64
	size  float64
	angle float64
}

// placeGlints scatters the glints over the card, mostly along its frame,
// across the seconds after the landing.
func placeGlints(t *track, lk look, seed uint64) []glint {
	rng := rand.New(rand.NewPCG(seed, 0x6c616e64))
	out := make([]glint, lk.glints)
	const first, jitter, longest = 0.35, 0.25, 0.8
	window := float64(DurationSeconds) - t.Land - first - jitter - longest
	for i := range out {
		g := glint{
			start: t.Land + first + window*float64(i)/float64(len(out)) + rng.Float64()*jitter,
			life:  0.45 + rng.Float64()*(longest-0.45),
			size:  (0.05 + rng.Float64()*0.05) * Width,
			angle: (rng.Float64() - 0.5) * 0.5,
		}
		if rng.Float64() < 0.7 {
			// On the frame: pick an edge, then a point along it.
			along, edge := 0.06+rng.Float64()*0.88, rng.IntN(4)
			inset := 0.045
			switch edge {
			case 0:
				g.u, g.v = along, inset*0.7
			case 1:
				g.u, g.v = 1-inset, along
			case 2:
				g.u, g.v = along, 1-inset*0.7
			default:
				g.u, g.v = inset, along
			}
		} else {
			g.u, g.v = 0.15+rng.Float64()*0.7, 0.1+rng.Float64()*0.8
		}
		out[i] = g
	}
	return out
}

func (o *overlay) drawGlints(dst *image.RGBA, f trackFrame, t float64) {
	if f.Facing < 0.3 {
		return
	}
	toScreen := squareToQuad(f.Card)
	for _, g := range o.glints {
		p := (t - g.start) / g.life
		if p <= 0 || p >= 1 {
			continue
		}
		strength := math.Sin(p * math.Pi)
		x, y := toScreen.apply(g.u, g.v)
		drawStarGlint(dst, x, y, g.size*(0.4+0.6*strength), g.angle+p*0.6, o.look.core, strength)
	}
}

// drawStarGlint adds a four-pointed glint: a hot core and two crossed,
// tapering streaks.
func drawStarGlint(dst *image.RGBA, cx, cy, size, angle float64, c color.NRGBA, strength float64) {
	screenLights(dst, []glow{newGlow(cx, cy, size*0.35, c, strength*0.9)}, c, 0)
	cos, sin := math.Cos(angle), math.Sin(angle)
	box := image.Rect(int(cx-size), int(cy-size), int(cx+size)+1, int(cy+size)+1).Intersect(dst.Bounds())
	width := math.Max(0.8, size*0.035)
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			along, across := dx*cos+dy*sin, -dx*sin+dy*cos
			a := streak(along, across, size, width) + streak(across, along, size*0.75, width)
			if a > 0.004 {
				addLight(dst.Pix[dst.PixOffset(x, y):], c, a*strength)
			}
		}
	}
}

// streak is a line of light along the first axis, length out from the
// centre each way, tapering to its tips.
func streak(along, across, length, width float64) float64 {
	taper := 1 - math.Abs(along)/length
	if taper <= 0 {
		return 0
	}
	return taper * taper * math.Exp(-(across*across)/(2*width*width*(0.4+taper)))
}

// addLight screens c over an opaque pixel at alpha: bright light brightens
// without clipping hard to white.
func addLight(px []uint8, c color.NRGBA, alpha float64) {
	a := math.Min(1, alpha)
	for i, channel := range [3]uint8{c.R, c.G, c.B} {
		v := float64(px[i]) / 255
		l := float64(channel) / 255 * a
		px[i] = uint8((1-(1-v)*(1-l))*255 + 0.5)
	}
}
