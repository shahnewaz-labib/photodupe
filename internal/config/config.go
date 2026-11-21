// Package config holds runtime settings for photodupe and loads/saves them as
// JSON. Precedence is: explicit CLI flag > data/config.json > built-in defaults.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// DefaultPath is where the UI reads/writes config.
const DefaultPath = "data/config.json"

// Config is the full settings surface. See PLAN §5.4.
type Config struct {
	InputDir  string `json:"input_dir"`
	OutputDir string `json:"output_dir"`
	// RemovedDir is where "clean up approved groups" moves left-out copies.
	// "" => a sibling of InputDir named "<input>-removed".
	RemovedDir string `json:"removed_dir"`

	QdrantAddr  string `json:"qdrant_addr"`   // gRPC addr, see PLAN Phase 4
	ModelPath   string `json:"model_path"`    // CLIP ONNX model
	OnnxLibPath string `json:"onnx_lib_path"` // libonnxruntime.{so,dylib,dll}

	HeifConvertPath string `json:"heif_convert_path"` // "" -> auto-detect

	PHashThreshold int  `json:"phash_threshold"` // Hamming <= N
	PHashEnabled   bool `json:"phash_enabled"`

	ClipSimilarityThresh float32 `json:"clip_similarity_thresh"` // cosine >= X
	ClipEnabled          bool    `json:"clip_enabled"`
	ClipTopK             int     `json:"clip_top_k"`

	KeeperOrder []string `json:"keeper_order"` // e.g. ["resolution","filesize","earliest"]

	ServerAddr string `json:"server_addr"`
}

// StrictnessLevel is a plain-language name for a pHash threshold, shown in the
// UI instead of raw Hamming distances.
type StrictnessLevel struct {
	Name      string `json:"name"`
	Threshold int    `json:"threshold"`
}

// StrictnessLevels are the UI's match levels, strictest first. The pipeline
// caches pairs up to the loosest level so switching levels never re-hashes.
var StrictnessLevels = []StrictnessLevel{
	{Name: "strict", Threshold: 5},
	{Name: "standard", Threshold: 10},
	{Name: "loose", Threshold: 14},
}

// LoosestThreshold is the highest threshold among StrictnessLevels.
func LoosestThreshold() int {
	m := 0
	for _, l := range StrictnessLevels {
		m = max(m, l.Threshold)
	}
	return m
}

// Default returns the built-in defaults. Thresholds are starting points, tuned
// later via the UI (PLAN §2).
func Default() Config {
	return Config{
		QdrantAddr:           "localhost:6334",
		ModelPath:            "models/clip-vit-b32.onnx",
		OnnxLibPath:          "",
		HeifConvertPath:      "",
		PHashThreshold:       10,
		PHashEnabled:         true,
		ClipSimilarityThresh: 0.92,
		ClipEnabled:          false, // Tier B (CLIP) not wired yet; pHash-only release
		ClipTopK:             10,
		KeeperOrder:          []string{"resolution", "filesize", "earliest"},
		ServerAddr:           "localhost:8080",
	}
}

// Load reads config from path, falling back to defaults for any missing file.
// A missing file is not an error (first run); a malformed file is.
func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	// Unmarshal onto defaults so unset JSON fields keep their default value.
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Save writes config to path, creating the parent dir if needed.
func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
