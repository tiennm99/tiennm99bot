package sticker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/log"
)

const (
	// ffmpegBinary is looked up on PATH. The runtime image installs it; see the
	// Dockerfile. Nothing else in this bot shells out, so this is the one place
	// an external binary is a hard dependency.
	ffmpegBinary = "ffmpeg"

	// Telegram's video-sticker rules (core.telegram.org/stickers): one side
	// exactly 512px and the other 512 or less, at most 3 seconds, at most
	// 30 FPS, at most 256 KB, WEBM/VP9, and **no audio stream**.
	maxStickerVideoSeconds = 3
	maxStickerVideoFPS     = 30
	maxStickerVideoBytes   = 256 << 10

	// ffmpegTimeout bounds one encode.
	//
	// Handlers run inline on a single worker, so this is time the whole bot is
	// unresponsive. It is set well above the observed cost (a 1280x720 source
	// encodes in ~0.4s with the flags below) purely so a pathological input
	// fails rather than hangs — not as a budget to be spent.
	ffmpegTimeout = 20 * time.Second
)

// errFFmpegMissing means the runtime image has no ffmpeg. A deployment fault,
// not a user error: it must never be echoed as a refusal the caller could act
// on, because nothing they send will help.
var errFFmpegMissing = errors.New("util: ffmpeg not found on PATH")

// stickerVideoCRFs is the quality ladder, tried in order until the output fits
// maxStickerVideoBytes.
//
// Lower is better quality and larger. 32 lands around 30 KB for ordinary
// footage — an order of magnitude inside the limit — so the later rungs exist
// for dense, high-motion sources rather than as the expected path.
var stickerVideoCRFs = []int{32, 42, 52}

// toStickerWEBM converts arbitrary video or GIF bytes into a WEBM/VP9 sticker.
//
// Every rule Telegram enforces is applied by the filter chain and the encoder
// flags rather than checked afterwards: the long edge is scaled to exactly 512
// (up or down — a 100x50 GIF becomes 512x256, not left undersized), the stream
// is cut at 3 seconds, the frame rate is capped at 30, and audio, subtitle and
// data streams are dropped outright. Only the size limit needs a retry, since
// it cannot be known before encoding.
func toStickerWEBM(ctx context.Context, src []byte) ([]byte, error) {
	if _, err := exec.LookPath(ffmpegBinary); err != nil {
		return nil, errFFmpegMissing
	}

	dir, err := os.MkdirTemp("", "sticker-video-*")
	if err != nil {
		return nil, fmt.Errorf("sticker video: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// No extension: ffmpeg probes the container from the content, so trusting a
	// caller-supplied name buys nothing and risks steering the demuxer wrong.
	in := filepath.Join(dir, "in")
	if err := os.WriteFile(in, src, 0o600); err != nil {
		return nil, fmt.Errorf("sticker video: write source: %w", err)
	}
	out := filepath.Join(dir, "out.webm")

	var best []byte
	for _, crf := range stickerVideoCRFs {
		data, err := runFFmpeg(ctx, in, out, crf)
		if err != nil {
			return nil, err
		}
		best = data
		if len(best) <= maxStickerVideoBytes {
			return best, nil
		}
	}

	// Past the last rung. Hand back the smallest attempt and let Telegram be
	// the authority: the 256 KB figure is documented, but the server is what
	// actually accepts or refuses, and apiRefusal already translates its answer.
	log.Error("sticker_video_oversized", "bytes", len(best))
	return best, nil
}

// runFFmpeg performs one encode at the given quality and returns the result.
func runFFmpeg(ctx context.Context, in, out string, crf int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()

	// scale sets the *longer* edge to exactly 512 and lets the other fall out
	// of the aspect ratio, rounded to an even number for yuv chroma. This is
	// deliberately not force_original_aspect_ratio=decrease, which leaves a
	// source smaller than 512 undersized and so fails Telegram's "one side must
	// be exactly 512" rule.
	filter := fmt.Sprintf(
		"scale=w='if(gte(iw,ih),%d,-2)':h='if(gte(iw,ih),-2,%d)':flags=lanczos,fps=%d",
		stickerEdge, stickerEdge, maxStickerVideoFPS)

	// #nosec G204 — no part of argv is user input. The binary is a package
	// constant, the flags are literals, the numbers come from constants and the
	// CRF ladder, the filter is built from constants, and in/out are paths
	// toStickerWEBM made under its own MkdirTemp directory. The caller's bytes reach
	// ffmpeg as the *contents* of `in`, never as an argument.
	cmd := exec.CommandContext(ctx, ffmpegBinary,
		"-hide_banner", "-loglevel", "error",
		"-nostdin", // never wait on a terminal; there is none
		"-y",
		"-i", in,
		"-t", strconv.Itoa(maxStickerVideoSeconds),
		"-an", "-sn", "-dn", // no audio, subtitle or data streams
		"-vf", filter,
		"-c:v", "libvpx-vp9",
		"-pix_fmt", "yuva420p", // keeps GIF/WEBP transparency; VP9 carries alpha
		"-b:v", "0", "-crf", strconv.Itoa(crf), // constant-quality mode
		"-deadline", "realtime", "-cpu-used", "5", "-row-mt", "1",
		"-f", "webm",
		out,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// stderr goes to the log, never to the reply: it carries file paths and
		// arbitrary demuxer output, and this command's refusals are held to the
		// same "nothing internal reaches the user" rule as the download path.
		log.Error("sticker_video_ffmpeg", "crf", crf, "err", err, "stderr", stderr.String())
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, refuse("That video took too long to convert. Try a shorter or smaller clip.")
		}
		return nil, refuse("That video could not be turned into a sticker. Try a different clip.")
	}

	// #nosec G304 — `out` is not a caller-supplied path: it is filepath.Join of
	// toStickerWEBM's own MkdirTemp directory and a fixed file name.
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("sticker video: read output: %w", err)
	}
	if len(data) == 0 {
		// A zero-byte output with a zero exit status means the input carried no
		// video stream at all — an audio file sent as a document, say.
		return nil, refuse("That file has no video to convert.")
	}
	return data, nil
}
