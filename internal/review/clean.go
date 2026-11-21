package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/shahnewaz-labib/photodupe/internal/cluster"
	"github.com/shahnewaz-labib/photodupe/internal/config"
	"github.com/shahnewaz-labib/photodupe/internal/hash"
	"github.com/shahnewaz-labib/photodupe/internal/pipeline"
	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

// RemovedLogPath records every file a clean-up moved (one JSON object per
// line: time, from, to) so a move can always be traced and undone.
const RemovedLogPath = "data/removed_log.jsonl"

// removedDir is where clean-up moves left-out copies: cfg.RemovedDir, or a
// sibling of the input folder named "<input>-removed".
func removedDir(cfg config.Config) string {
	if cfg.RemovedDir != "" {
		return expandHome(cfg.RemovedDir)
	}
	if cfg.InputDir == "" {
		return ""
	}
	in, err := filepath.Abs(expandHome(cfg.InputDir))
	if err != nil {
		return ""
	}
	return in + "-removed"
}

type cleanResp struct {
	Groups int      `json:"groups"` // approved groups cleaned up
	Moved  int      `json:"moved"`  // files moved to Dest
	Bytes  int64    `json:"bytes"`
	Dest   string   `json:"dest"`
	Failed []string `json:"failed"` // per-file or per-group problems; those files stay put
}

type cleanGroup struct {
	keeper scanner.ImageRecord
	drops  []scanner.ImageRecord
}

// handleClean moves the left-out copies of every approved group out of the
// input folder into removedDir, keeping their subfolders. Nothing is deleted.
// Moved photos are then dropped from the caches and the groups rebuilt, so
// cleaned groups disappear and every other decision carries over.
func (s *Server) handleClean(w http.ResponseWriter, r *http.Request) {
	if !s.cleanMu.TryLock() {
		http.Error(w, "a clean-up is already running", http.StatusConflict)
		return
	}
	defer s.cleanMu.Unlock()
	if s.prog.Running() {
		http.Error(w, "wait for the scan to finish first", http.StatusConflict)
		return
	}
	s.expMu.Lock()
	exporting := s.exp.Running
	s.expMu.Unlock()
	if exporting {
		http.Error(w, "wait for the copy to finish first", http.StatusConflict)
		return
	}

	s.mu.Lock()
	cfg := s.cfg
	var plan []cleanGroup
	for _, c := range s.clusters {
		dec := s.decisions[c.ID]
		if !dec.Approved || dec.Skipped {
			continue
		}
		keepID := dec.KeeperID
		if keepID == 0 || !containsID(c.MemberIDs, keepID) {
			keepID = s.suggestedLocked(c).ID
		}
		g := cleanGroup{}
		for _, id := range c.MemberIDs {
			rec, ok := s.records[id]
			if !ok {
				continue
			}
			if id == keepID {
				g.keeper = rec
			} else {
				g.drops = append(g.drops, rec)
			}
		}
		if g.keeper.ID != 0 && len(g.drops) > 0 {
			plan = append(plan, g)
		}
	}
	s.mu.Unlock()

	in, err := filepath.Abs(expandHome(cfg.InputDir))
	if err != nil || cfg.InputDir == "" {
		http.Error(w, "the photo folder is not set", http.StatusBadRequest)
		return
	}
	dest, err := filepath.Abs(removedDir(cfg))
	if err != nil {
		http.Error(w, "bad removed folder: "+err.Error(), http.StatusBadRequest)
		return
	}
	if within(dest, in) || within(in, dest) {
		http.Error(w, fmt.Sprintf("the removed folder %s can't be inside your photo folder (or contain it)", dest), http.StatusBadRequest)
		return
	}

	resp := cleanResp{Dest: dest, Failed: []string{}}
	_ = os.MkdirAll(filepath.Dir(RemovedLogPath), 0o755)
	logf, err := os.OpenFile(RemovedLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		http.Error(w, "can't open the move log: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer logf.Close()
	enc := json.NewEncoder(logf)

	moved := map[uint64]bool{}
	for _, g := range plan {
		// Never remove copies unless the photo we keep is really there.
		if _, err := os.Stat(g.keeper.AbsPath); err != nil {
			resp.Failed = append(resp.Failed, fmt.Sprintf("%s: the photo to keep is missing, group left alone", g.keeper.AbsPath))
			continue
		}
		n := 0
		for _, rec := range g.drops {
			src, err := filepath.Abs(rec.AbsPath)
			if err != nil || !within(src, in) {
				resp.Failed = append(resp.Failed, fmt.Sprintf("%s: outside the photo folder, left alone", rec.AbsPath))
				continue
			}
			rel, _ := filepath.Rel(in, src)
			dst, err := moveFile(src, filepath.Join(dest, rel))
			if err != nil {
				resp.Failed = append(resp.Failed, fmt.Sprintf("%s: %v", src, err))
				continue
			}
			_ = enc.Encode(map[string]string{"time": time.Now().Format(time.RFC3339), "from": src, "to": dst})
			moved[rec.ID] = true
			resp.Moved++
			resp.Bytes += rec.FileSize
			n++
		}
		if n > 0 {
			resp.Groups++
		}
	}

	if len(moved) > 0 {
		if err := s.forget(cfg, moved); err != nil {
			http.Error(w, "files were moved but updating the results failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// forget drops moved photos from the scan and pair caches, then rebuilds the
// groups and carries decisions over.
func (s *Server) forget(cfg config.Config, moved map[uint64]bool) error {
	recs, err := scanner.LoadCache(scanner.CachePath)
	if err != nil {
		return err
	}
	keptRecs := recs[:0]
	for _, r := range recs {
		if !moved[r.ID] {
			keptRecs = append(keptRecs, r)
		}
	}
	if err := scanner.SaveCache(scanner.CachePath, keptRecs); err != nil {
		return err
	}

	pairs, err := hash.LoadPairs(hash.CachePath)
	if err != nil {
		return err
	}
	keptPairs := pairs[:0]
	for _, p := range pairs {
		if !moved[p.AID] && !moved[p.BID] {
			keptPairs = append(keptPairs, p)
		}
	}
	if err := hash.SavePairs(hash.CachePath, keptPairs); err != nil {
		return err
	}

	sim, err := cluster.LoadSimPairs(cluster.SimPairsPath)
	if err != nil {
		return err
	}
	if sim != nil {
		keptSim := sim[:0]
		for _, p := range sim {
			if !moved[p.AID] && !moved[p.BID] {
				keptSim = append(keptSim, p)
			}
		}
		b, err := json.Marshal(keptSim)
		if err != nil {
			return err
		}
		if err := os.WriteFile(cluster.SimPairsPath, b, 0o644); err != nil {
			return err
		}
	}

	old := s.snapshotClusters()
	if _, err := pipeline.Recluster(cfg); err != nil {
		return err
	}
	s.reload()
	s.carryDecisions(old)
	return nil
}

// moveFile moves src to dst (creating parent folders), adding " (2)", " (3)"…
// before the extension if dst exists. It falls back to copy+delete across
// filesystems. It returns the final destination.
func moveFile(src, dst string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	ext := filepath.Ext(dst)
	stem := strings.TrimSuffix(dst, ext)
	for n := 2; ; n++ {
		if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
			break
		}
		dst = fmt.Sprintf("%s (%d)%s", stem, n, ext)
	}
	err := os.Rename(src, dst)
	if err == nil {
		return dst, nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return "", err
	}
	if err := copyThenRemove(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func copyThenRemove(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	_ = os.Chtimes(dst, info.ModTime(), info.ModTime())
	return os.Remove(src)
}
