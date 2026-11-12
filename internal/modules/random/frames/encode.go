package frames

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// FFmpeg is looked up on PATH; the runtime image installs it.
const FFmpeg = "ffmpeg"

// ErrFFmpegMissing means the runtime has no ffmpeg on PATH.
var ErrFFmpegMissing = errors.New("frames: ffmpeg not found on PATH")

// Encoder pipes RGBA frames into ffmpeg and collects a silent H.264 MP4,
// which Telegram plays as an animation.
type Encoder struct {
	ctx    context.Context
	cmd    *exec.Cmd
	sink   io.WriteCloser
	dir    string
	out    string
	stderr bytes.Buffer
}

// NewEncoder starts ffmpeg for frames of width x height at fps. Finish or
// Abort it.
func NewEncoder(ctx context.Context, width, height, fps int) (*Encoder, error) {
	if _, err := exec.LookPath(FFmpeg); err != nil {
		return nil, ErrFFmpegMissing
	}
	dir, err := os.MkdirTemp("", "frames-*")
	if err != nil {
		return nil, fmt.Errorf("frames: temp dir: %w", err)
	}
	e := &Encoder{ctx: ctx, dir: dir, out: filepath.Join(dir, "out.mp4")}
	// #nosec G204 — argv holds only constants, numbers, and a path under our
	// own temp dir; user text reaches ffmpeg only as drawn pixels.
	e.cmd = exec.CommandContext(ctx, FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-s", strconv.Itoa(width)+"x"+strconv.Itoa(height), "-r", strconv.Itoa(fps), "-i", "pipe:0",
		"-an", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
		"-movflags", "+faststart", "-f", "mp4", e.out)
	e.cmd.Stderr = &e.stderr
	if e.sink, err = e.cmd.StdinPipe(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("frames: encoder pipe: %w", err)
	}
	if err := e.cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("frames: start encoder: %w", err)
	}
	return e, nil
}

// WriteFrame sends one frame.
func (e *Encoder) WriteFrame(frame *image.RGBA) error {
	if _, err := e.sink.Write(frame.Pix); err != nil {
		return fmt.Errorf("frames: write frame: %w", err)
	}
	return nil
}

// Finish ends the stream and returns the MP4.
func (e *Encoder) Finish() ([]byte, error) {
	defer func() { _ = os.RemoveAll(e.dir) }()
	_ = e.sink.Close()
	if err := e.cmd.Wait(); err != nil {
		if e.ctx.Err() != nil {
			return nil, fmt.Errorf("frames: encode: %w", e.ctx.Err())
		}
		return nil, fmt.Errorf("frames: encode: %w: %s", err, strings.TrimSpace(e.stderr.String()))
	}
	// #nosec G304 — out is a fixed name in our own temp dir.
	data, err := os.ReadFile(e.out)
	if err != nil {
		return nil, fmt.Errorf("frames: read output: %w", err)
	}
	return data, nil
}

// Abort stops ffmpeg and discards the output.
func (e *Encoder) Abort() {
	_ = e.sink.Close()
	if e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
	}
	_ = e.cmd.Wait()
	_ = os.RemoveAll(e.dir)
}
