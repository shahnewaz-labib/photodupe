// Package scanner walks an input directory and produces ImageRecords: one per
// image file, with best-effort capture time, dimensions, and (for HEIC) a
// converted working copy. See PLAN §6 Phase 1.
package scanner

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"image"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rwcarlsen/goexif/exif"

	// Decoders registered for image.DecodeConfig / image.Decode.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	"github.com/shahnewaz-labib/photodupe/internal/config"
)

// HeicCacheDir holds JPEGs converted from HEIC inputs.
const HeicCacheDir = "data/heic_cache"

// ImageRecord describes one scanned image. See PLAN §5.1.
type ImageRecord struct {
	ID          uint64    `json:"id"`
	AbsPath     string    `json:"abs_path"`
	WorkPath    string    `json:"work_path"`
	Filename    string    `json:"filename"`
	CaptureTime time.Time `json:"capture_time"`
	FileSize    int64     `json:"file_size"`
	ModTime     time.Time `json:"mod_time"` // file mtime; with FileSize, decides if a cached hash is still valid
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	IsHEIC      bool      `json:"is_heic"`
}

// Report summarizes a scan.
type Report struct {
	Found       int `json:"found"`
	SkippedHEIC int `json:"skipped_heic"`
	Errors      int `json:"errors"`
}

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".bmp": true, ".heic": true, ".heif": true,
}

// IsImageExt reports whether ext (with dot, any case) is a supported image type.
func IsImageExt(ext string) bool { return imageExts[strings.ToLower(ext)] }

// IsHEICExt reports whether ext (with dot, any case) is HEIC/HEIF.
func IsHEICExt(ext string) bool { return isHEIC(strings.ToLower(ext)) }

func isHEIC(ext string) bool { return ext == ".heic" || ext == ".heif" }

// idFor returns a stable point ID for a path (FNV-1a 64), used as the Qdrant
// point ID so re-runs upsert rather than duplicate.
func idFor(absPath string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(absPath))
	return h.Sum64()
}

// ScanDir walks cfg.InputDir and returns image records + a report. onProgress,
// if non-nil, is called as files are processed (done, total). It never returns a
// partial-crash: unreadable files and HEIC-without-converter are counted and
// skipped, not fatal.
func ScanDir(cfg config.Config, onProgress func(done, total int)) ([]ImageRecord, Report, error) {
	root, err := filepath.Abs(cfg.InputDir)
	if err != nil {
		return nil, Report{}, fmt.Errorf("resolve input dir: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, Report{}, fmt.Errorf("input dir: %w", err)
	}
	if !info.IsDir() {
		return nil, Report{}, fmt.Errorf("input dir %q is not a directory", root)
	}

	// Pass 1: collect candidate image paths (cheap) so progress has a total.
	var paths []string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			slog.Warn("walk", "path", p, "err", err)
			return nil // skip unreadable entries, keep going
		}
		if d.IsDir() {
			return nil
		}
		if imageExts[strings.ToLower(filepath.Ext(p))] {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, Report{}, fmt.Errorf("walk: %w", err)
	}

	heifConvert := resolveHeifConvert(cfg)
	if heifConvert == "" {
		slog.Warn("heif-convert not found; HEIC/HEIF files will be skipped (install libheif)")
	} else if err := os.MkdirAll(HeicCacheDir, 0o755); err != nil {
		return nil, Report{}, fmt.Errorf("heic cache dir: %w", err)
	}

	records := make([]ImageRecord, 0, len(paths))
	var rep Report
	for i, p := range paths {
		if onProgress != nil {
			onProgress(i, len(paths))
		}
		rec, err := buildRecord(p, heifConvert)
		if err != nil {
			if err == errNoHeifConverter {
				rep.SkippedHEIC++
			} else {
				slog.Warn("scan file", "path", p, "err", err)
				rep.Errors++
			}
			continue
		}
		records = append(records, rec)
	}
	if onProgress != nil {
		onProgress(len(paths), len(paths))
	}
	rep.Found = len(records)
	return records, rep, nil
}

var errNoHeifConverter = fmt.Errorf("no heif converter")

func buildRecord(path, heifConvert string) (ImageRecord, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return ImageRecord{}, err
	}
	fi, err := os.Stat(absPath)
	if err != nil {
		return ImageRecord{}, err
	}

	rec := ImageRecord{
		ID:       idFor(absPath),
		AbsPath:  absPath,
		WorkPath: absPath,
		Filename: filepath.Base(absPath),
		FileSize: fi.Size(),
		ModTime:  fi.ModTime(),
		IsHEIC:   isHEIC(strings.ToLower(filepath.Ext(absPath))),
	}

	if rec.IsHEIC {
		if heifConvert == "" {
			return ImageRecord{}, errNoHeifConverter
		}
		work, err := convertHEIC(heifConvert, absPath, rec.ID)
		if err != nil {
			return ImageRecord{}, fmt.Errorf("heic convert: %w", err)
		}
		rec.WorkPath = work
	}

	// Dimensions from the working image (converted JPEG for HEIC).
	if w, h, err := dimensions(rec.WorkPath); err == nil {
		rec.Width, rec.Height = w, h
	} else {
		slog.Warn("dimensions", "path", rec.WorkPath, "err", err)
	}

	rec.CaptureTime = captureTime(absPath, fi.ModTime())
	return rec, nil
}

func dimensions(path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

// captureTime resolves best-effort capture time: sidecar JSON > EXIF > mtime.
func captureTime(absPath string, mtime time.Time) time.Time {
	if t, ok := sidecarTime(absPath); ok {
		return t
	}
	if t, ok := exifTime(absPath); ok {
		return t
	}
	return mtime
}

// sidecar is the subset of the Google Takeout JSON we read. Sidecars are
// optional; absence is normal (PLAN §6 Phase 1).
type sidecar struct {
	PhotoTakenTime struct {
		Timestamp string `json:"timestamp"` // unix seconds, as string
	} `json:"photoTakenTime"`
}

func sidecarTime(absPath string) (time.Time, bool) {
	candidates := []string{
		absPath + ".json",
		absPath + ".supplemental-metadata.json",
	}
	for _, c := range candidates {
		b, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		var s sidecar
		if json.Unmarshal(b, &s) != nil {
			continue
		}
		secs, err := strconv.ParseInt(s.PhotoTakenTime.Timestamp, 10, 64)
		if err != nil || secs <= 0 {
			continue
		}
		return time.Unix(secs, 0), true
	}
	return time.Time{}, false
}

func exifTime(absPath string) (time.Time, bool) {
	f, err := os.Open(absPath)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	x, err := exif.Decode(f)
	if err != nil {
		return time.Time{}, false
	}
	t, err := x.DateTime()
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func resolveHeifConvert(cfg config.Config) string {
	if cfg.HeifConvertPath != "" {
		return cfg.HeifConvertPath
	}
	if p, err := exec.LookPath("heif-convert"); err == nil {
		return p
	}
	return ""
}

// convertHEIC converts src to a cached JPEG keyed by id. It skips work if the
// cache file already exists (idempotent).
func convertHEIC(heifConvert, src string, id uint64) (string, error) {
	dst := filepath.Join(HeicCacheDir, fmt.Sprintf("%d.jpg", id))
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		return dst, nil
	}
	// heif-convert <input> <output>
	out, err := exec.Command(heifConvert, src, dst).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return dst, nil
}
