package review

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/shahnewaz-labib/photodupe/internal/cluster"
	"github.com/shahnewaz-labib/photodupe/internal/config"
	"github.com/shahnewaz-labib/photodupe/internal/export"
	"github.com/shahnewaz-labib/photodupe/internal/keeper"
	"github.com/shahnewaz-labib/photodupe/internal/pipeline"
	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.prog.Get())
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if s.prog.Running() {
		http.Error(w, "a run is already in progress", http.StatusConflict)
		return
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	if cfg.InputDir == "" {
		http.Error(w, "input_dir is not set", http.StatusBadRequest)
		return
	}

	s.startRun(cfg)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (s *Server) handleRecluster(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	warning := pipeline.LooseningWarning(cfg)
	old := s.snapshotClusters()
	clusters, err := pipeline.Recluster(cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.reload()
	s.carryDecisions(old)
	writeJSON(w, http.StatusOK, map[string]any{
		"clusters": len(clusters),
		"warning":  warning,
	})
}

// memberView is one image within a cluster, as rendered by the UI. ID is
// serialized as a string: uint64 IDs exceed JS's safe integer range and would
// lose precision as JSON numbers.
type memberView struct {
	ID          uint64 `json:"id,string"`
	Filename    string `json:"filename"`
	Path        string `json:"path"`
	Folder      string `json:"folder"` // directory relative to the input folder
	IsHEIC      bool   `json:"is_heic"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	FileSize    int64  `json:"file_size"`
	CaptureTime string `json:"capture_time"`
	Keeper      bool   `json:"keeper"`
}

type clusterView struct {
	ID          string                  `json:"id"`
	Tiers       []string                `json:"tiers"`
	SuggestedID uint64                  `json:"suggested_id,string"`
	Members     []memberView            `json:"members"`
	Decision    cluster.ClusterDecision `json:"decision"`
}

func (s *Server) handleClusters(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	views := make([]clusterView, 0, len(s.clusters))
	for _, c := range s.clusters {
		members := make([]scanner.ImageRecord, 0, len(c.MemberIDs))
		for _, id := range c.MemberIDs {
			if rec, ok := s.records[id]; ok {
				members = append(members, rec)
			}
		}
		dec := s.decisions[c.ID]

		suggested := keeper.SuggestKeeper(members, s.cfg.KeeperOrder).ID
		keeperID := dec.KeeperID
		if keeperID == 0 || !containsID(c.MemberIDs, keeperID) {
			keeperID = suggested
		}

		mv := make([]memberView, 0, len(members))
		for _, rec := range members {
			mv = append(mv, memberView{
				ID:          rec.ID,
				Filename:    rec.Filename,
				Path:        rec.AbsPath,
				Folder:      relFolder(s.cfg.InputDir, rec.AbsPath),
				IsHEIC:      rec.IsHEIC,
				Width:       rec.Width,
				Height:      rec.Height,
				FileSize:    rec.FileSize,
				CaptureTime: rec.CaptureTime.Format("2006-01-02 15:04:05"),
				Keeper:      rec.ID == keeperID && !dec.Skipped,
			})
		}
		views = append(views, clusterView{ID: c.ID, Tiers: c.Tiers, SuggestedID: suggested, Members: mv, Decision: dec})
	}
	writeJSON(w, http.StatusOK, views)
}

// relFolder returns abs's directory relative to inputDir ("" for the root),
// falling back to the absolute directory.
func relFolder(inputDir, abs string) string {
	dir := filepath.Dir(abs)
	if inputDir != "" {
		if in, err := filepath.Abs(inputDir); err == nil {
			if rel, err := filepath.Rel(in, dir); err == nil && within(dir, in) {
				if rel == "." {
					return ""
				}
				return filepath.ToSlash(rel)
			}
		}
	}
	return dir
}

func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var dec cluster.ClusterDecision
	if err := json.NewDecoder(r.Body).Decode(&dec); err != nil {
		http.Error(w, "bad decision body: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.decisions[id] = dec
	toSave := make(map[string]cluster.ClusterDecision, len(s.decisions))
	for k, v := range s.decisions {
		toSave[k] = v
	}
	s.mu.Unlock()

	if err := cluster.SaveDecisions(cluster.StatePath, toSave); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// handleImage serves an image by record ID. The client never supplies a path —
// only an ID we resolve against the scanned set — and we additionally verify the
// resolved file sits under the input dir or the HEIC cache (PLAN §7.2).
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	rec, ok := s.records[id]
	inputDir := s.cfg.InputDir
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !pathAllowed(rec.WorkPath, inputDir) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	serve := rec.WorkPath
	if r.URL.Query().Get("thumb") == "1" {
		serve = thumbFor(rec.ID, rec.WorkPath)
	}
	http.ServeFile(w, r, serve)
}

// pathAllowed reports whether p is within inputDir or the HEIC cache dir.
func pathAllowed(p, inputDir string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	heic, _ := filepath.Abs(scanner.HeicCacheDir)
	if within(abs, heic) {
		return true
	}
	if inputDir == "" {
		return false
	}
	in, err := filepath.Abs(inputDir)
	if err != nil {
		return false
	}
	return within(abs, in)
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

type browseResp struct {
	Path   string   `json:"path"`
	Parent string   `json:"parent"`
	Dirs   []string `json:"dirs"`
}

// handleBrowse lists subdirectories of a path so the UI can pick input/output
// folders server-side. Read-only.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		if home, err := os.UserHomeDir(); err == nil {
			p = home
		} else {
			p = string(filepath.Separator)
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	writeJSON(w, http.StatusOK, browseResp{Path: abs, Parent: filepath.Dir(abs), Dirs: dirs})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cfg := s.cfg // start from current so unspecified fields keep their value
	err := json.NewDecoder(r.Body).Decode(&cfg)
	if err == nil {
		s.cfg = cfg
	}
	s.mu.Unlock()
	if err != nil {
		http.Error(w, "bad config body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := config.Save(config.DefaultPath, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// handleExport validates the folders, then copies keepers in the background;
// the UI polls GET /api/export/status for progress and the final report.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cfg := s.cfg
	recs := make([]scanner.ImageRecord, 0, len(s.records))
	for _, rec := range s.records {
		recs = append(recs, rec)
	}
	clusters := s.clusters
	decisions := make(map[string]cluster.ClusterDecision, len(s.decisions))
	for k, v := range s.decisions {
		decisions[k] = v
	}
	s.mu.Unlock()

	if err := export.ValidateDirs(cfg.InputDir, cfg.OutputDir); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.expMu.Lock()
	if s.exp.Running {
		s.expMu.Unlock()
		http.Error(w, "an export is already running", http.StatusConflict)
		return
	}
	s.exp = exportState{Running: true, Output: cfg.OutputDir}
	s.expMu.Unlock()

	go func() {
		rep, err := export.Export(cfg, recs, clusters, decisions, func(done, total int) {
			s.expMu.Lock()
			s.exp.Done, s.exp.Total = done, total
			s.expMu.Unlock()
		})
		s.expMu.Lock()
		s.exp.Running = false
		if err != nil {
			s.exp.Error = err.Error()
		} else {
			s.exp.Report = &rep
		}
		s.expMu.Unlock()
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}
