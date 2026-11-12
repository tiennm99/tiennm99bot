package genshin

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules/random/frames"
)

func TestTimelineOrder(t *testing.T) {
	if !(flashStart < flashPeak && flashPeak < flashHoldEnd && flashHoldEnd < flashEnd) {
		t.Fatal("flash phases out of order")
	}
	if revealStart > flashHoldEnd {
		t.Fatal("the cut to the reveal must happen while the flash is opaque")
	}
	last := starRevealTimes(5)[4]
	if last+1 >= DurationSeconds {
		t.Fatalf("last star at %vs leaves no time to read it", last)
	}
}

func TestFlashCoversTheCut(t *testing.T) {
	if got := flashOpacity(revealStart); got != 1 {
		t.Fatalf("flash at the cut = %v, want 1", got)
	}
	if flashOpacity(flashStart) != 0 || flashOpacity(flashEnd) != 0 {
		t.Fatal("flash should be clear before it starts and after it ends")
	}
}

func TestStarRevealTimes(t *testing.T) {
	for _, rarity := range []int{3, 4, 5} {
		times := starRevealTimes(rarity)
		if len(times) != rarity || times[0] != starsStart {
			t.Fatalf("rarity %d: times = %v", rarity, times)
		}
	}
}

func TestLabelFontSizeShrinksLongLabels(t *testing.T) {
	if labelFontSize("Pizza", Height) <= labelFontSize(strings.Repeat("x", 40), Height) {
		t.Fatal("long labels should get smaller text")
	}
}

func TestShakeDecaysToRest(t *testing.T) {
	if x, y := shakeOffset(1, 2, 10); x != 0 || y != 0 {
		t.Fatal("no shake before the impact")
	}
	if x, y := shakeOffset(3, 2, 10); x != 0 || y != 0 {
		t.Fatal("shake should settle")
	}
	ex, ey := shakeOffset(2.05, 2, 10)
	lx, ly := shakeOffset(2.7, 2, 10)
	if ex*ex+ey*ey <= lx*lx+ly*ly {
		t.Fatal("shake should decay")
	}
}

func TestTiersGrowWithRarity(t *testing.T) {
	if !(tiers[3].shake < tiers[4].shake && tiers[4].shake < tiers[5].shake) {
		t.Fatal("shake should grow with rarity")
	}
	if !(tiers[3].starFlare < tiers[4].starFlare && tiers[4].starFlare < tiers[5].starFlare) {
		t.Fatal("star flare should grow with rarity")
	}
}

func TestRenderRejectsBadInput(t *testing.T) {
	if _, err := Render(context.Background(), " ", 5); err == nil {
		t.Fatal("empty label should fail")
	}
	if _, err := Render(context.Background(), "Pizza", 6); err == nil {
		t.Fatal("rarity 6 should fail")
	}
}

func TestRenderProducesTheWish(t *testing.T) {
	if _, err := exec.LookPath(frames.FFmpeg); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	for _, rarity := range []int{3, 4, 5} {
		t.Run(strconv.Itoa(rarity), func(t *testing.T) {
			data, err := Render(context.Background(), "Bún bò", rarity)
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
