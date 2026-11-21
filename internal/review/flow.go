package review

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shahnewaz-labib/photodupe/internal/cluster"
	"github.com/shahnewaz-labib/photodupe/internal/config"
	"github.com/shahnewaz-labib/photodupe/internal/export"
	"github.com/shahnewaz-labib/photodupe/internal/hash"
	"github.com/shahnewaz-labib/photodupe/internal/keeper"
	"github.com/shahnewaz-labib/photodupe/internal/pipeline"
	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

// folderWalkCap bounds how many entries handleFolder visits, so pointing the
// picker at "/" can't hang the UI.
const folderWalkCap = 250_000

// expandHome turns a leading "~" into the user's home directory.
func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

type folderResp struct {
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	IsDir      bool   `json:"is_dir"`
	Entries    int    `json:"entries"` // top-level entries
	Subfolders int    `json:"subfolders"`
	Photos     int    `json:"photos"`
	Bytes      int64  `json:"bytes"`
	HEIC       int    `json:"heic"`
	Truncated  bool   `json:"truncated"`
	Conflict   string `json:"conflict,omitempty"` // role=output: why export would refuse
}

// handleFolder describes a folder for the pickers: for an input, how many
// photos it holds; for an output (role=output), whether it exists, is empty,
// and passes export's guardrails. Read-only.
func (s *Server) handleFolder(w http.ResponseWriter, r *http.Request) {
	p := expandHome(r.URL.Query().Get("path"))
	if p == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	resp := folderResp{Path: abs}

	if r.URL.Query().Get("role") == "output" {
		s.mu.Lock()
		in := s.cfg.InputDir
		s.mu.Unlock()
		if err := export.ValidateDirs(in, abs); err != nil {
			resp.Conflict = err.Error()
		}
	}

	fi, err := os.Stat(abs)
	if err != nil {
		writeJSON(w, http.StatusOK, resp) // Exists=false
		return
	}
	resp.Exists, resp.IsDir = true, fi.IsDir()
	if !fi.IsDir() {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if top, err := os.ReadDir(abs); err == nil {
		resp.Entries = len(top)
		for _, e := range top {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				resp.Subfolders++
			}
		}
	}
	if r.URL.Query().Get("role") == "output" {
		writeJSON(w, http.StatusOK, resp) // no need to count photos
		return
	}

	visited := 0
	_ = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		visited++
		if visited > folderWalkCap {
			resp.Truncated = true
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if !scanner.IsImageExt(ext) {
			return nil
		}
		resp.Photos++
		if scanner.IsHEICExt(ext) {
			resp.HEIC++
		}
		if info, err := d.Info(); err == nil {
			resp.Bytes += info.Size()
		}
		return nil
	})
	writeJSON(w, http.StatusOK, resp)
}

type summaryResp struct {
	HasResults bool     `json:"has_results"`
	InputDir   string   `json:"input_dir"`
	OutputDir  string   `json:"output_dir"`
	Photos     int      `json:"photos"`
	TotalBytes int64    `json:"total_bytes"`
	Groups     int      `json:"groups"`
	Keep       int      `json:"keep"`
	KeepBytes  int64    `json:"keep_bytes"`
	Extra      int      `json:"extra"`
	ExtraBytes int64    `json:"extra_bytes"`
	Auto       int      `json:"auto"`     // groups using the suggested keeper
	Changed    int      `json:"changed"`  // groups where the user picked another keeper
	KeptAll    int      `json:"kept_all"` // groups marked "not duplicates"
	Reviewed   int      `json:"reviewed"`
	Approved   int      `json:"approved"`    // groups approved and not yet cleaned up
	RemovedDir string   `json:"removed_dir"` // where clean-up moves left-out copies
	Threshold  int      `json:"threshold"`
	Level      string   `json:"level"` // matching StrictnessLevel name, or "custom"
	KeepOrder  []string `json:"keep_order"`
}

// handleSummary reports what an export would do right now, using the same
// keeper logic as export itself.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	recs := make([]scanner.ImageRecord, 0, len(s.records))
	for _, rec := range s.records {
		recs = append(recs, rec)
	}
	keep := export.KeeperSet(s.cfg, recs, s.clusters, s.decisions)

	sum := summaryResp{
		HasResults: len(recs) > 0,
		InputDir:   s.cfg.InputDir,
		OutputDir:  s.cfg.OutputDir,
		RemovedDir: removedDir(s.cfg),
		Photos:     len(recs),
		Groups:     len(s.clusters),
		Threshold:  s.cfg.PHashThreshold,
		Level:      levelName(s.cfg.PHashThreshold),
		KeepOrder:  s.cfg.KeeperOrder,
	}
	for _, rec := range recs {
		sum.TotalBytes += rec.FileSize
		if keep[rec.ID] {
			sum.Keep++
			sum.KeepBytes += rec.FileSize
		} else {
			sum.Extra++
			sum.ExtraBytes += rec.FileSize
		}
	}
	for _, c := range s.clusters {
		dec := s.decisions[c.ID]
		if dec.Resolved {
			sum.Reviewed++
		}
		if dec.Approved && !dec.Skipped {
			sum.Approved++
		}
		switch {
		case dec.Skipped:
			sum.KeptAll++
		case dec.KeeperID != 0 && dec.KeeperID != s.suggestedLocked(c).ID:
			sum.Changed++
		default:
			sum.Auto++
		}
	}
	writeJSON(w, http.StatusOK, sum)
}

// suggestedLocked returns the heuristic keeper for c. s.mu must be held.
func (s *Server) suggestedLocked(c cluster.Cluster) scanner.ImageRecord {
	members := make([]scanner.ImageRecord, 0, len(c.MemberIDs))
	for _, id := range c.MemberIDs {
		if rec, ok := s.records[id]; ok {
			members = append(members, rec)
		}
	}
	return keeper.SuggestKeeper(members, s.cfg.KeeperOrder)
}

func levelName(threshold int) string {
	for _, l := range config.StrictnessLevels {
		if l.Threshold == threshold {
			return l.Name
		}
	}
	return "custom"
}

type levelView struct {
	Name        string `json:"name"`
	Threshold   int    `json:"threshold"`
	Groups      int    `json:"groups"`
	Extra       int    `json:"extra"`
	NeedsRescan bool   `json:"needs_rescan"` // cached pairs don't reach this level
}

// handleStrictness previews how many groups each match level would produce,
// from the cached pairs, without changing anything.
func (s *Server) handleStrictness(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	tierA, errA := hash.LoadPairs(hash.CachePath)
	tierB, _ := cluster.LoadSimPairs(cluster.SimPairsPath)
	meta, metaOK := pipeline.LoadMeta()

	levels := make([]levelView, 0, len(config.StrictnessLevels))
	for _, l := range config.StrictnessLevels {
		lv := levelView{Name: l.Name, Threshold: l.Threshold}
		if errA != nil || !metaOK || l.Threshold > meta.PHashThreshold {
			lv.NeedsRescan = true
		} else {
			c := cfg
			c.PHashThreshold = l.Threshold
			for _, cl := range cluster.Build(tierA, tierB, c) {
				lv.Groups++
				lv.Extra += len(cl.MemberIDs) - 1
			}
		}
		levels = append(levels, lv)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"current": levelName(cfg.PHashThreshold),
		"levels":  levels,
	})
}

// handleSetStrictness switches the match level. If the cached pairs cover it,
// it re-clusters instantly; otherwise it starts a full re-run.
func (s *Server) handleSetStrictness(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	th := -1
	for _, l := range config.StrictnessLevels {
		if l.Name == body.Name {
			th = l.Threshold
		}
	}
	if th < 0 {
		http.Error(w, "unknown level "+body.Name, http.StatusBadRequest)
		return
	}
	if s.prog.Running() {
		http.Error(w, "a scan is in progress", http.StatusConflict)
		return
	}

	s.mu.Lock()
	s.cfg.PHashThreshold = th
	cfg := s.cfg
	s.mu.Unlock()
	if err := config.Save(config.DefaultPath, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if pipeline.LooseningWarning(cfg) != "" {
		s.startRun(cfg)
		writeJSON(w, http.StatusAccepted, map[string]any{"rescan": true})
		return
	}
	old := s.snapshotClusters()
	if _, err := pipeline.Recluster(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.reload()
	s.carryDecisions(old)
	writeJSON(w, http.StatusOK, map[string]any{"rescan": false})
}

// snapshotClusters copies the current clusters, for carryDecisions.
func (s *Server) snapshotClusters() []cluster.Cluster {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]cluster.Cluster(nil), s.clusters...)
}

// carryDecisions re-keys saved review decisions after clusters were rebuilt.
// Cluster IDs are positional, so without this a decision could land on an
// unrelated group. Each old decision moves to the new cluster sharing the most
// members with its old cluster. A chosen keeper carries over if it is still a
// member; "not duplicates" carries over only if the group is unchanged.
func (s *Server) carryDecisions(old []cluster.Cluster) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.decisions) == 0 {
		return
	}

	where := map[uint64]int{} // member id -> index in s.clusters
	for i, c := range s.clusters {
		for _, id := range c.MemberIDs {
			where[id] = i
		}
	}
	next := map[string]cluster.ClusterDecision{}
	for _, oc := range old {
		dec, ok := s.decisions[oc.ID]
		if !ok {
			continue
		}
		overlap := map[int]int{}
		for _, id := range oc.MemberIDs {
			if i, ok := where[id]; ok {
				overlap[i]++
			}
		}
		best, bestN := -1, 0
		for i, n := range overlap {
			if n > bestN || (n == bestN && i < best) {
				best, bestN = i, n
			}
		}
		if best < 0 {
			continue // group dissolved
		}
		nc := s.clusters[best]
		if _, taken := next[nc.ID]; taken {
			continue
		}
		// An approve/decline verdict was given for an exact set of photos, so
		// it only survives if the group is unchanged; a picked keeper carries
		// over whenever it is still in the group.
		var carried cluster.ClusterDecision
		if dec.KeeperID != 0 && containsID(nc.MemberIDs, dec.KeeperID) {
			carried.KeeperID = dec.KeeperID
		}
		if sameMembers(oc.MemberIDs, nc.MemberIDs) {
			carried.Skipped, carried.Approved, carried.Resolved = dec.Skipped, dec.Approved, dec.Resolved
		}
		if carried == (cluster.ClusterDecision{}) {
			continue
		}
		next[nc.ID] = carried
	}
	s.decisions = next
	_ = cluster.SaveDecisions(cluster.StatePath, next)
}

func sameMembers(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[uint64]bool, len(a))
	for _, id := range a {
		set[id] = true
	}
	for _, id := range b {
		if !set[id] {
			return false
		}
	}
	return true
}

func containsID(ids []uint64, id uint64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// startRun launches the pipeline in the background and carries review
// decisions over to the new clusters when it finishes.
func (s *Server) startRun(cfg config.Config) {
	old := s.snapshotClusters()
	s.prog.Begin()
	go func() {
		_ = pipeline.RunAll(cfg, s.embedder, s.prog)
		s.reload()
		s.carryDecisions(old)
	}()
}

// exportState tracks the background export the UI polls.
type exportState struct {
	Running bool           `json:"running"`
	Done    int            `json:"done"`
	Total   int            `json:"total"`
	Report  *export.Report `json:"report"`
	Error   string         `json:"error"`
	Output  string         `json:"output"`
}

func (s *Server) handleExportStatus(w http.ResponseWriter, r *http.Request) {
	s.expMu.Lock()
	st := s.exp
	s.expMu.Unlock()
	writeJSON(w, http.StatusOK, st)
}

// handleOpen opens the configured output folder in the OS file manager. It
// only ever opens cfg.OutputDir, never a client-supplied path.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	dir := s.cfg.OutputDir
	s.mu.Unlock()
	if dir == "" {
		http.Error(w, "no output folder set", http.StatusBadRequest)
		return
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		http.Error(w, "output folder does not exist", http.StatusBadRequest)
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	if err := cmd.Start(); err != nil {
		http.Error(w, "could not open folder: "+err.Error(), http.StatusInternalServerError)
		return
	}
	go cmd.Wait() // reap
	writeJSON(w, http.StatusOK, map[string]string{"status": "opened"})
}
