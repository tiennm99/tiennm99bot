// Package export turns a novel URL into a PDF file: resolve the chapter list,
// fetch the chapters, render the PDF with the bundled font.
package export

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules/monkeyd/crawler"
	"github.com/tiennm99/tiennm99bot/internal/modules/monkeyd/pdf"
)

// Defaults the bot advertises to users or reuses in other crawls.
const (
	DefaultFontSize = 10.0
	DefaultDelay    = 400 * time.Millisecond
)

// Fixed layout and crawl settings. The bot exposes only the font size.
const (
	lineSpacing = 1.55 // multiple of font size
	margin      = 6.0  // millimetres
	workers     = 4
	retries     = 3
)

// Request describes one export.
type Request struct {
	NovelURL string

	// OutDir receives the PDF, named after the novel title.
	OutDir string

	// FontSize is in points. 0 means DefaultFontSize.
	FontSize float64

	// CacheDir stores raw pages so a re-export costs no requests. Empty
	// disables the cache.
	CacheDir string

	// Log receives progress messages. Optional.
	Log func(format string, args ...any)
}

// Result reports what was produced.
type Result struct {
	Path      string
	Title     string
	SourceURL string
	Chapters  int
	Words     int
	Page      pdf.PageSize
}

// Summary renders a one-line description of the exported book.
func (r *Result) Summary() string {
	return fmt.Sprintf("%s — %d chapters, %d words", r.Title, r.Chapters, r.Words)
}

// Export fetches the novel at req.NovelURL and writes it as a PDF, returning
// where it landed. The context bounds the whole crawl; cancelling it abandons
// the run without leaving a partial PDF behind.
func Export(ctx context.Context, req Request) (*Result, error) {
	if req.FontSize == 0 {
		req.FontSize = DefaultFontSize
	}
	if err := req.validate(); err != nil {
		return nil, err
	}

	c := &crawler.Crawler{
		Client:   crawler.NewClient(DefaultDelay, retries),
		CacheDir: req.CacheDir,
		Workers:  workers,
		Log:      req.Log,
	}

	novel, err := c.Novel(ctx, req.NovelURL)
	if err != nil {
		return nil, err
	}

	chapters, err := c.Chapters(ctx, novel)
	if err != nil {
		return nil, err
	}

	outPath := filepath.Join(req.OutDir, SafeFileName(novel.Title, novel.Slug)+".pdf")
	opts := pdf.Options{
		Page:        pdf.PhonePage,
		Margin:      margin,
		Font:        pdf.BundledFont(),
		FontSize:    req.FontSize,
		LineSpacing: lineSpacing,
		Title:       novel.Title,
		SourceURL:   novel.URL,
	}
	if err := pdf.Write(outPath, opts, toPDFChapters(chapters)); err != nil {
		return nil, err
	}

	return &Result{
		Path:      outPath,
		Title:     novel.Title,
		SourceURL: novel.URL,
		Chapters:  len(chapters),
		Words:     crawler.TotalWords(chapters),
		Page:      pdf.PhonePage,
	}, nil
}

// validate rejects a Request before any request is made, so a typo costs no
// fetches.
func (r *Request) validate() error {
	if r.NovelURL == "" {
		return fmt.Errorf("novel url is required")
	}
	parsed, err := url.Parse(r.NovelURL)
	if err != nil {
		return fmt.Errorf("invalid novel url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("invalid novel url: want an http(s) URL, got %q", r.NovelURL)
	}
	if r.FontSize <= 0 {
		return fmt.Errorf("font size must be positive")
	}
	return nil
}

func toPDFChapters(chapters []*crawler.Chapter) []pdf.Chapter {
	out := make([]pdf.Chapter, 0, len(chapters))
	for _, ch := range chapters {
		out = append(out, pdf.Chapter{Heading: ch.Heading(), Paragraphs: ch.Paragraphs})
	}
	return out
}

var unsafeNameChars = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// SafeFileName builds a file name from the novel title, falling back to the
// slug when the title has no usable characters. The result has no extension.
func SafeFileName(title, fallback string) string {
	name := strings.Trim(unsafeNameChars.ReplaceAllString(title, "-"), "-")
	if name == "" {
		return fallback
	}
	return name
}
