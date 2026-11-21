package pipeline

import (
	"fmt"

	"github.com/shahnewaz-labib/photodupe/internal/cluster"
	"github.com/shahnewaz-labib/photodupe/internal/config"
	"github.com/shahnewaz-labib/photodupe/internal/hash"
	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

// Embedder computes Tier B (CLIP) similarity pairs. It is optional: when nil,
// the pipeline runs pHash-only. Phase 3/4 provides the real implementation.
type Embedder interface {
	// Run embeds all records, stores vectors, and writes Tier B pairs to
	// cluster.SimPairsPath.
	Run(cfg config.Config, recs []scanner.ImageRecord, onProgress func(done, total int)) error
}

// RunAll runs scan -> hash -> (embed) -> cluster, writing each stage's cache and
// reporting progress. It runs synchronously; callers wanting non-blocking
// behavior (the UI) should call it in a goroutine.
func RunAll(cfg config.Config, embedder Embedder, prog *Progress) error {
	prog.start()
	var runErr error
	defer func() { prog.finish(runErr) }()

	// scan
	prog.setPhase("scan")
	recs, rep, err := scanner.ScanDir(cfg, prog.update)
	if err != nil {
		runErr = fmt.Errorf("scan: %w", err)
		return runErr
	}
	if err := scanner.SaveCache(scanner.CachePath, recs); err != nil {
		runErr = fmt.Errorf("scan cache: %w", err)
		return runErr
	}
	prog.Logf(fmt.Sprintf("found %d images, skipped %d HEIC, %d errors", rep.Found, rep.SkippedHEIC, rep.Errors))

	// hash (Tier A)
	if cfg.PHashEnabled {
		prog.setPhase("hash")
		th := CacheThreshold(cfg)
		pairs := hash.FindPairs(recs, th, prog.update)
		if err := hash.SavePairs(hash.CachePath, pairs); err != nil {
			runErr = fmt.Errorf("hash cache: %w", err)
			return runErr
		}
		_ = SaveMeta(th) // record cached threshold, for the loosening warning
		prog.Logf(fmt.Sprintf("found %d Tier A pairs", len(pairs)))
	}

	// embed (Tier B) — optional
	if cfg.ClipEnabled && embedder != nil {
		prog.setPhase("embed")
		if err := embedder.Run(cfg, recs, prog.update); err != nil {
			runErr = fmt.Errorf("embed: %w", err)
			return runErr
		}
	}

	// cluster
	prog.setPhase("cluster")
	tierA, err := hash.LoadPairs(hash.CachePath)
	if err != nil {
		runErr = fmt.Errorf("load Tier A: %w", err)
		return runErr
	}
	tierB, err := cluster.LoadSimPairs(cluster.SimPairsPath)
	if err != nil {
		runErr = fmt.Errorf("load Tier B: %w", err)
		return runErr
	}
	clusters := cluster.Build(tierA, tierB, cfg)
	if err := cluster.SaveClusters(cluster.CachePath, clusters); err != nil {
		runErr = fmt.Errorf("cluster cache: %w", err)
		return runErr
	}
	prog.Logf(fmt.Sprintf("built %d duplicate clusters", len(clusters)))
	return nil
}

// Recluster re-runs only the clustering stage from cached pairs. Fast path for
// UI parameter tuning (PLAN §7.5).
func Recluster(cfg config.Config) ([]cluster.Cluster, error) {
	tierA, err := hash.LoadPairs(hash.CachePath)
	if err != nil {
		return nil, err
	}
	tierB, err := cluster.LoadSimPairs(cluster.SimPairsPath)
	if err != nil {
		return nil, err
	}
	clusters := cluster.Build(tierA, tierB, cfg)
	if err := cluster.SaveClusters(cluster.CachePath, clusters); err != nil {
		return nil, err
	}
	return clusters, nil
}
