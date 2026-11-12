package cardpack

import (
	"bytes"
	"context"
	"image"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules/random/frames"
)

func TestHomographyMapsTheSquareOntoTheQuad(t *testing.T) {
	q := quad{110, 80, 430, 95, 470, 300, 90, 260}
	m := squareToQuad(q)
	for i, corner := range [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}} {
		x, y := m.apply(corner[0], corner[1])
		if math.Abs(x-q[2*i]) > 1e-9 || math.Abs(y-q[2*i+1]) > 1e-9 {
			t.Fatalf("corner %d maps to (%v, %v), want (%v, %v)", i, x, y, q[2*i], q[2*i+1])
		}
	}
	inverse, ok := m.inverse()
	if !ok {
		t.Fatal("a proper quad should invert")
	}
	x, y := m.apply(0.3, 0.7)
	u, v := inverse.apply(x, y)
	if math.Abs(u-0.3) > 1e-9 || math.Abs(v-0.7) > 1e-9 {
		t.Fatalf("round trip = (%v, %v), want (0.3, 0.7)", u, v)
	}
	if _, ok := squareToQuad(quad{5, 5, 5, 5, 5, 5, 5, 5}).inverse(); ok {
		t.Fatal("a collapsed quad should not invert")
	}
}

func TestTracksMatchTheClips(t *testing.T) {
	for _, rarity := range []int{3, 4, 5} {
		tr, err := tracks[rarity]()
		if err != nil {
			t.Fatal(err)
		}
		if !(0 < tr.Tear && tr.Tear < tr.Land && tr.Land < DurationSeconds-2) {
			t.Fatalf("rarity %d: tear %v and land %v out of order", rarity, tr.Tear, tr.Land)
		}
		// The card shows its back before it spins and its face at the end,
		// with the label fully in.
		first, last := tr.Frames[0], tr.Frames[frameCount-1]
		if first.Facing > 0 || last.Facing < 0.9 || last.Opacity != 1 || first.Opacity != 0 {
			t.Fatalf("rarity %d: first frame %+v, last frame %+v", rarity, first, last)
		}
		// The label box lies inside the card on screen.
		x, y := last.Label.centre()
		if !inside(last.Card, x, y) {
			t.Fatalf("rarity %d: label centre (%v, %v) is off the card %v", rarity, x, y, last.Card)
		}
	}
}

// inside reports whether (x, y) is inside the convex quad q.
func inside(q quad, x, y float64) bool {
	sign := 0.0
	for i := range 4 {
		ax, ay, bx, by := q[2*i], q[2*i+1], q[(2*i+2)%8], q[(2*i+3)%8]
		cross := (bx-ax)*(y-ay) - (by-ay)*(x-ax)
		if sign == 0 {
			sign = cross
		} else if cross*sign < 0 {
			return false
		}
	}
	return true
}

func TestPaintLabelWrapsLongLabels(t *testing.T) {
	short, err := paintLabel("Pizza", looks[5])
	if err != nil {
		t.Fatal(err)
	}
	long, err := paintLabel(strings.Repeat("Bún bò Huế ", 5), looks[5])
	if err != nil {
		t.Fatal(err)
	}
	if rows(short.glyphs) >= rows(long.glyphs) {
		t.Fatalf("a long label should take more lines: %d rows vs %d", rows(long.glyphs), rows(short.glyphs))
	}
	// Nothing may spill past the padding, or the warp would clip it.
	b := long.glyphs.Bounds()
	for x := range b.Dx() {
		if long.glyphs.AlphaAt(x, 0).A != 0 || long.glyphs.AlphaAt(x, b.Dy()-1).A != 0 {
			t.Fatal("glyphs reach the texture edge")
		}
	}
}

// rows counts the texture rows that hold any glyph coverage.
func rows(img *image.Alpha) int {
	n := 0
	b := img.Bounds()
	for y := range b.Dy() {
		for x := range b.Dx() {
			if img.AlphaAt(x, y).A > 0 {
				n++
				break
			}
		}
	}
	return n
}

func TestGlintsLandOnTheCardAfterTheLanding(t *testing.T) {
	tr, err := tracks[5]()
	if err != nil {
		t.Fatal(err)
	}
	glints := placeGlints(tr, looks[5], 1)
	if len(glints) != looks[5].glints {
		t.Fatalf("got %d glints, want %d", len(glints), looks[5].glints)
	}
	for _, g := range glints {
		if g.start < tr.Land || g.start+g.life > DurationSeconds || g.u < 0 || g.u > 1 || g.v < 0 || g.v > 1 {
			t.Fatalf("glint %+v outside the card or the clip", g)
		}
	}
}

func TestRenderRejectsBadInput(t *testing.T) {
	if _, err := Render(context.Background(), " ", 5, 1); err == nil {
		t.Fatal("empty label should fail")
	}
	if _, err := Render(context.Background(), "Pizza", 6, 1); err == nil {
		t.Fatal("rarity 6 should fail")
	}
}

func TestRenderProducesTheWish(t *testing.T) {
	if _, err := exec.LookPath(frames.FFmpeg); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	for _, rarity := range []int{3, 4, 5} {
		t.Run(strconv.Itoa(rarity), func(t *testing.T) {
			data, err := Render(context.Background(), "Bún bò", rarity, 7)
			if err != nil {
				t.Fatal(err)
			}
			if len(data) < 8 || !bytes.Equal(data[4:8], []byte("ftyp")) {
				t.Fatal("output is not an MP4")
			}
			probe, err := exec.LookPath("ffprobe")
			if err != nil {
				return
			}
			cmd := exec.Command(probe, "-v", "error", "-select_streams", "v:0", "-count_packets",
				"-show_entries", "stream=width,height,nb_read_packets", "-of", "csv=p=0", "-i", "pipe:0")
			cmd.Stdin = bytes.NewReader(data)
			got, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			want := strconv.Itoa(Width) + "," + strconv.Itoa(Height) + "," + strconv.Itoa(frameCount)
			if strings.TrimSpace(string(got)) != want {
				t.Fatalf("stream = %q, want %q", strings.TrimSpace(string(got)), want)
			}
		})
	}
}
