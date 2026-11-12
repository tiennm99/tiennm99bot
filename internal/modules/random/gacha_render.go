package random

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	gachaRemoteFPS    = 24
	gachaRemoteWidth  = 640
	gachaRemoteHeight = 360
)

// gachaStyle is a wish animation the renderer serves: its route name under
// /api, the clip length in seconds, and the frame size it renders for a
// gachaRemoteWidth request.
type gachaStyle struct {
	Path     string
	Duration int
	Width    int
	Height   int
}

// The card-pack wish is portrait: the requested width is its long edge.
var gachaStyleCardPack = gachaStyle{Path: "gacha", Duration: 6, Width: gachaRemoteHeight, Height: gachaRemoteWidth}

type gachaRenderRequest struct {
	Label  string `json:"label"`
	Rarity int    `json:"rarity"`
	FPS    int    `json:"fps"`
	Width  int    `json:"width"`
}

// RenderGacha returns the MP4 wish animation named path revealing label at
// rarity stars.
func (c rendererClient) RenderGacha(ctx context.Context, path, label string, rarity int) ([]byte, error) {
	endpoint, err := c.endpoint(path)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(label) == "" {
		return nil, fmt.Errorf("renderer gacha label empty")
	}
	if rarity < gachaMinRarity || rarity > gachaMaxRarity {
		return nil, fmt.Errorf("renderer gacha rarity %d out of range", rarity)
	}
	body, err := json.Marshal(gachaRenderRequest{
		Label:  label,
		Rarity: rarity,
		FPS:    gachaRemoteFPS,
		Width:  gachaRemoteWidth,
	})
	if err != nil {
		return nil, fmt.Errorf("renderer gacha request encode failed: %w", err)
	}
	return c.post(ctx, endpoint, body, "video/mp4", isMP4)
}

// isMP4 checks for the ISO base media "ftyp" box that opens every MP4.
func isMP4(data []byte) bool {
	return len(data) >= 8 && bytes.Equal(data[4:8], []byte("ftyp"))
}

func renderGachaAnimation(ctx context.Context, style gachaStyle, label string, rarity int) (renderedAnimation, error) {
	data, err := newRendererClientFromEnv().RenderGacha(ctx, style.Path, label, rarity)
	if err != nil {
		return renderedAnimation{}, err
	}
	return renderedAnimation{
		Data:     data,
		Duration: style.Duration,
		Width:    style.Width,
		Height:   style.Height,
	}, nil
}
