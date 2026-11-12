package frames

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// Clip streams the frames of an MP4 held in memory as RGBA, scaled to one
// size and resampled to one frame rate. The MP4 must be encoded with
// +faststart, since ffmpeg reads it from a pipe.
type Clip struct {
	cmd    *exec.Cmd
	out    io.ReadCloser
	stderr bytes.Buffer
	// last is the newest whole frame; next receives the one after it.
	last, next []byte
	decoded    int
	ended      bool
}

// OpenClip starts decoding mp4 into width x height frames at fps. Close it.
func OpenClip(ctx context.Context, mp4 []byte, width, height, fps int) (*Clip, error) {
	if _, err := exec.LookPath(FFmpeg); err != nil {
		return nil, ErrFFmpegMissing
	}
	size := strconv.Itoa(width) + "x" + strconv.Itoa(height)
	// #nosec G204 — argv holds only constants; the clip arrives on stdin.
	cmd := exec.CommandContext(ctx, FFmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "mp4", "-i", "pipe:0",
		"-vf", "scale="+size+":flags=bicubic,fps="+strconv.Itoa(fps),
		"-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1")
	c := &Clip{cmd: cmd}
	cmd.Stdin = bytes.NewReader(mp4)
	cmd.Stderr = &c.stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("frames: decoder pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("frames: start decoder: %w", err)
	}
	c.out = out
	return c, nil
}

// Next fills dst, which must match the clip's size, with the next frame.
// Once the clip runs out its last frame repeats, so a clip a frame or two
// short of the animation still fills it.
func (c *Clip) Next(dst *image.RGBA) error {
	if !c.ended {
		if c.next == nil {
			c.next, c.last = make([]byte, len(dst.Pix)), make([]byte, len(dst.Pix))
		}
		// Read into a spare buffer: a short read must not spoil the frame
		// that repeats.
		_, err := io.ReadFull(c.out, c.next)
		switch {
		case err == nil:
			c.next, c.last = c.last, c.next
			c.decoded++
		case c.decoded == 0:
			// Close reports why: ffmpeg's stderr is only safe to read after Wait.
			return fmt.Errorf("frames: clip has no frames: %w", err)
		case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
			c.ended = true
		default:
			return fmt.Errorf("frames: read clip: %w", err)
		}
	}
	copy(dst.Pix, c.last)
	return nil
}

// Close drains and stops the decoder, returning its error, if any.
func (c *Clip) Close() error {
	_, _ = io.Copy(io.Discard, c.out)
	if err := c.cmd.Wait(); err != nil {
		return fmt.Errorf("frames: decode: %w: %s", err, strings.TrimSpace(c.stderr.String()))
	}
	return nil
}
