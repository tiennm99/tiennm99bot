package wheel

import (
	"image"
	"image/color"
	"math"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
	"golang.org/x/text/unicode/norm"

	"github.com/tiennm99/tiennm99bot/internal/modules/random/frames"
)

// The disc is stored in polar form, angleSamples around by discRadius+1
// rings, as running sums along each ring: rotating the wheel is then a shift
// in angle, and blurring it along its motion is a difference of two sums.
const angleSamples = 2048

// Label track: text runs outward from the hub and ends short of the pegs.
const (
	labelStart = hubRadius + 16
	labelEnd   = pegRadius - 14
	labelWidth = labelEnd - labelStart
)

var (
	separatorColor = color.NRGBA{0xf3, 0xd2, 0x8a, 0xff}
	separatorDark  = color.NRGBA{0x5a, 0x3a, 0x10, 0xff}
)

// disc holds the polar running sums of the painted disc.
type disc struct {
	n    int
	sums []float32 // per ring: angleSamples+1 entries of (r, g, b)
}

// segmentColours picks each segment's colour, never repeating a colour next
// to itself, including across the seam between the last and first segment.
func segmentColours(n int) []color.NRGBA {
	out := make([]color.NRGBA, n)
	for i := range out {
		out[i] = palette[i%len(palette)]
	}
	if last := n - 1; n > 2 && out[last] == out[0] {
		for _, c := range palette {
			if c != out[0] && c != out[last-1] {
				out[last] = c
				break
			}
		}
	}
	return out
}

// newDisc paints the segments, separators, labels, and pegs, then converts
// them to polar running sums.
func newDisc(options []string, peg *image.RGBA) (*disc, error) {
	n := len(options)
	size := 2*discRadius + 2
	c := float64(size) / 2
	flat := image.NewRGBA(image.Rect(0, 0, size, size))
	colours := segmentColours(n)
	step := 2 * math.Pi / float64(n)

	for y := range size {
		for x := range size {
			dx, dy := float64(x)+0.5-c, float64(y)+0.5-c
			r := math.Hypot(dx, dy)
			if r > discRadius+1 {
				continue
			}
			angle := clockwiseAngle(dx, dy)
			segment := min(int(angle/step), n-1)
			px := flat.Pix[flat.PixOffset(x, y):]
			base := colours[segment]
			// Slightly darker toward the hub, as a printed wheel's ink sits.
			tone := 0.86 + 0.14*r/discRadius
			px[0], px[1], px[2], px[3] = scale(base.R, tone), scale(base.G, tone), scale(base.B, tone), 0xff
			if n > 1 {
				// Embossed gold separators on the boundaries.
				offset := math.Mod(angle, step)
				gap := math.Min(offset, step-offset) * r
				if gap < 2.4 {
					frames.Blend(px, separatorColor, math.Min(1, 2.4-gap))
				} else if offset < step/2 && gap < 4 {
					frames.Blend(px, separatorDark, 0.35*(4-gap)/1.6)
				}
			}
			// A dark bevel where the disc meets the rim.
			if edge := discRadius - r; edge < 5 {
				frames.Blend(px, color.NRGBA{0, 0, 0, 0xff}, 0.45*(1-math.Max(0, edge)/5))
			}
		}
	}

	if err := drawLabels(flat, options, c); err != nil {
		return nil, err
	}
	for k := range n {
		angle := float64(k) * step
		at := image.Pt(int(math.Round(c+pegRadius*math.Sin(angle)))-peg.Bounds().Dx()/2,
			int(math.Round(c-pegRadius*math.Cos(angle)))-peg.Bounds().Dy()/2)
		xdraw.Draw(flat, peg.Bounds().Add(at), peg, image.Point{}, xdraw.Over)
	}

	d := &disc{n: n, sums: make([]float32, (discRadius+1)*(angleSamples+1)*3)}
	for ring := 0; ring <= discRadius; ring++ {
		row := d.sums[ring*(angleSamples+1)*3:]
		var acc [3]float32
		for j := range angleSamples {
			a := 2 * math.Pi * (float64(j) + 0.5) / angleSamples
			rgb := bilinear(flat, c+float64(ring)*math.Sin(a), c-float64(ring)*math.Cos(a))
			for ch := range 3 {
				acc[ch] += rgb[ch]
				row[(j+1)*3+ch] = acc[ch]
			}
		}
	}
	return d, nil
}

// clockwiseAngle is the angle of (dx, dy) clockwise from 12 o'clock, in
// [0, 2π), with y pointing down.
func clockwiseAngle(dx, dy float64) float64 {
	a := math.Atan2(dx, -dy)
	if a < 0 {
		a += 2 * math.Pi
	}
	return a
}

func scale(v uint8, f float64) uint8 { return uint8(math.Min(255, float64(v)*f+0.5)) }

// bilinear samples img's RGB at (x, y) in pixel-centre coordinates.
func bilinear(img *image.RGBA, x, y float64) [3]float32 {
	x, y = x-0.5, y-0.5
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := float32(x-float64(x0)), float32(y-float64(y0))
	var out [3]float32
	b := img.Bounds()
	for _, corner := range [4]struct {
		dx, dy int
		w      float32
	}{{0, 0, (1 - fx) * (1 - fy)}, {1, 0, fx * (1 - fy)}, {0, 1, (1 - fx) * fy}, {1, 1, fx * fy}} {
		px, py := min(max(x0+corner.dx, b.Min.X), b.Max.X-1), min(max(y0+corner.dy, b.Min.Y), b.Max.Y-1)
		p := img.Pix[img.PixOffset(px, py):]
		for ch := range 3 {
			out[ch] += float32(p[ch]) * corner.w
		}
	}
	return out
}

// drawLabels writes each option along its segment's centre line, reading
// outward from the hub and ending short of the pegs.
func drawLabels(flat *image.RGBA, options []string, c float64) error {
	n := len(options)
	step := 2 * math.Pi / float64(n)
	for i, option := range options {
		label := norm.NFC.String(strings.TrimSpace(option))
		lines, size, err := fitLabel(label, n)
		if err != nil {
			return err
		}
		face, err := frames.Face(size)
		if err != nil {
			return err
		}
		lineHeight := size * 1.1
		blockWidth := 0.0
		for _, line := range lines {
			blockWidth = math.Max(blockWidth, frames.TextWidth(face, line))
		}
		layer := frames.RenderTextLayer(face, lines, blockWidth, lineHeight, true, color.NRGBA{0, 0, 0, 150}, size*0.12, size*0.08)
		_ = face.Close()

		angle := (float64(i) + 0.5) * step
		along := [2]float64{math.Sin(angle), -math.Cos(angle)} // outward
		down := [2]float64{math.Cos(angle), math.Sin(angle)}   // the text's "down"
		pad := float64(layer.Pad)
		start := labelEnd - blockWidth
		mid := pad + lineHeight*float64(len(lines))/2
		m := f64.Aff3{
			along[0], down[0], c + along[0]*(start-pad) - down[0]*mid,
			along[1], down[1], c + along[1]*(start-pad) - down[1]*mid,
		}
		xdraw.BiLinear.Transform(flat, m, layer.Image, layer.Image.Bounds(), xdraw.Over, nil)
	}
	return nil
}

// fitLabel picks the largest font size, and up to as many lines as the
// segment is tall, at which the label fits the track; below the smallest
// size it is cut short with an ellipsis.
func fitLabel(label string, n int) ([]string, float64, error) {
	base := math.Max(11, math.Min(26, discRadius/math.Max(5.8, float64(n)*0.33)))
	midHeight := 2 * math.Pi * (labelStart + labelWidth/2) / float64(n)
	if n == 1 {
		midHeight = discRadius
	}
	var lines []string
	size := base
	for ; size >= 9; size-- {
		face, err := frames.Face(size)
		if err != nil {
			return nil, 0, err
		}
		maxLines := max(1, min(3, int(midHeight*0.8/(size*1.1))))
		lines = frames.WrapText(face, label, labelWidth, maxLines)
		_ = face.Close()
		if !strings.HasSuffix(lines[len(lines)-1], frames.Ellipsis) || strings.HasSuffix(label, frames.Ellipsis) {
			return lines, size, nil
		}
	}
	return lines, size + 1, nil
}

// arc returns the disc's colour on a ring, averaged over the arc from
// angle-width to angle, in angle samples. width is at least one sample,
// which also smooths the stationary disc.
func (d *disc) arc(ring int, angle, width float64) [3]float32 {
	row := d.sums[ring*(angleSamples+1)*3:]
	// Keep the sums small, so float32 keeps the difference exact enough.
	angle -= angleSamples * math.Floor(angle/angleSamples)
	hi := d.cumulative(row, angle)
	lo := d.cumulative(row, angle-width)
	w := float32(width)
	return [3]float32{(hi[0] - lo[0]) / w, (hi[1] - lo[1]) / w, (hi[2] - lo[2]) / w}
}

// cumulative is the running sum up to x samples, continuing past a full
// turn in either direction, interpolated between samples.
func (d *disc) cumulative(row []float32, x float64) [3]float32 {
	turns := math.Floor(x / angleSamples)
	x -= turns * angleSamples
	i := int(x)
	f := float32(x - float64(i))
	total := row[angleSamples*3:]
	var out [3]float32
	for ch := range 3 {
		v := row[i*3+ch]
		if i < angleSamples {
			v += (row[(i+1)*3+ch] - v) * f
		}
		out[ch] = v + float32(turns)*total[ch]
	}
	return out
}
