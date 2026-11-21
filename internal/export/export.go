// Package export copies keepers + uniques into the output directory. It never
// modifies, moves, or deletes source files. See PLAN §6b.
package export

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shahnewaz-labib/photodupe/internal/cluster"
	"github.com/shahnewaz-labib/photodupe/internal/config"
	"github.com/shahnewaz-labib/photodupe/internal/keeper"
	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

// Report summarizes an export.
type Report struct {
	Copied     int      `json:"copied"`
	Skipped    int      `json:"skipped"` // rejected duplicates, not copied
	Collisions int      `json:"collisions"`
	Errors     []string `json:"errors"`
}

// Export copies the keeper of each cluster plus every unique image into
// cfg.OutputDir. decisions may be nil (all clusters use the heuristic default).
// onProgress, if non-nil, is called as files are copied.
func Export(cfg config.Config, records []scanner.ImageRecord, clusters []cluster.Cluster,
	decisions map[string]cluster.ClusterDecision, onProgress func(done, total int)) (Report, error) {

	if err := ValidateDirs(cfg.InputDir, cfg.OutputDir); err != nil {
		return Report{}, err
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return Report{}, fmt.Errorf("create output dir: %w", err)
	}

	keeperIDs := KeeperSet(cfg, records, clusters, decisions)

	// Copy keepers.
	used := map[string]bool{}
	var rep Report
	total := len(keeperIDs)
	done := 0
	for _, r := range records {
		if !keeperIDs[r.ID] {
			rep.Skipped++
			continue
		}
		dst, collided := uniqueDest(cfg.OutputDir, r.Filename, used)
		if collided {
			rep.Collisions++
		}
		if err := copyFile(r.AbsPath, dst); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %v", r.AbsPath, err))
		} else {
			rep.Copied++
		}
		done++
		if onProgress != nil {
			onProgress(done, total)
		}
	}
	return rep, nil
}

// KeeperSet returns the IDs export would copy: each cluster's keeper (the
// user's choice, else the heuristic), every member of a cluster marked "not
// duplicates", and every image in no cluster. It touches no files, so the UI
// uses it to preview an export.
func KeeperSet(cfg config.Config, records []scanner.ImageRecord, clusters []cluster.Cluster,
	decisions map[string]cluster.ClusterDecision) map[uint64]bool {

	byID := make(map[uint64]scanner.ImageRecord, len(records))
	for _, r := range records {
		byID[r.ID] = r
	}

	keeperIDs := map[uint64]bool{}
	clustered := map[uint64]bool{}

	for _, c := range clusters {
		members := make([]scanner.ImageRecord, 0, len(c.MemberIDs))
		for _, id := range c.MemberIDs {
			clustered[id] = true
			if r, ok := byID[id]; ok {
				members = append(members, r)
			}
		}
		dec := decisions[c.ID]
		if dec.Skipped {
			// "not actually duplicates" => keep all members.
			for _, id := range c.MemberIDs {
				keeperIDs[id] = true
			}
			continue
		}
		if dec.KeeperID != 0 && contains(c.MemberIDs, dec.KeeperID) {
			keeperIDs[dec.KeeperID] = true
		} else if len(members) > 0 {
			keeperIDs[keeper.SuggestKeeper(members, cfg.KeeperOrder).ID] = true
		}
	}

	// Every image not in any cluster is a unique => always kept.
	for _, r := range records {
		if !clustered[r.ID] {
			keeperIDs[r.ID] = true
		}
	}
	return keeperIDs
}

// ValidateDirs enforces the safety guardrails (PLAN §6b step 4): output must be
// set, must differ from input, and must not be an ancestor of input.
func ValidateDirs(inputDir, outputDir string) error {
	if strings.TrimSpace(outputDir) == "" {
		return fmt.Errorf("output dir is empty")
	}
	in, err := filepath.Abs(inputDir)
	if err != nil {
		return err
	}
	out, err := filepath.Abs(outputDir)
	if err != nil {
		return err
	}
	if in == out {
		return fmt.Errorf("output dir must differ from input dir (%s)", out)
	}
	// output is an ancestor of input if input is inside output.
	rel, err := filepath.Rel(out, in)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "." {
		return fmt.Errorf("output dir %q is an ancestor of input dir %q; refusing to write into the source tree", out, in)
	}
	return nil
}

// uniqueDest returns a destination path in dir for filename, appending " (n)"
// before the extension on collision. Reports whether a collision occurred.
func uniqueDest(dir, filename string, used map[string]bool) (string, bool) {
	ext := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, ext)
	name := filename
	collided := false
	for n := 2; used[name] || exists(filepath.Join(dir, name)); n++ {
		name = fmt.Sprintf("%s (%d)%s", stem, n, ext)
		collided = true
	}
	used[name] = true
	return filepath.Join(dir, name), collided
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func contains(ids []uint64, id uint64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// copyFile copies src to dst byte-for-byte and preserves the source mtime. It
// never touches src.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if fi, err := os.Stat(src); err == nil {
		_ = os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	}
	return nil
}
