// Package wheel renders the /wheelofnames spin in process. Blender supplies
// the static layers (stage, rim, hub, pointer, peg, and the disc's light and
// clear-coat maps; see scripts/blender/wheel-layers.py); Go draws the segments and
// labels, spins them with rotational motion blur, lights them with those
// maps, and animates the bulbs, the pointer flicking off the pegs, and the
// winner's celebration. ffmpeg encodes the MP4.
package wheel

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"sync"
)

// Output size and timing.
const (
	Size            = 512
	FPS             = 30
	DurationSeconds = 7
	frameCount      = FPS * DurationSeconds

	// MaxOptions is the most segments the wheel draws legibly.
	MaxOptions = 32
)

// Geometry in output pixels. It must match scripts/blender/wheel-layers.py
// (one Blender unit is 100 pixels, centred in the frame).
const (
	centre        = Size / 2
	discRadius    = 200
	hubRadius     = 30
	pegRadius     = 188 // pegs sit on the segment boundaries at this radius
	pegClearance  = 10  // peg radius plus half the pointer tip's width
	pointerPivotY = centre - 230
	pointerTipY   = centre - 184
	bulbCount     = 24
	bulbRadius    = 209
)

// Seconds from the start of the clip.
const (
	windupEnd = 0.3 // the wheel is pulled back a little, then released
	spinEnd   = 5.7
)

// palette colours the segments in turn.
var palette = []color.NRGBA{
	{0xe6, 0x39, 0x46, 0xff}, // red
	{0xf4, 0xa2, 0x1c, 0xff}, // amber
	{0x2a, 0x9d, 0x5c, 0xff}, // green
	{0x1d, 0x6f, 0xd8, 0xff}, // blue
	{0x8e, 0x44, 0xc9, 0xff}, // purple
	{0xf0, 0x6c, 0x9b, 0xff}, // pink
	{0x16, 0xa5, 0xa5, 0xff}, // teal
	{0xf2, 0x7a, 0x1a, 0xff}, // orange
}

//go:embed assets/*.png
var assetFS embed.FS

// layers are the decoded Blender renders.
type layers struct {
	backdrop  *image.RGBA
	front     *image.RGBA
	pointer   *image.RGBA // cropped to the pointer; pointerAt is its offset
	pointerAt image.Point
	peg       *image.RGBA
	shade     []float32 // diffuse light per pixel, 1 = fully lit white
	spec      []float32 // clear-coat highlight per pixel, 0 to 255
}

var loadLayers = sync.OnceValues(func() (*layers, error) {
	l := &layers{}
	var err error
	if l.backdrop, err = decode("backdrop.png"); err != nil {
		return nil, err
	}
	if l.front, err = decode("front.png"); err != nil {
		return nil, err
	}
	if l.peg, err = decode("peg.png"); err != nil {
		return nil, err
	}
	pointer, err := decode("pointer.png")
	if err != nil {
		return nil, err
	}
	l.pointer, l.pointerAt = crop(pointer)
	shade, err := decode("shade.png")
	if err != nil {
		return nil, err
	}
	spec, err := decode("spec.png")
	if err != nil {
		return nil, err
	}
	l.shade = make([]float32, Size*Size)
	l.spec = make([]float32, Size*Size)
	for i := range l.shade {
		// A white disc in full light renders near 180; that maps to 1.
		l.shade[i] = float32(shade.Pix[i*4]) / 180
		// The clear coat reflects a dim stage everywhere; only the
		// highlight above that floor is added.
		l.spec[i] = float32(math.Max(0, float64(spec.Pix[i*4])-58)) * 0.9
	}
	return l, nil
})

func decode(name string) (*image.RGBA, error) {
	data, err := assetFS.ReadFile("assets/" + name)
	if err != nil {
		return nil, fmt.Errorf("wheel: %s: %w", name, err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("wheel: decode %s: %w", name, err)
	}
	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	return rgba, nil
}

// crop trims the transparent border, returning the opaque part and its
// position in the original.
func crop(img *image.RGBA) (*image.RGBA, image.Point) {
	box := image.Rectangle{}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.Pix[img.PixOffset(x, y)+3] != 0 {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	box = box.Inset(-2).Intersect(b)
	out := image.NewRGBA(image.Rect(0, 0, box.Dx(), box.Dy()))
	draw.Draw(out, out.Bounds(), img, box.Min, draw.Src)
	return out, box.Min
}
