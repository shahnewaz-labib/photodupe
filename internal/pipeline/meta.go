package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shahnewaz-labib/photodupe/internal/config"
)

// MetaPath records the parameters a run's caches were generated with, so the UI
// can tell when a threshold change needs a full re-run vs a cheap re-cluster.
const MetaPath = "data/run_meta.json"

// RunMeta captures the thresholds used to generate the cached pair files. The
// pHash cache only contains pairs at or below PHashThreshold, so lowering the
// threshold below this value (looser) requires re-hashing.
type RunMeta struct {
	PHashThreshold int `json:"phash_threshold"`
}

// SaveMeta records the run parameters.
func SaveMeta(cfg config.Config) error {
	if err := os.MkdirAll(filepath.Dir(MetaPath), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(RunMeta{PHashThreshold: cfg.PHashThreshold}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(MetaPath, b, 0o644)
}

// LoadMeta reads the last run's parameters. Missing => zero value + false.
func LoadMeta() (RunMeta, bool) {
	b, err := os.ReadFile(MetaPath)
	if err != nil {
		return RunMeta{}, false
	}
	var m RunMeta
	if json.Unmarshal(b, &m) != nil {
		return RunMeta{}, false
	}
	return m, true
}

// LooseningWarning returns a non-empty message when cfg's pHash threshold is
// looser (higher) than the cached run's, meaning a re-cluster alone can't
// surface the extra pairs — a full re-run is needed. See PLAN §7.5.
func LooseningWarning(cfg config.Config) string {
	m, ok := LoadMeta()
	if !ok {
		return ""
	}
	if cfg.PHashEnabled && cfg.PHashThreshold > m.PHashThreshold {
		return fmt.Sprintf(
			"pHash threshold raised from %d to %d. Re-cluster only re-filters existing pairs; "+
				"run a full dedup to find the newly-eligible looser matches.",
			m.PHashThreshold, cfg.PHashThreshold)
	}
	return ""
}
