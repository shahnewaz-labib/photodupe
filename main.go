// Command photodupe finds duplicate/similar images in a folder and copies the
// keepers to a new folder. See PLAN.md for the full design.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/shahnewaz-labib/photodupe/internal/cluster"
	"github.com/shahnewaz-labib/photodupe/internal/config"
	"github.com/shahnewaz-labib/photodupe/internal/export"
	"github.com/shahnewaz-labib/photodupe/internal/hash"
	"github.com/shahnewaz-labib/photodupe/internal/pipeline"
	"github.com/shahnewaz-labib/photodupe/internal/review"
	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

const usage = `photodupe - local image deduplicator

usage: photodupe <command> [flags]

commands:
  scan      walk input dir, build image record cache
  hash      perceptual-hash images, find near-exact dup pairs (Tier A)
  cluster   merge duplicate pairs into groups
  export    copy keepers + uniques to the output dir
  ui        start the web UI (alias: review)
  all       scan -> hash -> cluster, then start the UI
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cmd := os.Args[1]
	// Subcommand flags are parsed inside each handler as phases land.
	cfg, err := config.Load(config.DefaultPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	switch cmd {
	case "scan":
		cmdScan(cfg, os.Args[2:])
	case "hash":
		cmdHash(cfg)
	case "cluster":
		cmdCluster(cfg)
	case "export":
		cmdExport(cfg, os.Args[2:])
	case "ui", "review":
		cmdUI(cfg, os.Args[2:])
	case "all":
		cmdAll(cfg, os.Args[2:])
	case "embed":
		// ponytail: wired in with Phase 3 (CLIP/ONNX).
		fmt.Fprintln(os.Stderr, "photodupe embed: not implemented yet")
		os.Exit(1)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

func cmdScan(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	fs.StringVar(&cfg.InputDir, "input", cfg.InputDir, "input directory to scan")
	fs.Parse(args)

	if cfg.InputDir == "" {
		fmt.Fprintln(os.Stderr, "scan: --input is required (or set input_dir in config)")
		os.Exit(2)
	}

	recs, rep, err := scanner.ScanDir(cfg, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan: %v\n", err)
		os.Exit(1)
	}
	if err := scanner.SaveCache(scanner.CachePath, recs); err != nil {
		fmt.Fprintf(os.Stderr, "scan: write cache: %v\n", err)
		os.Exit(1)
	}
	// Persist config so a later `ui` picks up InputDir (needed to serve images).
	_ = config.Save(config.DefaultPath, cfg)
	fmt.Printf("found %d images, skipped %d HEIC (no converter), %d errors -> %s\n",
		rep.Found, rep.SkippedHEIC, rep.Errors, scanner.CachePath)
}

func cmdHash(cfg config.Config) {
	recs, err := scanner.LoadCache(scanner.CachePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash: load scan cache (run `scan` first): %v\n", err)
		os.Exit(1)
	}
	th := pipeline.CacheThreshold(cfg)
	pairs := hash.FindPairs(recs, th, nil)
	if err := hash.SavePairs(hash.CachePath, pairs); err != nil {
		fmt.Fprintf(os.Stderr, "hash: write pairs: %v\n", err)
		os.Exit(1)
	}
	_ = pipeline.SaveMeta(th) // record cached threshold for the loosening warning
	fmt.Printf("hashed %d images, cached %d Tier A pairs (dist <= %d; clustering uses <= %d) -> %s\n",
		len(recs), len(pairs), th, cfg.PHashThreshold, hash.CachePath)
}

func cmdCluster(cfg config.Config) {
	tierA, err := hash.LoadPairs(hash.CachePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cluster: load Tier A: %v\n", err)
		os.Exit(1)
	}
	tierB, err := cluster.LoadSimPairs(cluster.SimPairsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cluster: load Tier B: %v\n", err)
		os.Exit(1)
	}
	clusters := cluster.Build(tierA, tierB, cfg)
	if err := cluster.SaveClusters(cluster.CachePath, clusters); err != nil {
		fmt.Fprintf(os.Stderr, "cluster: write: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("built %d duplicate clusters (%d Tier A + %d Tier B pairs) -> %s\n",
		len(clusters), len(tierA), len(tierB), cluster.CachePath)
}

func cmdExport(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	fs.StringVar(&cfg.OutputDir, "output", cfg.OutputDir, "output directory for kept images")
	fs.Parse(args)
	_ = config.Save(config.DefaultPath, cfg)

	recs, err := scanner.LoadCache(scanner.CachePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "export: load scan cache (run `scan` first): %v\n", err)
		os.Exit(1)
	}
	clusters, err := cluster.LoadClusters(cluster.CachePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "export: load clusters (run `cluster` first): %v\n", err)
		os.Exit(1)
	}
	decisions, err := cluster.LoadDecisions(cluster.StatePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "export: load decisions: %v\n", err)
		os.Exit(1)
	}
	rep, err := export.Export(cfg, recs, clusters, decisions, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "export: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("copied %d, skipped %d duplicates, %d collisions, %d errors -> %s\n",
		rep.Copied, rep.Skipped, rep.Collisions, len(rep.Errors), cfg.OutputDir)
	for _, e := range rep.Errors {
		fmt.Fprintln(os.Stderr, "  error:", e)
	}
}

func cmdUI(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	fs.StringVar(&cfg.ServerAddr, "addr", cfg.ServerAddr, "address to listen on")
	fs.Parse(args)

	// ponytail: embedder nil => pHash-only UI until Phase 3 wires CLIP in.
	srv := review.New(cfg, nil)
	fmt.Printf("photodupe UI: http://%s\n", cfg.ServerAddr)
	if err := srv.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "ui: %v\n", err)
		os.Exit(1)
	}
}

func cmdAll(cfg config.Config, args []string) {
	fs := flag.NewFlagSet("all", flag.ExitOnError)
	fs.StringVar(&cfg.InputDir, "input", cfg.InputDir, "input directory to scan")
	fs.StringVar(&cfg.OutputDir, "output", cfg.OutputDir, "output directory for kept images")
	fs.StringVar(&cfg.ServerAddr, "addr", cfg.ServerAddr, "address to listen on")
	fs.Parse(args)

	if cfg.InputDir == "" {
		fmt.Fprintln(os.Stderr, "all: --input is required")
		os.Exit(2)
	}
	_ = config.Save(config.DefaultPath, cfg)
	prog := pipeline.NewProgress()
	if err := pipeline.RunAll(cfg, nil, prog); err != nil {
		fmt.Fprintf(os.Stderr, "all: %v\n", err)
		os.Exit(1)
	}
	for _, line := range prog.Get().Log {
		fmt.Println(line)
	}
	cmdUI(cfg, []string{"-addr", cfg.ServerAddr})
}
