package cardpack

import (
	"encoding/json"
	"fmt"
	"sync"
)

// track is what scripts/blender/gacha-pack.py writes beside each clip: where
// the card is on every frame, so the label can be drawn onto it, and when the
// pack tears and the card lands, so the flashes line up with the clip.
type track struct {
	Width  int     `json:"width"`
	Height int     `json:"height"`
	FPS    int     `json:"fps"`
	Tear   float64 `json:"tear"`
	Land   float64 `json:"land"`
	// TearAt is where the pack tears on screen, in output pixels.
	TearAt [2]float64 `json:"tearAt"`
	// LabelUV is the label box in card UVs: left, top, right, bottom, with v
	// running up the card. The face's shine sweep is laid out in these UVs.
	LabelUV [4]float64   `json:"labelUV"`
	Frames  []trackFrame `json:"frames"`
}

type trackFrame struct {
	// Label and Card are corners on screen: top-left, top-right,
	// bottom-right, bottom-left, as x, y pairs in output pixels.
	Label quad `json:"q"`
	Card  quad `json:"c"`
	// Facing is the cosine between the face's normal and the direction to the
	// camera: 1 face on, negative when the back shows.
	Facing float64 `json:"f"`
	// Sweep is the shine band's position along u + 0.45v; off the card when
	// no sweep is running.
	Sweep float64 `json:"s"`
	// Opacity is how far the label has faded in.
	Opacity float64 `json:"a"`
}

type quad [8]float64

// centre is the mean of the corners.
func (q quad) centre() (float64, float64) {
	return (q[0] + q[2] + q[4] + q[6]) / 4, (q[1] + q[3] + q[5] + q[7]) / 4
}

var tracks = map[int]func() (*track, error){}

func init() {
	for _, rarity := range []int{3, 4, 5} {
		tracks[rarity] = sync.OnceValues(func() (*track, error) { return parseTrack(rarity) })
	}
}

func parseTrack(rarity int) (*track, error) {
	data, err := assets.ReadFile(fmt.Sprintf("assets/track-%d.json", rarity))
	if err != nil {
		return nil, fmt.Errorf("cardpack: track %d: %w", rarity, err)
	}
	var t track
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("cardpack: track %d: %w", rarity, err)
	}
	if t.Width != Width || t.Height != Height || t.FPS != FPS || len(t.Frames) != frameCount {
		return nil, fmt.Errorf("cardpack: track %d is %dx%d at %dfps with %d frames, want %dx%d at %dfps with %d",
			rarity, t.Width, t.Height, t.FPS, len(t.Frames), Width, Height, FPS, frameCount)
	}
	return &t, nil
}
