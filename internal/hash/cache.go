package hash

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// CachePath is the default Tier A pairs cache location.
const CachePath = "data/tier_a.json"

// SavePairs writes pairs as JSON, creating the parent dir if needed.
func SavePairs(path string, pairs []Pair) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(pairs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// LoadPairs reads pairs previously written by SavePairs. A missing file yields
// an empty slice, not an error (Tier A may be disabled).
func LoadPairs(path string) ([]Pair, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pairs []Pair
	if err := json.Unmarshal(b, &pairs); err != nil {
		return nil, err
	}
	return pairs, nil
}
