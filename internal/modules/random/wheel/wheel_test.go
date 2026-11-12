package wheel

import (
	"bytes"
	"context"
	"math"
	"math/rand/v2"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules/random/frames"
)

func TestSpinLandsClearlyOnTheWinner(t *testing.T) {
	for _, n := range []int{1, 2, 7, 16, MaxOptions} {
		for seed := range uint64(3) {
			winner := int(seed) % n
			s := planSpin(n, winner, rand.New(rand.NewPCG(seed, 9)))
			got, frac := segmentUnderPointer(n, s.rest)
			if got != winner {
				t.Fatalf("n=%d seed=%d: landed on %d, want %d", n, seed, got, winner)
			}
			if margin := math.Min(frac, 1-frac) * 2 * math.Pi / float64(n) * pegRadius; n > 1 && margin < 10 {
				t.Fatalf("n=%d seed=%d: tip %.0fpx from a peg, want it clearly inside", n, seed, margin)
			}
			if s.stopped > spinEnd || s.stopped < releaseTime+2 {
				t.Fatalf("n=%d seed=%d: stopped at %.2fs", n, seed, s.stopped)
			}
		}
	}
}

func TestSpinWindsUpThenSpeedsUpThenSlowsDown(t *testing.T) {
	s := planSpin(12, 0, rand.New(rand.NewPCG(1, 2)))
	speed := func(t float64) float64 { return (at(s.theta, t) - at(s.theta, t-0.02)) / 0.02 }
	if speed(0.2) >= 0 {
		t.Fatal("the hand should pull the wheel back first")
	}
	if !(speed(0.5) < speed(0.7)) {
		t.Fatal("the push should speed the wheel up")
	}
	if !(speed(1.5) > speed(3) && speed(3) > speed(4.5)) {
		t.Fatal("the wheel should slow down")
	}
}

func TestSomeSpinsRockBackOffAPeg(t *testing.T) {
	rebounds := 0
	for seed := range uint64(10) {
		if planSpin(12, 0, rand.New(rand.NewPCG(seed, 3))).rebound > 0.03 {
			rebounds++
		}
	}
	if rebounds == 0 || rebounds == 10 {
		t.Fatalf("%d of 10 spins rocked back; want some, not all", rebounds)
	}
}

func TestSegmentColoursNeverRepeatSideBySide(t *testing.T) {
	for n := 2; n <= MaxOptions; n++ {
		c := segmentColours(n)
		for i := range c {
			if c[i] == c[(i+1)%n] {
				t.Fatalf("n=%d: segments %d and %d share a colour", n, i, (i+1)%n)
			}
		}
	}
}

func TestRenderRejectsBadInput(t *testing.T) {
	for name, tc := range map[string]struct {
		options []string
		winner  int
	}{
		"no options":   {nil, 0},
		"too many":     {strings.Split(strings.Repeat("x,", MaxOptions+1), ",")[:MaxOptions+1], 0},
		"empty option": {[]string{"a", " "}, 0},
		"bad winner":   {[]string{"a", "b"}, 2},
	} {
		if _, err := Render(context.Background(), tc.options, tc.winner, 1); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestRenderProducesTheSpin(t *testing.T) {
	if _, err := exec.LookPath(frames.FFmpeg); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	data, err := Render(context.Background(), []string{"Bún bò", "Phở bò tái chín nạm gầu", "Cơm tấm"}, 1, 7)
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
	want := strconv.Itoa(Size) + "," + strconv.Itoa(Size) + "," + strconv.Itoa(frameCount)
	if strings.TrimSpace(string(got)) != want {
		t.Fatalf("stream = %q, want %q", strings.TrimSpace(string(got)), want)
	}
}
