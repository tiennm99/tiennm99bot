package caseopen

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules/random/frames"
)

var sampleItems = []Item{{"Bún bò", 5}, {"Phở bò tái chín", 4}, {"Cơm tấm", 3}, {"Bánh mì", 3}}

func TestReelStopsInsideTheWinningCard(t *testing.T) {
	for seed := range uint64(50) {
		s, err := newScene(sampleItems, 1, seed)
		if err != nil {
			t.Fatal(err)
		}
		if s.reel[winnerSlot] != 1 {
			t.Fatalf("seed %d: winner slot holds item %d", seed, s.reel[winnerSlot])
		}
		win := s.winnerRect()
		if win.Min.X+cellW/10 >= Width/2 || Width/2 >= win.Max.X-cellW/10 {
			t.Fatalf("seed %d: marker at %d is too close to the winning card's edge %v", seed, Width/2, win)
		}
		if win.Max.X > Width-1 || win.Min.X < 0 {
			t.Fatalf("seed %d: winning card %v leaves the frame", seed, win)
		}
		if got := s.shift(spinEnd + 1); got != s.finalShift {
			t.Fatalf("seed %d: reel keeps moving after it stops", seed)
		}
	}
}

func TestReelSlowsDown(t *testing.T) {
	s, err := newScene(sampleItems, 0, 7)
	if err != nil {
		t.Fatal(err)
	}
	early := s.shift(1.0) - s.shift(0.9)
	late := s.shift(5.0) - s.shift(4.9)
	if !(early > late && late > 0) {
		t.Fatalf("speeds early %v late %v, want slowing to a stop", early, late)
	}
}

func TestRenderRejectsBadInput(t *testing.T) {
	for name, items := range map[string][]Item{
		"empty label": {{" ", 5}},
		"bad rarity":  {{"Pizza", 6}},
	} {
		if _, err := Render(context.Background(), items, 0, 1); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := Render(context.Background(), sampleItems, 9, 1); err == nil {
		t.Error("winner out of range: want an error")
	}
}

func TestRenderProducesTheOpening(t *testing.T) {
	if _, err := exec.LookPath(frames.FFmpeg); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	for winner, item := range sampleItems[:3] {
		t.Run(strconv.Itoa(item.Rarity), func(t *testing.T) {
			data, err := Render(context.Background(), sampleItems, winner, uint64(winner))
			if err != nil {
				t.Fatal(err)
			}
			if len(data) < 8 || !bytes.Equal(data[4:8], []byte("ftyp")) {
				t.Fatal("output is not an MP4")
			}
			if dir := os.Getenv("CASEOPEN_DUMP_DIR"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, "case-"+strconv.Itoa(item.Rarity)+".mp4"), data, 0o600); err != nil {
					t.Fatal(err)
				}
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
