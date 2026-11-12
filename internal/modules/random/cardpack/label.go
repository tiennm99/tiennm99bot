package cardpack

import (
	"image"
	"image/color"
	"math"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/tiennm99/tiennm99bot/internal/modules/random/frames"
)

// The label is painted once into a texture the shape of the card's label box
// (2 by 0.56 card units), about twice the size it covers on screen, then
// warped onto the card every frame.
const (
	texW = 600
	texH = 168
	// texPad leaves room around the box for the glow and shadow.
	texPad = 24
)

// labelTexture is the painted label: premultiplied colour (glow, shadow and
// metallic glyphs) and the glyphs' own coverage, which the shine sweep
// lights.
type labelTexture struct {
	color  *image.RGBA
	glyphs *image.Alpha
}

// labelSizes are the font sizes tried for a one-line label, as fractions of
// the box height; longer labels wrap onto two lines at twoLineSize.
var labelSizes = []float64{0.6, 0.52, 0.45}

const (
	twoLineSize   = 0.38
	twoLineHeight = 1.12
	// labelWidth is how much of the box width the text may fill.
	labelWidth = 0.92
)

func paintLabel(label string, look look) (*labelTexture, error) {
	width := texW * labelWidth
	var (
		face  font.Face
		lines []string
		size  float64
		err   error
	)
	for _, fraction := range labelSizes {
		size = texH * fraction
		if face, err = frames.Face(size); err != nil {
			return nil, err
		}
		if frames.TextWidth(face, label) <= width {
			lines = []string{label}
			break
		}
		_ = face.Close()
		face = nil
	}
	if face == nil {
		size = texH * twoLineSize
		if face, err = frames.Face(size); err != nil {
			return nil, err
		}
		lines = frames.WrapText(face, label, width, 2)
	}
	defer func() { _ = face.Close() }()

	bounds := image.Rect(0, 0, texW+2*texPad, texH+2*texPad)
	glyphs := image.NewAlpha(bounds)
	metrics := face.Metrics()
	ascent, descent := float64(metrics.Ascent)/64, float64(metrics.Descent)/64
	lineHeight := size * twoLineHeight
	block := lineHeight*float64(len(lines)-1) + ascent + descent
	baseline := texPad + (texH-block)/2 + ascent
	drawer := font.Drawer{Dst: glyphs, Src: image.Opaque, Face: face}
	for i, line := range lines {
		x := texPad + (texW-frames.TextWidth(face, line))/2
		drawer.Dot = fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6((baseline + float64(i)*lineHeight) * 64)}
		drawer.DrawString(line)
	}

	tex := image.NewRGBA(bounds)
	glow := look.glow
	glow.A = 150
	frames.PaintMask(tex, frames.BlurAlpha(glyphs, texH*0.07), image.Point{}, glow)
	frames.PaintMask(tex, frames.BlurAlpha(glyphs, texH*0.025), image.Point{Y: 3}, color.NRGBA{0, 0, 0, 230})
	// Metallic glyphs: light at the top of each line, the rarity metal below.
	fill := image.NewRGBA(bounds)
	for y := range bounds.Dy() {
		line := math.Mod(float64(y)-(baseline-ascent), lineHeight) / (ascent + descent)
		t := math.Max(0, math.Min(1, line))
		c := mixNRGBA(look.textTop, look.textBottom, t)
		for x := range bounds.Dx() {
			fill.SetRGBA(x, y, color.RGBA{c.R, c.G, c.B, 0xff})
		}
	}
	draw.DrawMask(tex, bounds, fill, image.Point{}, glyphs, image.Point{}, draw.Over)
	return &labelTexture{color: tex, glyphs: glyphs}, nil
}

func mixNRGBA(a, b color.NRGBA, t float64) color.NRGBA {
	mix := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return color.NRGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 0xff}
}

// drawLabel warps the label onto the card for frame f.
func (o *overlay) drawLabel(dst *image.RGBA, f trackFrame) {
	opacity := f.Opacity * smoothstep(0.2, 0.6, f.Facing)
	if opacity <= 0 {
		return
	}
	toScreen := squareToQuad(f.Label)
	toBox, ok := toScreen.inverse()
	if !ok {
		return
	}
	// Screen bounds of the padded texture.
	padU, padV := float64(texPad)/texW, float64(texPad)/texH
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, corner := range [][2]float64{{-padU, -padV}, {1 + padU, -padV}, {1 + padU, 1 + padV}, {-padU, 1 + padV}} {
		x, y := toScreen.apply(corner[0], corner[1])
		minX, minY, maxX, maxY = math.Min(minX, x), math.Min(minY, y), math.Max(maxX, x), math.Max(maxY, y)
	}
	area := image.Rect(int(minX)-1, int(minY)-1, int(maxX)+2, int(maxY)+2).Intersect(dst.Bounds())

	light := 0.72 + 0.28*f.Facing
	uv := o.track.LabelUV
	tex := o.label
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			// Four samples per pixel: the texture is about twice the size it
			// covers, and one bilinear tap would alias.
			var r, g, b, a, glyph float64
			var u, v float64
			for _, offset := range [4][2]float64{{0.25, 0.25}, {0.75, 0.25}, {0.25, 0.75}, {0.75, 0.75}} {
				u, v = toBox.apply(float64(x)+offset[0], float64(y)+offset[1])
				tx, ty := texPad+u*texW, texPad+v*texH
				sr, sg, sb, sa, sGlyph := tex.sample(tx, ty)
				r, g, b, a, glyph = r+sr, g+sg, b+sb, a+sa, glyph+sGlyph
			}
			if a <= 0 {
				continue
			}
			r, g, b, a, glyph = r/4, g/4, b/4, a/4, glyph/4
			// The shine band crossing the face lights the glyphs too.
			cu := uv[0] + u*(uv[2]-uv[0])
			cv := uv[1] + v*(uv[3]-uv[1])
			shine := smoothstep(0.09, 0, math.Abs(cu+0.45*cv-f.Sweep)) * glyph * 0.85
			px := dst.Pix[dst.PixOffset(x, y):]
			for i, c := range [3]float64{r, g, b} {
				lit := math.Min(255, c*light+shine*255)
				px[i] = uint8(math.Min(255, float64(px[i])*(1-a*opacity)+lit*opacity) + 0.5)
			}
		}
	}
}

// sample reads the texture bilinearly at (x, y), in texture pixels:
// premultiplied colour, coverage, and glyph coverage, all 0 to 255 except
// the coverages, 0 to 1.
func (t *labelTexture) sample(x, y float64) (r, g, b, a, glyph float64) {
	x, y = x-0.5, y-0.5
	x0, y0 := math.Floor(x), math.Floor(y)
	fx, fy := x-x0, y-y0
	ix, iy := int(x0), int(y0)
	bounds := t.color.Bounds()
	for _, tap := range [4]struct {
		dx, dy int
		w      float64
	}{{0, 0, (1 - fx) * (1 - fy)}, {1, 0, fx * (1 - fy)}, {0, 1, (1 - fx) * fy}, {1, 1, fx * fy}} {
		px, py := ix+tap.dx, iy+tap.dy
		if tap.w == 0 || px < bounds.Min.X || py < bounds.Min.Y || px >= bounds.Max.X || py >= bounds.Max.Y {
			continue
		}
		c := t.color.Pix[t.color.PixOffset(px, py):]
		r += float64(c[0]) * tap.w
		g += float64(c[1]) * tap.w
		b += float64(c[2]) * tap.w
		a += float64(c[3]) / 255 * tap.w
		glyph += float64(t.glyphs.Pix[t.glyphs.PixOffset(px, py)]) / 255 * tap.w
	}
	return r, g, b, a, glyph
}

func smoothstep(edge0, edge1, x float64) float64 {
	t := math.Max(0, math.Min(1, (x-edge0)/(edge1-edge0)))
	return t * t * (3 - 2*t)
}
