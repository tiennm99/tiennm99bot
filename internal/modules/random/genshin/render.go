package genshin

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules/random/frames"
)

// renderTimeout bounds one wish. A render takes a second or two; the margin
// only stops a stuck ffmpeg from holding the handler.
const renderTimeout = 30 * time.Second

// clips holds one pre-rendered 854x480 30fps clip per rarity, encoded with
// +faststart so ffmpeg can read it from a pipe.
//
//go:embed assets/wish-3.mp4 assets/wish-4.mp4 assets/wish-5.mp4
var clips embed.FS

// Render returns the wish revealing label at rarity stars (3 to 5) as a
// silent H.264 MP4 of Width x Height, DurationSeconds long.
func Render(ctx context.Context, label string, rarity int) ([]byte, error) {
	if strings.TrimSpace(label) == "" {
		return nil, errors.New("genshin: label empty")
	}
	clip, err := clips.ReadFile(fmt.Sprintf("assets/wish-%d.mp4", rarity))
	if err != nil {
		return nil, fmt.Errorf("genshin: rarity %d out of range", rarity)
	}
	o, err := newOverlay(label, rarity)
	if err != nil {
		return nil, err
	}

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
	pipeErr := pipeFrames(src, encoder, o)
	if err := src.Close(); err != nil && pipeErr == nil {
		pipeErr = fmt.Errorf("genshin: %w", err)
	}
	if pipeErr != nil {
		encoder.Abort()
		return nil, pipeErr
	}
	return encoder.Finish()
}

// pipeFrames draws the overlay on each clip frame and writes the result to
// the encoder.
func pipeFrames(clip *frames.Clip, encoder *frames.Encoder, o *overlay) error {
	bounds := image.Rect(0, 0, Width, Height)
	src, dst := image.NewRGBA(bounds), image.NewRGBA(bounds)
	for index := range frameCount {
		if err := clip.Next(src); err != nil {
			return fmt.Errorf("genshin: %w", err)
		}
		o.compose(dst, src, index)
		if err := encoder.WriteFrame(dst); err != nil {
			return err
		}
	}
	return nil
}
