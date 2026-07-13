package review

import (
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/image/draw"

	_ "image/gif"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// ThumbDir caches downscaled review thumbnails.
const ThumbDir = "data/thumbs"

const thumbMax = 256

// thumbFor returns the cached thumbnail path for id, generating it from src on
// first request. Failures fall back to the original by returning src.
func thumbFor(id uint64, src string) string {
	dst := filepath.Join(ThumbDir, strconv.FormatUint(id, 10)+".jpg")
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		return dst
	}
	if err := makeThumb(src, dst); err != nil {
		return src // serve full image rather than fail
	}
	return dst
}

func makeThumb(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return err
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := 1.0
	if w > h && w > thumbMax {
		scale = float64(thumbMax) / float64(w)
	} else if h >= w && h > thumbMax {
		scale = float64(thumbMax) / float64(h)
	}
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}

	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(out, out.Bounds(), img, b, draw.Over, nil)

	if err := os.MkdirAll(ThumbDir, 0o755); err != nil {
		return err
	}
	of, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer of.Close()
	return jpeg.Encode(of, out, &jpeg.Options{Quality: 82})
}
