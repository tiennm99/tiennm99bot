package cardpack

import (
	"image"
	"image/color"
	"math"
	"runtime"
	"sync"
)

// flashCutoff is the strength below which a decaying flash is dropped: by
// then it moves a pixel by about two levels at most, which no one can see.
const flashCutoff = 0.02

// glow is a soft disc of light whose brightness falls off as (1 - d)^2 to
// nothing at its radius.
type glow struct {
	cx, cy, radius float64
	// rgb is the light's colour times its strength, each 0 to 1.
	rgb [3]float64
}

func newGlow(cx, cy, radius float64, c color.NRGBA, strength float64) glow {
	s := math.Min(1, strength) / 255
	return glow{cx, cy, radius, [3]float64{float64(c.R) * s, float64(c.G) * s, float64(c.B) * s}}
}

// falloff holds (1 - d)^2 indexed by d^2, so the per-pixel work needs no
// square root.
var falloff = func() [4097]float64 {
	var table [4097]float64
	for i := range table {
		d := math.Sqrt(float64(i) / 4096)
		table[i] = (1 - d) * (1 - d)
	}
	return table
}()

// screenLights screens the glows, and wash over the whole frame if its
// strength is above zero, onto dst in one pass. Screening is order-free, so
// this equals applying each light in turn: a pixel v becomes
// 1 - (1 - v) * product(1 - light). Rows are split across the CPUs.
func screenLights(dst *image.RGBA, glows []glow, wash color.NRGBA, washStrength float64) {
	area := image.Rectangle{}
	for _, g := range glows {
		area = area.Union(image.Rect(int(g.cx-g.radius), int(g.cy-g.radius), int(g.cx+g.radius)+1, int(g.cy+g.radius)+1))
	}
	var washKeep [3]float64
	if washStrength > 0 {
		area = dst.Bounds()
		w := newGlow(0, 0, 0, wash, washStrength)
		for i := range washKeep {
			washKeep[i] = 1 - w.rgb[i]
		}
	}
	area = area.Intersect(dst.Bounds())
	if area.Empty() {
		return
	}
	parallelRows(area, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			py := float64(y) + 0.5
			row := dst.Pix[dst.PixOffset(area.Min.X, y):]
			for x := area.Min.X; x < area.Max.X; x++ {
				keep := [3]float64{1, 1, 1}
				if washStrength > 0 {
					keep = washKeep
				}
				lit := washStrength > 0
				px := float64(x) + 0.5
				for i := range glows {
					g := &glows[i]
					dx, dy := (px-g.cx)/g.radius, (py-g.cy)/g.radius
					d2 := dx*dx + dy*dy
					if d2 >= 1 {
						continue
					}
					f := falloff[int(d2*4096)]
					keep[0] *= 1 - g.rgb[0]*f
					keep[1] *= 1 - g.rgb[1]*f
					keep[2] *= 1 - g.rgb[2]*f
					lit = true
				}
				if lit {
					p := row[(x-area.Min.X)*4:]
					for c := range 3 {
						p[c] = uint8(255 - (255-float64(p[c]))*keep[c] + 0.5)
					}
				}
			}
		}
	})
}

// parallelRows runs fn over bands of area's rows, one band per CPU.
func parallelRows(area image.Rectangle, fn func(y0, y1 int)) {
	workers := min(runtime.NumCPU(), area.Dy())
	if workers <= 1 {
		fn(area.Min.Y, area.Max.Y)
		return
	}
	band := (area.Dy() + workers - 1) / workers
	var wg sync.WaitGroup
	for y := area.Min.Y; y < area.Max.Y; y += band {
		wg.Go(func() { fn(y, min(y+band, area.Max.Y)) })
	}
	wg.Wait()
}
