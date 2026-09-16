// Command generate_testdata creates a mixed media corpus for manual scans.
// Usage: go run ./scripts/generate_testdata.go [-count 3000] [-out ./testdata]
//
// Layout per media type — groups of 4:
//
//	*-0000.*  unique base
//	*-0001.*  exact byte-copy of base     → exact duplicate group with base
//	*-0002.*  near-dup variant A of base  ┐
//	*-0003.*  near-dup variant B of base  ┘ → similar_* group (after exact pass)
//
// Base content differs per group so near-duplicates do not cross-match.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	count := flag.Int("count", 3000, "total files, approximately")
	out := flag.String("out", "./testdata", "output directory")
	flag.Parse()
	if *count < 40 {
		*count = 40
	}
	if err := os.RemoveAll(*out); err != nil {
		fail(err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}

	images := *count * 40 / 100
	texts := *count * 40 / 100
	videos := *count - images - texts
	if err := makeImages(filepath.Join(*out, "images"), images); err != nil {
		fail(err)
	}
	if err := makeTexts(filepath.Join(*out, "texts"), texts); err != nil {
		fail(err)
	}
	if err := makeVideos(filepath.Join(*out, "videos"), videos); err != nil {
		fail(err)
	}
	fmt.Printf("generated %d files in %s (images=%d texts=%d videos=%d)\n", *count, *out, images, texts, videos)
}

// --- images ---

func makeImages(dir string, n int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	groups := (n + 3) / 4
	idx := 0
	for g := 0; g < groups && idx < n; g++ {
		base := renderBaseImage(g)
		basePath := filepath.Join(dir, fmt.Sprintf("image-%04d.png", idx))
		if err := writePNG(basePath, base); err != nil {
			return err
		}
		idx++
		if idx >= n {
			break
		}
		// Exact copy → SHA-256 exact group with base.
		if err := copyFile(basePath, filepath.Join(dir, fmt.Sprintf("image-%04d.png", idx))); err != nil {
			return err
		}
		idx++
		if idx >= n {
			break
		}
		// Near-dup A: perturb a small block.
		if err := writePNG(filepath.Join(dir, fmt.Sprintf("image-%04d.png", idx)), perturbBlock(base, 88, 148, 0x18, 0x10, 0x08)); err != nil {
			return err
		}
		idx++
		if idx >= n {
			break
		}
		// Near-dup B: different perturbation block → similar to A/base, not identical.
		if err := writePNG(filepath.Join(dir, fmt.Sprintf("image-%04d.png", idx)), perturbBlock(base, 40, 40, 0x0C, 0x14, 0x1C)); err != nil {
			return err
		}
		idx++
	}
	return nil
}

func renderBaseImage(group int) *image.RGBA {
	const w, h = 320, 200
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Strong per-group variation so pHash of different groups stays far apart.
	rBase := (group * 67) % 180
	gBase := (group * 41) % 160
	bBase := (group * 29) % 140
	// Add a group-specific solid stripe so low-frequency structure differs.
	stripeY := 20 + (group*13)%140
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r := (x*200/(w-1) + rBase) % 256
			g := (y*180/(h-1) + gBase) % 256
			b := ((x+y)/3 + bBase) % 256
			if y >= stripeY && y < stripeY+16 {
				r = (r + 90) % 256
				g = (g + 40) % 256
			}
			img.SetRGBA(x, y, color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255})
		}
	}
	return img
}

func perturbBlock(src *image.RGBA, y0, x0 int, dr, dg, db byte) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	b := src.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(x, y, src.At(x, y))
		}
	}
	for y := y0; y < y0+24 && y < b.Max.Y; y++ {
		for x := x0; x < x0+24 && x < b.Max.X; x++ {
			r, g, ch, a := dst.At(x, y).RGBA()
			dst.SetRGBA(x, y, color.RGBA{
				R: byte(r>>8) ^ dr,
				G: byte(g>>8) ^ dg,
				B: byte(ch>>8) ^ db,
				A: byte(a >> 8),
			})
		}
	}
	return dst
}

// --- texts ---

func makeTexts(dir string, n int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	groups := (n + 3) / 4
	idx := 0
	for g := 0; g < groups && idx < n; g++ {
		body := textBody(g)
		write := func(content string) error {
			path := filepath.Join(dir, fmt.Sprintf("note-%04d.txt", idx))
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return err
			}
			idx++
			return nil
		}
		if err := write(body); err != nil {
			return err
		}
		if idx >= n {
			break
		}
		// Exact copy.
		if err := write(body); err != nil {
			return err
		}
		if idx >= n {
			break
		}
		// Near-dup A: one extra sentence.
		if err := write(body + fmt.Sprintf("Variant A marker for corpus group %04d.\n", g)); err != nil {
			return err
		}
		if idx >= n {
			break
		}
		// Near-dup B: different extra sentence → similar to A, not identical.
		if err := write(body + fmt.Sprintf("Variant B note unique to group %04d.\n", g)); err != nil {
			return err
		}
	}
	return nil
}

// textThemes supplies per-group body content so cross-group Jaccard stays
// below the default 0.92 threshold while within-group near-dups stay above it.
var textThemes = []string{
	"Harbor cranes unload containers at dawn while gulls circle the breakwater.",
	"Alpine meadows glow amber as herders move goats along the ridgeline trail.",
	"Circuit boards glitter under fluorescent light in the overnight assembly hall.",
	"Coral reefs shimmer turquoise when noon sun filters through shallow lagoons.",
	"Desert highways stretch empty under a copper sky and distant mesa silhouettes.",
	"Library stacks smell of old paper as readers trace footnotes in quiet rows.",
	"Orbital stations drift silent above blue atmosphere and thin cloud ribbons.",
	"Pottery wheels spin clay into bowls while kilns radiate steady evening heat.",
	"Rainforest canopies drip after storms as howler monkeys call from emergent trees.",
	"Subway platforms blur with motion as late trains hiss into tiled stations.",
	"Tea plantations terrace green hillsides where mist pools in early morning hollows.",
	"Workshop benches hold chisels and gouges beside curling wood shavings.",
}

func textBody(group int) string {
	theme := textThemes[group%len(textThemes)]
	// Unique mid-section per group prevents cross-group feature collision;
	// shared structure within the group keeps near-dup similarity high.
	return fmt.Sprintf(
		"=== Document %04d — media-dedupe text corpus ===\n"+
			"Theme line: %s\n"+
			"Internal marker zeta-%04d ties every file in this logical group together.\n"+
			"The following shared block is identical for exact duplicates in the group.\n"+
			"Shingle windows over this paragraph feed the minhash banding stage.\n"+
			"Stable phrasing keeps feature counts predictable across workers.\n"+
			"Group-local vocabulary %s appears only in this corpus slice.\n"+
			"Near-duplicate variants append a short tail sentence below.\n"+
			"Tail sentence alpha mentions trailing token %s for recall.\n"+
			"Tail sentence beta adds a second trailing token %s for margin.\n"+
			"Closing marker omega-%04d seals the document identity block.\n",
		group, theme, group,
		fmt.Sprintf("qz%dvx", group),
		fmt.Sprintf("ka%drb", group),
		fmt.Sprintf("mw%dtp", group),
		group,
	)
}

// --- videos ---

func makeVideos(dir string, n int) error {
	if n == 0 {
		return nil
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg is required to generate videos: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Distinct lavfi sources keep frame pHash far apart across groups.
	sources := []string{
		"testsrc=size=320x200:rate=10:duration=1.5",
		"smptebars=size=320x200:rate=10:duration=1.5",
		"rgbtestsrc=size=320x200:rate=10:duration=1.5",
		"yuvtestsrc=size=320x200:rate=10:duration=1.5",
	}
	groups := (n + 3) / 4
	idx := 0
	for g := 0; g < groups && idx < n; g++ {
		basePath := filepath.Join(dir, fmt.Sprintf("video-%04d.mp4", idx))
		src := sources[g%len(sources)]
		// Layer a group-tinted box on top so even same-source groups differ slightly.
		vf := fmt.Sprintf("drawbox=x=%d:y=%d:w=100:h=70:color=0x%06x@0.85:t=fill",
			15+(g*23)%180, 15+(g*17)%100, (g*7919)%0xFFFFFF)
		if err := runFFmpeg(
			"-y", "-loglevel", "error",
			"-f", "lavfi",
			"-i", src,
			"-vf", vf,
			"-pix_fmt", "yuv420p",
			"-c:v", "libx264",
			basePath,
		); err != nil {
			return err
		}
		idx++
		if idx >= n {
			break
		}
		// Exact copy.
		if err := copyFile(basePath, filepath.Join(dir, fmt.Sprintf("video-%04d.mp4", idx))); err != nil {
			return err
		}
		idx++
		if idx >= n {
			break
		}
		// Near-dup A: re-encode CRF 28.
		if err := reencode(basePath, filepath.Join(dir, fmt.Sprintf("video-%04d.mp4", idx)), "28"); err != nil {
			return err
		}
		idx++
		if idx >= n {
			break
		}
		// Near-dup B: re-encode CRF 32 → different bytes, same visual family.
		if err := reencode(basePath, filepath.Join(dir, fmt.Sprintf("video-%04d.mp4", idx)), "32"); err != nil {
			return err
		}
		idx++
	}
	return nil
}

func reencode(src, dst, crf string) error {
	return runFFmpeg(
		"-y", "-loglevel", "error",
		"-i", src,
		"-c:v", "libx264",
		"-crf", crf,
		"-preset", "veryslow",
		"-pix_fmt", "yuv420p",
		dst,
	)
}

func runFFmpeg(args ...string) error {
	cmd := exec.Command("ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %s: %w", out, err)
	}
	return nil
}

// --- helpers ---

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
