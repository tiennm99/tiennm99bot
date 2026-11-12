package genshin

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"image"
	"io"
	"os/exec"
	"strconv"
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
	size := strconv.Itoa(Width) + "x" + strconv.Itoa(Height)
	// #nosec G204 — argv holds only constants; the clip arrives on stdin.
	decoder := exec.CommandContext(ctx, frames.FFmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "mp4", "-i", "pipe:0",
		"-vf", "scale="+size+":flags=bicubic,fps="+strconv.Itoa(FPS),
		"-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1")
	var decodeErr bytes.Buffer
	decoder.Stdin = bytes.NewReader(clip)
	decoder.Stderr = &decodeErr
	clipFrames, err := decoder.StdoutPipe()
	if err != nil {
		encoder.Abort()
		return nil, fmt.Errorf("genshin: decoder pipe: %w", err)
	}
	if err := decoder.Start(); err != nil {
		encoder.Abort()
		return nil, fmt.Errorf("genshin: start decoder: %w", err)
	}

	pipeErr := pipeFrames(clipFrames, encoder, o)
	// Drain whatever the decoder still has so it can exit.
	_, _ = io.Copy(io.Discard, clipFrames)
	if err := decoder.Wait(); err != nil && pipeErr == nil {
		pipeErr = fmt.Errorf("genshin: decode: %w: %s", err, strings.TrimSpace(decodeErr.String()))
	}
	if pipeErr != nil {
		encoder.Abort()
		return nil, pipeErr
	}
	return encoder.Finish()
}

// pipeFrames reads each decoded clip frame, draws the overlay on it, and
// writes the result to the encoder. If the clip runs short, its last frame
// holds so the wish always lasts DurationSeconds.
func pipeFrames(clip io.Reader, encoder *frames.Encoder, o *overlay) error {
	bounds := image.Rect(0, 0, Width, Height)
	src, dst := image.NewRGBA(bounds), image.NewRGBA(bounds)
	decoded := 0
	for index := range frameCount {
		if decoded == index {
			if _, err := io.ReadFull(clip, src.Pix); err == nil {
				decoded++
			} else if decoded == 0 {
				return fmt.Errorf("genshin: clip has no frames: %w", err)
			}
		}
		o.compose(dst, src, index)
		if err := encoder.WriteFrame(dst); err != nil {
			return err
		}
	}
	return nil
}
