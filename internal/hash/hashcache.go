package hash

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

// HashCachePath stores each photo's perceptual hash so a re-scan only decodes
// new or changed files.
const HashCachePath = "data/phash_cache.json"

// HashEntry is a cached hash plus the file facts it was computed from.
type HashEntry struct {
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"` // UnixNano
	Hash    uint64 `json:"hash,string"`
}

// HashCache maps record ID (a hash of the path) to its cached hash.
type HashCache map[uint64]HashEntry

func entryFor(rec scanner.ImageRecord, h uint64) HashEntry {
	return HashEntry{Size: rec.FileSize, ModTime: rec.ModTime.UnixNano(), Hash: h}
}

// matches reports whether e was computed from the file rec describes now.
func (e HashEntry) matches(rec scanner.ImageRecord) bool {
	return !rec.ModTime.IsZero() && e.Size == rec.FileSize && e.ModTime == rec.ModTime.UnixNano()
}

// LoadHashCache reads the cache. A missing or unreadable cache is just empty:
// everything gets re-hashed.
func LoadHashCache(path string) HashCache {
	b, err := os.ReadFile(path)
	if err != nil {
		return HashCache{}
	}
	var c HashCache
	if json.Unmarshal(b, &c) != nil {
		return HashCache{}
	}
	return c
}

// SaveHashCache writes the cache, creating the parent dir if needed.
func SaveHashCache(path string, c HashCache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
