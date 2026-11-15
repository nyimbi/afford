package visual

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"afford/internal/config"
)

func encode(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func solid(width, height int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestCompareIdentical(t *testing.T) {
	img := solid(20, 10, color.White)
	diff, err := Compare(encode(t, img), encode(t, img), 0)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Changed != 0 || diff.Pct != 0 || diff.SizeChanged {
		t.Fatalf("identical images differ: %+v", diff)
	}
}

func TestCompareOnePixel(t *testing.T) {
	base := solid(10, 10, color.White)
	current := solid(10, 10, color.White)
	current.Set(3, 4, color.Black)
	diff, err := Compare(encode(t, base), encode(t, current), 0)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Changed != 1 || diff.Total != 100 {
		t.Fatalf("expected 1/100, got %d/%d", diff.Changed, diff.Total)
	}
	if diff.BBox.Min.X != 3 || diff.BBox.Min.Y != 4 {
		t.Errorf("bbox %v", diff.BBox)
	}
	if len(diff.PNG) == 0 {
		t.Error("no diff image")
	}
}

func TestCompareToleranceAbsorbsSmallChanges(t *testing.T) {
	base := solid(10, 10, color.RGBA{R: 100, G: 100, B: 100, A: 255})
	current := solid(10, 10, color.RGBA{R: 104, G: 100, B: 100, A: 255})
	if diff, _ := Compare(encode(t, base), encode(t, current), 12); diff.Changed != 0 {
		t.Errorf("tolerance 12 should absorb a delta of 4, got %d", diff.Changed)
	}
	if diff, _ := Compare(encode(t, base), encode(t, current), 2); diff.Changed != 100 {
		t.Errorf("tolerance 2 should catch a delta of 4, got %d", diff.Changed)
	}
}

func TestCompareSizeChange(t *testing.T) {
	diff, err := Compare(encode(t, solid(10, 10, color.White)), encode(t, solid(10, 20, color.White)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.SizeChanged || diff.Pct != 100 {
		t.Fatalf("size change not reported: %+v", diff)
	}
}

func TestKeyDistinguishesTemplateAndViewport(t *testing.T) {
	a := Key("/orders/{id}", []int{1280, 900})
	b := Key("/orders/{id}", []int{390, 844})
	c := Key("/users/{id}", []int{1280, 900})
	if a == b || a == c {
		t.Error("keys collide across viewport or template")
	}
}

func TestConsiderSeedsThenCompares(t *testing.T) {
	cfg := config.VisualConfig{ThresholdPct: 0.1, Tolerance: 12}
	store := Store{Dir: filepath.Join(t.TempDir(), "baselines")}
	artifacts := t.TempDir()
	key := Key("/p", []int{100, 100})
	base := encode(t, solid(20, 20, color.White))

	first := Consider(store, cfg, key, base, artifacts)
	if first.Action != "seeded" {
		t.Fatalf("first run action = %q", first.Action)
	}
	if _, err := os.Stat(store.Path(key)); err != nil {
		t.Fatalf("baseline not written: %v", err)
	}

	same := Consider(store, cfg, key, base, artifacts)
	if same.Action != "none" {
		t.Fatalf("identical render action = %q", same.Action)
	}

	changed := solid(20, 20, color.White)
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			changed.Set(x, y, color.Black)
		}
	}
	diff := Consider(store, cfg, key, encode(t, changed), artifacts)
	if diff.Action != "diff" || diff.Pct != 100 {
		t.Fatalf("changed render action = %q pct=%v", diff.Action, diff.Pct)
	}

	cfg.UpdateBaseline = true
	updated := Consider(store, cfg, key, encode(t, changed), artifacts)
	if updated.Action != "updated" {
		t.Fatalf("update action = %q", updated.Action)
	}
}
