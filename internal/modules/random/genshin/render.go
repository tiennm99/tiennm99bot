package genshin

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ffmpegBinary is looked up on PATH; the runtime image installs it.
const ffmpegBinary = "ffmpeg"

// renderTimeout bounds one wish. A render takes a second or two; the margin
// only stops a stuck ffmpeg from holding the handler.
const renderTimeout = 30 * time.Second

// clips holds one pre-rendered 854x480 30fps clip per rarity, encoded with
// +faststart so ffmpeg can read it from a pipe.
//
//go:embed assets/wish-3.mp4 assets/wish-4.mp4 assets/wish-5.mp4
var clips embed.FS

// ErrFFmpegMissing means the runtime has no ffmpeg on PATH.
var ErrFFmpegMissing = errors.New("genshin: ffmpeg not found on PATH")

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
	if _, err := exec.LookPath(ffmpegBinary); err != nil {
		return nil, ErrFFmpegMissing
	}
	o, err := newOverlay(label, rarity)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()

	dir, err := os.MkdirTemp("", "genshin-*")
	if err != nil {
		return nil, fmt.Errorf("genshin: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	out := filepath.Join(dir, "wish.mp4")

	size := strconv.Itoa(Width) + "x" + strconv.Itoa(Height)
	// #nosec G204 — argv holds only constants and a path under our own temp
	// dir; the label never reaches ffmpeg, only the pixels drawn from it.
	decoder := exec.CommandContext(ctx, ffmpegBinary,
		"-hide_banner", "-loglevel", "error",
		"-f", "mp4", "-i", "pipe:0",
		"-vf", "scale="+size+":flags=bicubic,fps="+strconv.Itoa(FPS),
		"-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1")
	// #nosec G204 — as above.
	encoder := exec.CommandContext(ctx, ffmpegBinary,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "rawvideo", "-pix_fmt", "rgba", "-s", size, "-r", strconv.Itoa(FPS), "-i", "pipe:0",
		"-an", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
		"-movflags", "+faststart", "-f", "mp4", out)

	var decodeErr, encodeErr bytes.Buffer
	decoder.Stdin = bytes.NewReader(clip)
	decoder.Stderr = &decodeErr
	encoder.Stderr = &encodeErr
	frames, err := decoder.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("genshin: decoder pipe: %w", err)
	}
	sink, err := encoder.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("genshin: encoder pipe: %w", err)
	}
	if err := decoder.Start(); err != nil {
		return nil, fmt.Errorf("genshin: start decoder: %w", err)
	}
	if err := encoder.Start(); err != nil {
		_ = decoder.Process.Kill()
		_ = decoder.Wait()
		return nil, fmt.Errorf("genshin: start encoder: %w", err)
	}

	pipeErr := pipeFrames(frames, sink, o)
	_ = sink.Close()
	// Drain whatever the decoder still has so it can exit.
	_, _ = io.Copy(io.Discard, frames)
	waitDecode, waitEncode := decoder.Wait(), encoder.Wait()
	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("genshin: render: %w", ctx.Err())
	case waitDecode != nil:
		return nil, fmt.Errorf("genshin: decode: %w: %s", waitDecode, strings.TrimSpace(decodeErr.String()))
	case waitEncode != nil:
		return nil, fmt.Errorf("genshin: encode: %w: %s", waitEncode, strings.TrimSpace(encodeErr.String()))
	case pipeErr != nil:
		return nil, pipeErr
	}
	// #nosec G304 — out is a fixed name in our own temp dir.
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("genshin: read output: %w", err)
	}
	return data, nil
}

// pipeFrames reads each decoded clip frame, draws the overlay on it, and
// writes the result to the encoder. If the clip runs short, its last frame
// holds so the wish always lasts DurationSeconds.
func pipeFrames(frames io.Reader, sink io.Writer, o *overlay) error {
	bounds := image.Rect(0, 0, Width, Height)
	src, dst := image.NewRGBA(bounds), image.NewRGBA(bounds)
	decoded := 0
	for index := range frameCount {
		if decoded == index {
			if _, err := io.ReadFull(frames, src.Pix); err == nil {
				decoded++
			} else if decoded == 0 {
				return fmt.Errorf("genshin: clip has no frames: %w", err)
			}
		}
		o.compose(dst, src, index)
		if _, err := sink.Write(dst.Pix); err != nil {
			return fmt.Errorf("genshin: write frame %d: %w", index, err)
		}
	}
	return nil
}
