package sticker

import (
	"bytes"
	"image"
	"image/png"

	// Registered for their side effect: image.Decode needs the formats
	// Telegram actually delivers.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // read-only WEBP decoder

	"github.com/tiennm99/tiennm99bot/internal/log"
)

const (
	// stickerEdge is Telegram's requirement: one side exactly 512px, the other
	// at most 512px.
	stickerEdge = 512
	// maxDecodeDimension bounds peak allocation. Checked via DecodeConfig,
	// before any pixel buffer exists.
	//
	// Note the 2 MB source cap bounds *compressed* bytes and so does not bound
	// this: a flat-colour 4096x4096 PNG is a few tens of KB. At this cap a
	// single conversion peaks near 90 MB live — the decoded source, the NRGBA
	// scale target, and the PNG encode buffer coexist. Handlers run one at a
	// time, so this is RSS pressure rather than a multiplier.
	maxDecodeDimension = 4096

	// softMaxStickerBytes is a *client-side* ceiling, not a documented limit.
	// The widely-repeated 512 KB figure appears in no current official page, so
	// it must never be stated to users as a rule.
	softMaxStickerBytes = 512 << 10
)

// toStickerPNG converts an arbitrary image to a PNG sized for a static sticker:
// long edge exactly 512, aspect ratio preserved.
func toStickerPNG(src []byte) ([]byte, error) {
	img, err := decodeBounded(src)
	if err != nil {
		return nil, err
	}

	b := img.Bounds()
	w, h := scaleToLongEdge(b.Dx(), b.Dy(), stickerEdge)
	out := resize(img, w, h)

	data, err := encodePNG(out, png.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if len(data) <= softMaxStickerBytes {
		return data, nil
	}

	// Try harder before losing resolution.
	if data, err = encodePNG(out, png.BestCompression); err == nil && len(data) <= softMaxStickerBytes {
		return data, nil
	}
	// Then step the long edge down. Below 320 the sticker is too small to be
	// worth shrinking further; hand back the best effort instead.
	//
	// Each rung resamples the already-scaled 512px image, not the source. The
	// source may be 4096x4096, and resampling it once per rung made the ladder
	// cost four full-size resamples instead of one — seconds of uninterruptible
	// CPU on a dispatcher that runs handlers one at a time, so a single user
	// could stall the bot for everyone. Target dimensions still come from the
	// original ratio, so the aspect is identical either way.
	for _, edge := range []int{448, 384, 320} {
		w, h = scaleToLongEdge(b.Dx(), b.Dy(), edge)
		candidate, encErr := encodePNG(resize(out, w, h), png.BestCompression)
		if encErr != nil {
			return nil, encErr
		}
		data = candidate
		if len(data) <= softMaxStickerBytes {
			return data, nil
		}
	}
	log.Error("sticker_image_oversized", "bytes", len(data))
	return data, nil
}

// decodeBounded reads the header first and refuses oversized images before any
// pixel buffer is allocated.
func decodeBounded(src []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, refuse("That file is not an image Telegram can use. Send a PNG, JPEG or WEBP.")
	}
	if cfg.Width > maxDecodeDimension || cfg.Height > maxDecodeDimension {
		return nil, refuse("That image is too big to process. Keep both sides under 4096 pixels.")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		// Not "too large" — a header claiming no pixels at all. The user can
		// act on this, so it must be a userError rather than surfacing as a
		// generic failure with an ERROR log line.
		return nil, refuse("That image has no usable dimensions. Send a normal PNG, JPEG or WEBP.")
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, refuse("That image could not be read. Send a PNG, JPEG or WEBP.")
	}
	return img, nil
}

// scaleToLongEdge returns the dimensions that put the long edge exactly at
// edge, keeping the aspect ratio. The short edge is clamped to at least 1 so an
// extreme aspect ratio cannot produce a zero-dimension image.
func scaleToLongEdge(w, h, edge int) (int, int) {
	if w <= 0 || h <= 0 {
		return edge, edge
	}
	if w >= h {
		short := int(float64(h)*float64(edge)/float64(w) + 0.5)
		return edge, max(short, 1)
	}
	short := int(float64(w)*float64(edge)/float64(h) + 0.5)
	return max(short, 1), edge
}

// resize scales into a fresh NRGBA, which preserves alpha.
func resize(img image.Image, w, h int) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(out, out.Bounds(), img, img.Bounds(), draw.Over, nil)
	return out
}

func encodePNG(img image.Image, level png.CompressionLevel) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: level}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
