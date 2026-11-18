// Package visual stores reference screenshots and compares new renders to them.
//
// It is deliberately simple: decode both PNGs, compare pixels with a per-channel
// tolerance, and keep the diff image and bounding box. No SSIM, no perceptual
// hashing — a pixel diff with a threshold is explainable, and an explainable
// gate is one people keep.
package visual

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"

	"github.com/nyimbi/afford/internal/config"
)

// Key identifies a baseline: the route template plus the viewport. Instances of
// a template share a baseline, which is what makes detail pages comparable.
func Key(template string, viewport []int) string {
	width, height := 0, 0
	if len(viewport) == 2 {
		width, height = viewport[0], viewport[1]
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%dx%d", template, width, height)))
	return hex.EncodeToString(sum[:])[:20]
}

type Store struct {
	Dir string
}

func (s Store) Path(key string) string {
	return filepath.Join(s.Dir, key+".png")
}

func (s Store) Load(key string) ([]byte, bool) {
	blob, err := os.ReadFile(s.Path(key))
	return blob, err == nil
}

func (s Store) Save(key string, blob []byte) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.Path(key), blob, 0o644)
}

type Diff struct {
	SizeChanged bool
	Changed     int
	Total       int
	Pct         float64
	BBox        image.Rectangle
	PNG         []byte
}

// Compare diffs two PNGs. A size change is always a difference: the page
// reflowed, which is a regression or a deliberate redesign, never noise.
func Compare(baselinePNG, currentPNG []byte, tolerance int) (Diff, error) {
	baseline, err := png.Decode(bytes.NewReader(baselinePNG))
	if err != nil {
		return Diff{}, fmt.Errorf("decode baseline: %w", err)
	}
	current, err := png.Decode(bytes.NewReader(currentPNG))
	if err != nil {
		return Diff{}, fmt.Errorf("decode current: %w", err)
	}
	bb, cb := baseline.Bounds(), current.Bounds()
	if bb.Dx() != cb.Dx() || bb.Dy() != cb.Dy() {
		return sizeChanged(current), nil
	}
	width, height := cb.Dx(), cb.Dy()
	if tolerance < 0 {
		tolerance = 0
	}
	diff := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(diff, diff.Bounds(), current, cb.Min, draw.Src)

	changed := 0
	var box image.Rectangle
	first := true
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			br, bg, bblue, _ := baseline.At(bb.Min.X+x, bb.Min.Y+y).RGBA()
			cr, cg, cblue, _ := current.At(cb.Min.X+x, cb.Min.Y+y).RGBA()
			if channelDelta(br, cr, bg, cg, bblue, cblue) <= tolerance {
				continue
			}
			changed++
			diff.Set(x, y, color.RGBA{R: 0xff, A: 0xff})
			pixel := image.Rect(x, y, x+1, y+1)
			if first {
				box, first = pixel, false
			} else {
				box = box.Union(pixel)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, diff); err != nil {
		return Diff{}, err
	}
	total := width * height
	pct := 0.0
	if total > 0 {
		pct = 100 * float64(changed) / float64(total)
	}
	return Diff{Changed: changed, Total: total, Pct: pct, BBox: box, PNG: buf.Bytes()}, nil
}

func sizeChanged(current image.Image) Diff {
	bounds := current.Bounds()
	out := image.NewRGBA(bounds)
	draw.Draw(out, bounds, current, bounds.Min, draw.Src)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := out.At(x, y).RGBA()
			out.Set(x, y, color.RGBA{
				R: uint8((int(r>>8) + 0xff) / 2),
				G: uint8(int(g>>8) / 2),
				B: uint8(int(b>>8) / 2),
				A: uint8(a >> 8),
			})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, out)
	total := bounds.Dx() * bounds.Dy()
	return Diff{
		SizeChanged: true, Changed: total, Total: total, Pct: 100,
		BBox: bounds, PNG: buf.Bytes(),
	}
}

func channelDelta(r1, r2, g1, g2, b1, b2 uint32) int {
	delta := func(a, b uint32) int {
		d := int(a>>8) - int(b>>8)
		if d < 0 {
			return -d
		}
		return d
	}
	worst := delta(r1, r2)
	if d := delta(g1, g2); d > worst {
		worst = d
	}
	if d := delta(b1, b2); d > worst {
		worst = d
	}
	return worst
}

// Outcome is what one page's comparison did.
type Outcome struct {
	Action      string // seeded | updated | diff | none | error
	Key         string
	Pct         float64
	Changed     int
	Total       int
	SizeChanged bool
	Baseline    string
	Current     string
	Diff        string
	Error       string
}

// Consider compares a fresh screenshot with the stored baseline and writes the
// three artifacts a human needs to adjudicate the result.
func Consider(store Store, cfg config.VisualConfig, key string, currentPNG []byte, artifactDir string) Outcome {
	out := Outcome{Key: key}
	baseline, exists := store.Load(key)
	if !exists || cfg.UpdateBaseline {
		if err := store.Save(key, currentPNG); err != nil {
			out.Action, out.Error = "error", err.Error()
			return out
		}
		out.Action = "seeded"
		if exists {
			out.Action = "updated"
		}
		out.Baseline = writeArtifact(artifactDir, key+"-baseline.png", currentPNG)
		out.Current = writeArtifact(artifactDir, key+"-current.png", currentPNG)
		return out
	}
	diff, err := Compare(baseline, currentPNG, cfg.Tolerance)
	if err != nil {
		out.Action, out.Error = "error", err.Error()
		return out
	}
	out.Pct = diff.Pct
	out.Changed = diff.Changed
	out.Total = diff.Total
	out.SizeChanged = diff.SizeChanged
	out.Baseline = writeArtifact(artifactDir, key+"-baseline.png", baseline)
	out.Current = writeArtifact(artifactDir, key+"-current.png", currentPNG)
	out.Diff = writeArtifact(artifactDir, key+"-diff.png", diff.PNG)
	out.Action = "none"
	overPixels := cfg.MaxDiffPixels > 0 && diff.Changed > cfg.MaxDiffPixels
	if diff.SizeChanged || diff.Pct > cfg.ThresholdPct || overPixels {
		out.Action = "diff"
	}
	return out
}

func writeArtifact(dir, name string, blob []byte) string {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		return ""
	}
	return path
}
