// Package review serves the local web UI: run the pipeline, watch progress,
// review duplicate clusters, tweak params, and export keepers. See PLAN §7.
package review

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"

	"github.com/shahnewaz-labib/photodupe/internal/cluster"
	"github.com/shahnewaz-labib/photodupe/internal/config"
	"github.com/shahnewaz-labib/photodupe/internal/pipeline"
	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

//go:embed templates static
var assets embed.FS

// Server holds UI state: current config, the pipeline progress, and the
// in-memory record/cluster caches used by the review views.
type Server struct {
	mu       sync.Mutex
	cfg      config.Config
	prog     *pipeline.Progress
	embedder pipeline.Embedder // optional Tier B engine; nil => pHash-only

	records   map[uint64]scanner.ImageRecord
	clusters  []cluster.Cluster
	decisions map[string]cluster.ClusterDecision

	expMu sync.Mutex
	exp   exportState

	cleanMu sync.Mutex // held while a clean-up moves files
}

// New builds a Server. embedder may be nil (pHash-only). It loads any existing
// caches so a restart resumes review in progress.
func New(cfg config.Config, embedder pipeline.Embedder) *Server {
	s := &Server{
		cfg:       cfg,
		prog:      pipeline.NewProgress(),
		embedder:  embedder,
		records:   map[uint64]scanner.ImageRecord{},
		decisions: map[string]cluster.ClusterDecision{},
	}
	s.reload()
	return s
}

// reload refreshes the in-memory caches from disk. Missing caches are fine.
func (s *Server) reload() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if recs, err := scanner.LoadCache(scanner.CachePath); err == nil {
		m := make(map[uint64]scanner.ImageRecord, len(recs))
		for _, r := range recs {
			m[r.ID] = r
		}
		s.records = m
	}
	if cl, err := cluster.LoadClusters(cluster.CachePath); err == nil {
		s.clusters = cl
	}
	if dec, err := cluster.LoadDecisions(cluster.StatePath); err == nil {
		// Older versions marked a group Resolved just by moving past it. Only an
		// explicit approve or decline counts now, so those go back to pending.
		for id, d := range dec {
			if d.Resolved && !d.Approved && !d.Skipped {
				d.Resolved = false
				dec[id] = d
			}
		}
		s.decisions = dec
	}
}

// Handler returns the configured HTTP mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Static assets + index.
	staticFS, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("GET /{$}", s.handleIndex)

	// API.
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/run", s.handleRun)
	mux.HandleFunc("POST /api/clean", s.handleClean)
	mux.HandleFunc("POST /api/recluster", s.handleRecluster)
	mux.HandleFunc("GET /api/clusters", s.handleClusters)
	mux.HandleFunc("POST /api/clusters/{id}/decision", s.handleDecision)
	mux.HandleFunc("GET /api/image", s.handleImage)
	mux.HandleFunc("GET /api/browse", s.handleBrowse)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("POST /api/config", s.handleSetConfig)
	mux.HandleFunc("POST /api/export", s.handleExport)
	mux.HandleFunc("GET /api/export/status", s.handleExportStatus)
	mux.HandleFunc("GET /api/folder", s.handleFolder)
	mux.HandleFunc("GET /api/summary", s.handleSummary)
	mux.HandleFunc("GET /api/strictness", s.handleStrictness)
	mux.HandleFunc("POST /api/strictness", s.handleSetStrictness)
	mux.HandleFunc("POST /api/open", s.handleOpen)

	return mux
}

// Serve starts the HTTP server (blocking).
func (s *Server) Serve() error {
	addr := s.cfg.ServerAddr
	slog.Info("photodupe UI listening", "addr", "http://"+addr)
	return http.ListenAndServe(addr, s.Handler())
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	b, err := assets.ReadFile("templates/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}
