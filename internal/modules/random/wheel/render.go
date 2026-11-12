package wheel

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/vector"

	"github.com/tiennm99/tiennm99bot/internal/modules/random/frames"
)

// renderTimeout bounds one spin; it renders in a second or two.
const renderTimeout = 30 * time.Second

var (
	bulbGlow   = color.NRGBA{0xff, 0xc8, 0x70, 0xff}
	bulbCore   = color.NRGBA{0xff, 0xf8, 0xe8, 0xff}
	confettiOf = []color.NRGBA{
		{0xff, 0x4d, 0x6d, 0xff}, {0xff, 0xc7, 0x3c, 0xff}, {0x4c, 0xd9, 0x7b, 0xff},
		{0x4d, 0x9d, 0xff, 0xff}, {0xb3, 0x7b, 0xff, 0xff}, {0xff, 0xff, 0xff, 0xff},
	}
)

// Render returns the spin that lands on options[winner] as a silent H.264
// MP4, Size pixels square and DurationSeconds long. seed varies the launch
// and the confetti.
func Render(ctx context.Context, options []string, winner int, seed uint64) ([]byte, error) {
	if len(options) == 0 || len(options) > MaxOptions {
		return nil, fmt.Errorf("wheel: %d options, want 1 to %d", len(options), MaxOptions)
	}
	if winner < 0 || winner >= len(options) {
		return nil, fmt.Errorf("wheel: winner %d out of range", winner)
	}
	for _, option := range options {
		if strings.TrimSpace(option) == "" {
			return nil, errors.New("wheel: empty option")
		}
	}
	l, err := loadLayers()
	if err != nil {
		return nil, err
	}
	d, err := newDisc(options, l.peg)
	if err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x6a09e667f3bcc909))
	sc := &scene{layers: l, disc: d, spin: planSpin(len(options), winner, rng), winner: winner,
		raster: vector.NewRasterizer(1, 1)}
	sc.pixels = discPixels()
	sc.confetti = newConfetti(rng)

	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	encoder, err := frames.NewEncoder(ctx, Size, Size, FPS)
	if err != nil {
		return nil, err
	}
	frame := image.NewRGBA(image.Rect(0, 0, Size, Size))
	for index := range frameCount {
		sc.draw(frame, float64(index)/FPS)
		if err := encoder.WriteFrame(frame); err != nil {
			encoder.Abort()
			return nil, err
		}
	}
	return encoder.Finish()
}

type scene struct {
	*layers
	disc     *disc
	spin     spin
	winner   int
	pixels   []discPixel
	confetti []confetto
	raster   *vector.Rasterizer
}

// discPixel is a frame pixel over the disc, with its polar position.
type discPixel struct {
	offset int     // into the frame's Pix
	index  int     // into the shade and spec maps
	ring   int     // distance from the centre, rounded to whole pixels
	angle  float64 // clockwise from 12 o'clock, in angle samples
}

func discPixels() []discPixel {
	var out []discPixel
	for y := range Size {
		for x := range Size {
			dx, dy := float64(x)+0.5-centre, float64(y)+0.5-centre
			r := math.Hypot(dx, dy)
			if r > discRadius {
				continue
			}
			out = append(out, discPixel{
				offset: (y*Size + x) * 4,
				index:  y*Size + x,
				ring:   int(math.Round(r)),
				angle:  clockwiseAngle(dx, dy) / (2 * math.Pi) * angleSamples,
			})
		}
	}
	// Walking ring by ring reads the running sums in order.
	slices.SortFunc(out, func(a, b discPixel) int {
		return cmp.Or(cmp.Compare(a.ring, b.ring), cmp.Compare(a.angle, b.angle))
	})
	return out
}

func (s *scene) draw(dst *image.RGBA, t float64) {
	copy(dst.Pix, s.backdrop.Pix)
	s.drawDisc(dst, t)
	xdraw.Draw(dst, dst.Bounds(), s.front, image.Point{}, xdraw.Over)
	s.drawBulbs(dst, t)
	s.drawPointer(dst, t)
	s.drawConfetti(dst, t)
}

// drawDisc spins the disc to its simulated angle, blurred along the arc it
// swept while a half-open shutter was open, and lights it with the
// Blender-rendered light and clear-coat maps. Once it stops, the winning
// segment glows and the rest dim.
func (s *scene) drawDisc(dst *image.RGBA, t float64) {
	const toSamples = angleSamples / (2 * math.Pi)
	theta := at(s.spin.theta, t) * toSamples
	travel := theta - at(s.spin.theta, t-0.5/FPS)*toSamples
	width := math.Abs(travel)
	celebrate := math.Max(0, math.Min(1, (t-s.spin.stopped-0.15)/0.35))
	pulse := 0.5 + 0.5*math.Sin((t-s.spin.stopped)*7)
	segment := float64(angleSamples) / float64(s.disc.n)

	// Rings are split among the CPUs; each pixel is written by one worker.
	workers := runtime.GOMAXPROCS(0)
	chunk := (len(s.pixels) + workers - 1) / workers
	var wg sync.WaitGroup
	for start := 0; start < len(s.pixels); start += chunk {
		wg.Go(func() {
			s.drawPixels(dst, s.pixels[start:min(start+chunk, len(s.pixels))], theta, travel, width, celebrate, pulse, segment)
		})
	}
	wg.Wait()
}

func (s *scene) drawPixels(dst *image.RGBA, pixels []discPixel, theta, travel, width, celebrate, pulse, segment float64) {
	for _, p := range pixels {
		// The disc angle under this pixel now; earlier in the shutter it was
		// travel samples further on.
		beta := p.angle - theta
		hi := math.Max(beta, beta+travel)
		w := width
		if w < 1 {
			hi, w = beta+0.5, 1
		}
		rgb := s.disc.arc(p.ring, hi, w)
		gain := float32(s.shade[p.index])
		if celebrate > 0 {
			discAngle := beta - angleSamples*math.Floor(beta/angleSamples)
			if int(discAngle/segment) == s.winner {
				gain *= float32(1 + celebrate*(0.18+0.22*pulse))
			} else {
				gain *= float32(1 - 0.35*celebrate)
			}
		}
		px := dst.Pix[p.offset:]
		spec := s.spec[p.index]
		for ch := range 3 {
			px[ch] = uint8(math.Min(255, float64(rgb[ch]*gain+spec)))
		}
	}
}

// drawBulbs lights the rim: a chase while the wheel turns, all of them
// flashing once it stops.
func (s *scene) drawBulbs(dst *image.RGBA, t float64) {
	for i := range bulbCount {
		var level float64
		switch {
		case t < windupEnd:
			level = 0.55
		case t < s.spin.stopped:
			phase := (i + int(t*14)) % 4
			level = []float64{1, 0.35, 0.12, 0.12}[phase]
		default:
			if int((t-s.spin.stopped)*5)%2 == 0 {
				level = 1
			} else {
				level = 0.2
			}
		}
		a := 2 * math.Pi * float64(i) / bulbCount
		x, y := centre+bulbRadius*math.Sin(a), centre-bulbRadius*math.Cos(a)
		frames.DrawRadial(dst, x, y, 2, 15, bulbGlow, 0.75*level)
		frames.DrawRadial(dst, x, y, 1.5, 5, bulbCore, level)
	}
}

// drawPointer turns the pointer about its pivot by the flapper's simulated
// deflection; positive swings the tip clockwise, with the pegs.
func (s *scene) drawPointer(dst *image.RGBA, t float64) {
	gamma := -at(s.spin.psi, t)
	cos, sin := math.Cos(gamma), math.Sin(gamma)
	px, py := float64(centre), float64(pointerPivotY)
	ox, oy := float64(s.pointerAt.X)-px, float64(s.pointerAt.Y)-py
	m := f64.Aff3{
		cos, -sin, px + cos*ox - sin*oy,
		sin, cos, py + sin*ox + cos*oy,
	}
	xdraw.BiLinear.Transform(dst, m, s.pointer, s.pointer.Bounds(), xdraw.Over, nil)
}

// confetto is one piece of the celebration's confetti.
type confetto struct {
	x, y, vx, vy float64
	w, h         float64
	spin, flip   float64
	phase        float64
	colour       color.NRGBA
}

func newConfetti(rng *rand.Rand) []confetto {
	out := make([]confetto, 90)
	for i := range out {
		out[i] = confetto{
			x:      rng.Float64() * Size,
			y:      -10 - rng.Float64()*160,
			vx:     (rng.Float64() - 0.5) * 70,
			vy:     40 + rng.Float64()*90,
			w:      4 + rng.Float64()*3,
			h:      8 + rng.Float64()*5,
			spin:   (rng.Float64() - 0.5) * 12,
			flip:   4 + rng.Float64()*8,
			phase:  rng.Float64() * 2 * math.Pi,
			colour: confettiOf[rng.IntN(len(confettiOf))],
		}
	}
	return out
}

// drawConfetti drops the confetti once the wheel stops; each piece tumbles,
// showing its edge as it flips.
func (s *scene) drawConfetti(dst *image.RGBA, t float64) {
	elapsed := t - s.spin.stopped - 0.1
	if elapsed <= 0 {
		return
	}
	for _, c := range s.confetti {
		x := c.x + c.vx*elapsed + 10*math.Sin(elapsed*3+c.phase)
		y := c.y + c.vy*elapsed + 60*elapsed*elapsed
		if y < -20 || y > Size+20 {
			continue
		}
		angle := c.phase + c.spin*elapsed
		flip := math.Abs(math.Cos(c.phase + c.flip*elapsed))
		hw, hh := c.w/2*math.Max(0.15, flip), c.h/2
		cos, sin := math.Cos(angle), math.Sin(angle)
		corners := [4][2]float64{{-hw, -hh}, {hw, -hh}, {hw, hh}, {-hw, hh}}
		box := image.Rect(int(x-hh)-2, int(y-hh)-2, int(x+hh)+3, int(y+hh)+3).Intersect(dst.Bounds())
		if box.Empty() {
			continue
		}
		s.raster.Reset(box.Dx(), box.Dy())
		for i, p := range corners {
			cx := float32(x + p[0]*cos - p[1]*sin - float64(box.Min.X))
			cy := float32(y + p[0]*sin + p[1]*cos - float64(box.Min.Y))
			if i == 0 {
				s.raster.MoveTo(cx, cy)
			} else {
				s.raster.LineTo(cx, cy)
			}
		}
		s.raster.ClosePath()
		colour := c.colour
		// The face catches the light as it turns toward the viewer.
		shade := 0.65 + 0.35*flip
		colour.R, colour.G, colour.B = scale(colour.R, shade), scale(colour.G, shade), scale(colour.B, shade)
		s.raster.Draw(dst, box, image.NewUniform(colour), image.Point{})
	}
}
