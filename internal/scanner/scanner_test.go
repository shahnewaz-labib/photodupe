package scanner

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/shahnewaz-labib/photodupe/internal/config"
)

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func writeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
}

func TestScanDir(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "a.png"), 100, 60)
	writeJPEG(t, filepath.Join(dir, "b.jpg"), 40, 40)
	// nested dir + a non-image file that must be ignored
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(sub, "c.png"), 10, 10)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.InputDir = dir
	recs, rep, err := ScanDir(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Found != 3 {
		t.Fatalf("found = %d, want 3", rep.Found)
	}

	// dimensions read for the png
	var a *ImageRecord
	for i := range recs {
		if recs[i].Filename == "a.png" {
			a = &recs[i]
		}
	}
	if a == nil {
		t.Fatal("a.png not found in records")
	}
	if a.Width != 100 || a.Height != 60 {
		t.Fatalf("a.png dims = %dx%d, want 100x60", a.Width, a.Height)
	}
	if a.WorkPath != a.AbsPath {
		t.Fatalf("non-heic WorkPath should equal AbsPath")
	}
}

func TestSidecarTimeWins(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "photo.png")
	writePNG(t, img, 20, 20)

	// mtime far from the sidecar time
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(img, old, old); err != nil {
		t.Fatal(err)
	}

	want := time.Date(2021, 6, 15, 12, 0, 0, 0, time.UTC)
	sidecar := `{"photoTakenTime":{"timestamp":"` +
		strconv.FormatInt(want.Unix(), 10) + `"}}`
	if err := os.WriteFile(img+".json", []byte(sidecar), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.InputDir = dir
	recs, _, err := ScanDir(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if !recs[0].CaptureTime.Equal(want) {
		t.Fatalf("CaptureTime = %v, want sidecar %v", recs[0].CaptureTime, want)
	}
}
