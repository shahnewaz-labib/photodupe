package export

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shahnewaz-labib/photodupe/internal/cluster"
	"github.com/shahnewaz-labib/photodupe/internal/config"
	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExportKeepersAndUniques(t *testing.T) {
	in := t.TempDir()
	out := filepath.Join(t.TempDir(), "kept")

	// cluster: keep.jpg (bigger res) vs drop.jpg
	keep := filepath.Join(in, "keep.jpg")
	drop := filepath.Join(in, "drop.jpg")
	uniq := filepath.Join(in, "uniq.jpg")
	writeFile(t, keep, "KEEP")
	writeFile(t, drop, "DROP")
	writeFile(t, uniq, "UNIQ")

	recs := []scanner.ImageRecord{
		{ID: 1, AbsPath: keep, Filename: "keep.jpg", Width: 200, Height: 200, FileSize: 4},
		{ID: 2, AbsPath: drop, Filename: "drop.jpg", Width: 100, Height: 100, FileSize: 4},
		{ID: 3, AbsPath: uniq, Filename: "uniq.jpg", Width: 50, Height: 50, FileSize: 4},
	}
	clusters := []cluster.Cluster{{ID: "cluster_001", Tiers: []string{"A"}, MemberIDs: []uint64{1, 2}}}

	cfg := config.Default()
	cfg.InputDir = in
	cfg.OutputDir = out

	rep, err := Export(cfg, recs, clusters, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Copied != 2 { // keep + uniq
		t.Fatalf("copied = %d, want 2", rep.Copied)
	}
	if rep.Skipped != 1 { // drop
		t.Fatalf("skipped = %d, want 1", rep.Skipped)
	}
	if !exists(filepath.Join(out, "keep.jpg")) || !exists(filepath.Join(out, "uniq.jpg")) {
		t.Fatal("expected keep.jpg and uniq.jpg in output")
	}
	if exists(filepath.Join(out, "drop.jpg")) {
		t.Fatal("drop.jpg should not be copied")
	}
	// sources untouched
	for _, p := range []string{keep, drop, uniq} {
		if !exists(p) {
			t.Fatalf("source %s was removed", p)
		}
	}
}

func TestExportSkippedCluster(t *testing.T) {
	in := t.TempDir()
	out := filepath.Join(t.TempDir(), "kept")
	a := filepath.Join(in, "a.jpg")
	b := filepath.Join(in, "b.jpg")
	writeFile(t, a, "A")
	writeFile(t, b, "B")

	recs := []scanner.ImageRecord{
		{ID: 1, AbsPath: a, Filename: "a.jpg", Width: 100, Height: 100},
		{ID: 2, AbsPath: b, Filename: "b.jpg", Width: 100, Height: 100},
	}
	clusters := []cluster.Cluster{{ID: "cluster_001", MemberIDs: []uint64{1, 2}}}
	dec := map[string]cluster.ClusterDecision{"cluster_001": {Skipped: true, Resolved: true}}

	cfg := config.Default()
	cfg.InputDir = in
	cfg.OutputDir = out
	rep, err := Export(cfg, recs, clusters, dec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Copied != 2 { // skipped cluster => keep all members
		t.Fatalf("copied = %d, want 2", rep.Copied)
	}
}

func TestExportCollisionSuffix(t *testing.T) {
	in := t.TempDir()
	out := filepath.Join(t.TempDir(), "kept")
	// same filename in two subdirs -> both unique keepers, names collide
	p1 := filepath.Join(in, "x", "img.jpg")
	p2 := filepath.Join(in, "y", "img.jpg")
	writeFile(t, p1, "ONE")
	writeFile(t, p2, "TWO")

	recs := []scanner.ImageRecord{
		{ID: 1, AbsPath: p1, Filename: "img.jpg", Width: 10, Height: 10},
		{ID: 2, AbsPath: p2, Filename: "img.jpg", Width: 10, Height: 10},
	}
	cfg := config.Default()
	cfg.InputDir = in
	cfg.OutputDir = out
	rep, err := Export(cfg, recs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Copied != 2 || rep.Collisions != 1 {
		t.Fatalf("copied=%d collisions=%d, want 2 and 1", rep.Copied, rep.Collisions)
	}
	if !exists(filepath.Join(out, "img.jpg")) || !exists(filepath.Join(out, "img (2).jpg")) {
		t.Fatal("expected img.jpg and img (2).jpg")
	}
}

func TestExportGuardrails(t *testing.T) {
	in := t.TempDir()
	cfg := config.Default()
	cfg.InputDir = in

	// empty output
	cfg.OutputDir = ""
	if _, err := Export(cfg, nil, nil, nil, nil); err == nil {
		t.Error("empty output dir should error")
	}
	// output == input
	cfg.OutputDir = in
	if _, err := Export(cfg, nil, nil, nil, nil); err == nil {
		t.Error("output == input should error")
	}
	// output is ancestor of input
	cfg.OutputDir = filepath.Dir(in)
	if _, err := Export(cfg, nil, nil, nil, nil); err == nil {
		t.Error("output as ancestor of input should error")
	}
}
