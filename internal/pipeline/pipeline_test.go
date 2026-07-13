package pipeline

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/shahnewaz-labib/photodupe/internal/cluster"
	"github.com/shahnewaz-labib/photodupe/internal/config"
)

// blocks makes high-contrast structured content so perception hashing is stable.
func blocks(w, h int, invert bool) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	vals := []uint8{20, 110, 180, 240}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			q := 0
			if x >= w/2 {
				q++
			}
			if y >= h/2 {
				q += 2
			}
			v := vals[q]
			d := x - y
			if d < 0 {
				d = -d
			}
			if d < w/16 {
				v = 255
			}
			if invert {
				v = 255 - v
			}
			img.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	return img
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func writeJPEG(t *testing.T, path string, img image.Image, q int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
}

// TestRunAllPHash exercises the full pHash pipeline (scan -> hash -> cluster) end
// to end, isolated in a temp working dir so cache files don't touch the repo.
func TestRunAllPHash(t *testing.T) {
	tmp := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(prev)

	in := filepath.Join(tmp, "in")
	if err := os.MkdirAll(in, 0o755); err != nil {
		t.Fatal(err)
	}
	base := blocks(256, 256, false)
	writePNG(t, filepath.Join(in, "orig.png"), base)        // keeper (dup group)
	writeJPEG(t, filepath.Join(in, "copy.jpg"), base, 70)   // near-exact dup of orig
	writePNG(t, filepath.Join(in, "other.png"), blocks(256, 256, true)) // unique

	cfg := config.Default()
	cfg.InputDir = in
	cfg.PHashEnabled = true

	if err := RunAll(cfg, nil, NewProgress()); err != nil {
		t.Fatalf("RunAll: %v", err)
	}

	clusters, err := cluster.LoadClusters(cluster.CachePath)
	if err != nil {
		t.Fatalf("load clusters: %v", err)
	}
	if len(clusters) != 1 {
		t.Fatalf("got %d clusters, want 1: %+v", len(clusters), clusters)
	}
	if len(clusters[0].MemberIDs) != 2 {
		t.Fatalf("cluster size = %d, want 2 (orig + copy)", len(clusters[0].MemberIDs))
	}
}
