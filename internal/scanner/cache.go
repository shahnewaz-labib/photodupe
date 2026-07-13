package scanner

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// CachePath is the default scan cache location.
const CachePath = "data/scan_cache.json"

// SaveCache writes records as JSON, creating the parent dir if needed.
func SaveCache(path string, recs []ImageRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// LoadCache reads records previously written by SaveCache.
func LoadCache(path string) ([]ImageRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var recs []ImageRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil, err
	}
	return recs, nil
}
