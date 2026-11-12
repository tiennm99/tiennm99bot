package random

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// rendererURLEnv holds the renderer's base URL, e.g. http://renderer:3000.
	// The standard renderer is the renderer/ service in this repository, wired
	// in by compose.yml and reachable only on the compose network, so requests
	// carry no credentials. The client appends each animation's /api/<name>
	// path itself.
	rendererURLEnv = "RENDERER_URL"

	rendererMaxBytes = 12 << 20
	// rendererTimeout outlasts the renderer's own default render limit
	// (RENDERER_RENDER_TIMEOUT_MS, 30s) plus upload time, so a slow render
	// ends in the renderer's 504 rather than the bot abandoning a render
	// that was about to finish.
	rendererTimeout = 45 * time.Second
)

var errRendererNotConfigured = errors.New("renderer not configured")

// rendererClient calls the animation renderer. BaseURL is the service root;
// every animation lives under its /api path.
type rendererClient struct {
	HTTP    *http.Client
	BaseURL string
}

// renderedAnimation is a rendered clip plus the metadata Telegram's
// sendAnimation wants.
type renderedAnimation struct {
	Data     []byte
	Duration int
	Width    int
	Height   int
}

func newRendererClientFromEnv() rendererClient {
	return rendererClient{
		BaseURL: strings.TrimSpace(os.Getenv(rendererURLEnv)),
	}
}

// post sends one JSON render request to endpoint and returns the response
// body once it is a 2xx of the expected media type that passes isValid.
func (c rendererClient) post(ctx context.Context, endpoint *url.URL, body []byte, mediaType string, isValid func([]byte) bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("renderer request build failed: %w", err)
	}
	req.Header.Set("Accept", mediaType)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, errors.New("renderer request failed")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("renderer status %d", resp.StatusCode)
	}
	if err := requireContentType(resp.Header.Get("Content-Type"), mediaType); err != nil {
		return nil, err
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, rendererMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("renderer response read failed: %w", err)
	}
	if len(data) > rendererMaxBytes {
		return nil, fmt.Errorf("renderer response too large")
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("renderer response empty")
	}
	if !isValid(data) {
		return nil, fmt.Errorf("renderer response is not %s", mediaType)
	}
	return data, nil
}

func (c rendererClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: rendererTimeout}
}

// endpoint resolves the renderer route /api/<name> under BaseURL. A path
// prefix on BaseURL is kept, so a renderer mounted below a reverse-proxy path
// still works.
func (c rendererClient) endpoint(name string) (*url.URL, error) {
	base, err := rendererBaseURL(c.BaseURL)
	if err != nil {
		return nil, err
	}
	return base.JoinPath("api", name), nil
}

// rendererBaseURL validates the configured base URL.
func rendererBaseURL(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errRendererNotConfigured
	}
	base, err := url.Parse(rawURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("renderer url invalid")
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("renderer url scheme %q unsupported", base.Scheme)
	}
	return base, nil
}

func requireContentType(contentType, want string) error {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != want {
		return fmt.Errorf("renderer content type %q unsupported", contentType)
	}
	return nil
}

// rendererConfigured reports whether RENDERER_URL names a usable renderer.
func rendererConfigured() bool {
	_, err := rendererBaseURL(newRendererClientFromEnv().BaseURL)
	return err == nil
}
