package sticker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/go-telegram/bot"

	"github.com/tiennm99/tiennm99bot/internal/log"
)

const (
	// maxSourceBytes bounds what an image source may pull over the network.
	// Telegram-compressed photo sizes are typically well under 500 KB.
	maxSourceBytes = 2 << 20

	// maxVideoSourceBytes is the same bound for a moving source. Higher than
	// maxSourceBytes because the ceiling is on the *source*, and a few seconds
	// of H.264 is far larger than any still Telegram would deliver — the
	// 256 KB sticker limit is enforced on the transcode output instead.
	maxVideoSourceBytes = 10 << 20

	// downloadTimeout is this command's own ceiling. The library's shared HTTP
	// client allows 60s, which is far too long to inherit on a dispatcher where
	// one slow handler stalls every other user.
	downloadTimeout = 8 * time.Second
)

// errDownloadFailed replaces every error from the download path.
//
// This is a security boundary, not tidiness. FileDownloadLink returns
// "https://api.telegram.org/file/bot<TOKEN>/<path>", and every transport
// failure from http.Client.Do is a *url.Error whose Error() embeds the full
// URL — which the dispatcher then logs verbatim. A mid-transfer timeout, which
// is trivially reachable, would print the bot token to stdout and every log
// shipper downstream.
var errDownloadFailed = errors.New("util: sticker download failed")

// downloadClient is separate from the library's so its timeout is ours.
var downloadClient = &http.Client{Timeout: downloadTimeout}

// downloadFile fetches a Telegram file by ID, bounded in bytes and time.
//
// The original error is discarded rather than wrapped: wrapping would keep the
// URL reachable through errors.Unwrap and %v, which defeats the point.
func downloadFile(ctx context.Context, b *bot.Bot, fileID string, maxBytes int64) ([]byte, error) {
	f, err := b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		log.Error("sticker_getfile", "file_id", fileID, "reason", classify(err))
		return nil, fmt.Errorf("file_id=%s: %w", fileID, errDownloadFailed)
	}
	if f.FileSize > maxBytes {
		return nil, refuse(tooLargeRefusal(maxBytes))
	}

	link := b.FileDownloadLink(f)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		log.Error("sticker_download_request", "file_id", fileID, "reason", classify(err))
		return nil, fmt.Errorf("file_id=%s: %w", fileID, errDownloadFailed)
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		log.Error("sticker_download", "file_id", fileID, "reason", classify(err))
		return nil, fmt.Errorf("file_id=%s: %w", fileID, errDownloadFailed)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		log.Error("sticker_download", "file_id", fileID, "reason", "status", "status", resp.StatusCode)
		return nil, fmt.Errorf("file_id=%s: %w", fileID, errDownloadFailed)
	}

	// Never trust Content-Length; bound the reader itself. One extra byte is
	// read so an oversized body is detected rather than silently truncated.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		log.Error("sticker_download_read", "file_id", fileID, "reason", classify(err))
		return nil, fmt.Errorf("file_id=%s: %w", fileID, errDownloadFailed)
	}
	if int64(len(data)) > maxBytes {
		return nil, refuse(tooLargeRefusal(maxBytes))
	}
	return data, nil
}

// tooLargeRefusal states the cap that was exceeded. The limit is a parameter,
// so it must come from the same value the check used rather than a constant a
// later edit could leave behind.
func tooLargeRefusal(maxBytes int64) string {
	return fmt.Sprintf("That file is too large — keep it under %d MB.", maxBytes>>20)
}

// classify reduces an error to a coarse label that cannot contain a URL.
//
// It deliberately inspects only the error's *type*, never its text: any path
// that formats the original error risks carrying the token along with it.
func classify(err error) string {
	var urlErr *url.Error
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.As(err, &urlErr):
		if urlErr.Timeout() {
			return "timeout"
		}
		return "transport"
	}
	return "unknown"
}
