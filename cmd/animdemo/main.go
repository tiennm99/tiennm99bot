// Command animdemo renders sample clips of the animations the bot draws in
// process (/wheelofnames, /genshin, /gacha, /csgo), each with a contact sheet, so a
// change can be reviewed by eye. It is a development tool, not part of the
// deployed bot; scripts/demo.sh wraps it and compares against a git ref.
//
//	go run ./cmd/animdemo [-out .tmp/demo/current] [-only wheel,genshin,gacha,csgo] [-seed 1]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules/random/cardpack"
	"github.com/tiennm99/tiennm99bot/internal/modules/random/caseopen"
	"github.com/tiennm99/tiennm99bot/internal/modules/random/genshin"
	"github.com/tiennm99/tiennm99bot/internal/modules/random/wheel"
)

// demo is one sample clip.
type demo struct {
	kind   string // wheel, genshin, gacha, or csgo
	name   string
	render func(ctx context.Context, seed uint64) ([]byte, error)
}

var caseItems = []caseopen.Item{
	{Label: "Cơm tấm", Rarity: 3},
	{Label: "Phở bò tái chín", Rarity: 4},
	{Label: "Bún bò", Rarity: 5},
	{Label: "Bánh mì", Rarity: 3},
}

func demos() []demo {
	wheelDemo := func(name string, winner int, options ...string) demo {
		return demo{"wheel", name, func(ctx context.Context, seed uint64) ([]byte, error) {
			return wheel.Render(ctx, options, winner, seed)
		}}
	}
	many := make([]string, wheel.MaxOptions)
	for i := range many {
		many[i] = fmt.Sprintf("Option %d", i+1)
	}
	out := []demo{
		wheelDemo("wheel-2", 0, "Yes", "No"),
		wheelDemo("wheel-6-vi", 2, "Bún bò", "Phở bò tái chín", "Cơm tấm", "Bánh mì", "Hủ tiếu Nam Vang", "Bún chả"),
		wheelDemo("wheel-12", 5, "alice", "bob", "carol", "dave", "erin", "frank", "grace", "heidi", "ivan", "judy", "mallory", "oscar"),
		wheelDemo(fmt.Sprintf("wheel-%d", wheel.MaxOptions), 7, many...),
	}
	for _, rarity := range []int{3, 4, 5} {
		label := map[int]string{3: "Cơm tấm", 4: "Phở bò tái chín", 5: "Bún bò"}[rarity]
		out = append(out, demo{"genshin", fmt.Sprintf("genshin-%d", rarity), func(ctx context.Context, _ uint64) ([]byte, error) {
			return genshin.Render(ctx, label, rarity)
		}})
		out = append(out, demo{"gacha", fmt.Sprintf("gacha-%d", rarity), func(ctx context.Context, seed uint64) ([]byte, error) {
			return cardpack.Render(ctx, label, rarity, seed)
		}})
		winner := slices.IndexFunc(caseItems, func(item caseopen.Item) bool { return item.Rarity == rarity })
		out = append(out, demo{"csgo", fmt.Sprintf("csgo-%d", rarity), func(ctx context.Context, seed uint64) ([]byte, error) {
			return caseopen.Render(ctx, caseItems, winner, seed)
		}})
	}
	return out
}

func main() {
	outDir := flag.String("out", filepath.Join(".tmp", "demo", "current"), "folder for the clips and contact sheets")
	only := flag.String("only", "", "comma-separated kinds to render: wheel, genshin, gacha, csgo (default all)")
	seed := flag.Uint64("seed", 1, "seed for the random parts, so two runs are comparable")
	flag.Parse()

	kinds := map[string]bool{}
	for _, kind := range strings.Split(*only, ",") {
		if kind = strings.TrimSpace(kind); kind != "" {
			kinds[kind] = true
		}
	}
	if err := os.MkdirAll(*outDir, 0o750); err != nil {
		fail(err)
	}

	ctx := context.Background()
	rendered := 0
	for _, d := range demos() {
		if len(kinds) > 0 && !kinds[d.kind] {
			continue
		}
		start := time.Now()
		data, err := d.render(ctx, *seed)
		if err != nil {
			fail(fmt.Errorf("%s: %w", d.name, err))
		}
		clip := filepath.Join(*outDir, d.name+".mp4")
		if err := os.WriteFile(clip, data, 0o600); err != nil {
			fail(err)
		}
		if err := contactSheet(clip, filepath.Join(*outDir, d.name+".jpg")); err != nil {
			fail(fmt.Errorf("%s: contact sheet: %w", d.name, err))
		}
		fmt.Printf("%-12s %6.2fs  %s\n", d.name, time.Since(start).Seconds(), clip)
		rendered++
	}
	if rendered == 0 {
		fail(fmt.Errorf("-only %q matches no demo; use wheel, genshin, gacha, or csgo", *only))
	}
}

// contactSheet tiles six evenly spaced frames of the 7-second clip.
func contactSheet(clip, sheet string) error {
	// #nosec G204 — the arguments are this command's own paths and constants.
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", clip,
		"-vf", `select='not(mod(n\,35))',scale=320:-2,tile=3x2`, "-frames:v", "1", "-q:v", "4", sheet)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "animdemo:", err)
	os.Exit(1)
}
