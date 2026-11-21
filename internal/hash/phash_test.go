package hash

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

// blocks produces high-contrast structured content so perception hashing is
// stable. Smooth gradients are pathological for DCT pHash (too many coefficients
// near the median threshold), so use distinct quadrants + a diagonal band.
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
			if absInt(x-y) < w/16 {
				v = 255 // diagonal band
			}
			if invert {
				v = 255 - v
			}
			img.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	return img
}

func absInt(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func savePNG(t *testing.T, dir, name string, img image.Image) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

func saveJPEG(t *testing.T, dir, name string, img image.Image, q int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
	return p
}

func rec(id uint64, path string) scanner.ImageRecord {
	return scanner.ImageRecord{ID: id, AbsPath: path, WorkPath: path}
}

func TestFindPairs(t *testing.T) {
	dir := t.TempDir()

	base := blocks(256, 256, false)
	pOrig := savePNG(t, dir, "orig.png", base)
	// same content, re-encoded as lossy JPEG -> near-exact duplicate
	pCopy := saveJPEG(t, dir, "copy.jpg", base, 80)
	// clearly different content
	pDiff := savePNG(t, dir, "diff.png", blocks(256, 256, true))

	recs := []scanner.ImageRecord{
		rec(1, pOrig),
		rec(2, pCopy),
		rec(3, pDiff),
	}

	pairs := FindPairs(recs, 10, nil)

	// orig<->copy must be a pair; nothing involving diff should be.
	foundCopy := false
	for _, p := range pairs {
		if (p.AID == 1 && p.BID == 2) || (p.AID == 2 && p.BID == 1) {
			foundCopy = true
		}
		if p.AID == 3 || p.BID == 3 {
			t.Fatalf("distinct image 3 should not pair: %+v", p)
		}
	}
	if !foundCopy {
		t.Fatalf("expected orig<->copy pair, got %+v", pairs)
	}
}

// A cached hash is reused while the file's size and mtime match, and
// recomputed once either changes.
func TestFindPairsCachedReusesHashes(t *testing.T) {
	dir := t.TempDir()
	base := blocks(256, 256, false)
	a := rec(1, savePNG(t, dir, "a.png", base))
	b := rec(2, saveJPEG(t, dir, "b.jpg", base, 80))
	for _, r := range []*scanner.ImageRecord{&a, &b} {
		fi, err := os.Stat(r.WorkPath)
		if err != nil {
			t.Fatal(err)
		}
		r.FileSize, r.ModTime = fi.Size(), fi.ModTime()
	}
	recs := []scanner.ImageRecord{a, b}

	pairs, cache := FindPairsCached(recs, 10, nil, nil)
	if len(pairs) != 1 || len(cache) != 2 {
		t.Fatalf("first run: pairs %+v, cache %d entries", pairs, len(cache))
	}

	// Poison b's cached hash: if it is reused, a and b stop matching.
	poisoned := HashCache{1: cache[1], 2: cache[2]}
	e := poisoned[2]
	e.Hash = ^e.Hash
	poisoned[2] = e
	stale := HashCache{1: cache[1], 2: cache[2], 99: {Hash: 7}}
	if pairs, _ := FindPairsCached(recs, 10, poisoned, nil); len(pairs) != 0 {
		t.Fatalf("cached hash was not reused: %+v", pairs)
	}
	if _, fresh := FindPairsCached(recs, 10, stale, nil); len(fresh) != 2 {
		t.Errorf("stale entry kept: %+v", fresh)
	}

	// Same poisoned cache, but b's file changed: it must be re-hashed.
	recs[1].ModTime = recs[1].ModTime.Add(time.Second)
	if pairs, _ := FindPairsCached(recs, 10, poisoned, nil); len(pairs) != 1 {
		t.Fatalf("changed file was not re-hashed: %+v", pairs)
	}

	// Round-trips through disk.
	p := filepath.Join(dir, "cache.json")
	if err := SaveHashCache(p, cache); err != nil {
		t.Fatal(err)
	}
	if got := LoadHashCache(p); len(got) != 2 || got[2] != cache[2] {
		t.Errorf("loaded cache %+v, want %+v", got, cache)
	}
}
