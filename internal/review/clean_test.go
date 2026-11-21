package review

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/shahnewaz-labib/photodupe/internal/cluster"
	"github.com/shahnewaz-labib/photodupe/internal/config"
	"github.com/shahnewaz-labib/photodupe/internal/hash"
	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

// Clean-up moves only the left-out copies of approved groups, keeps their
// subfolders, logs every move, and rebuilds the groups without them.
func TestCleanMovesApprovedExtras(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	in := filepath.Join(root, "photos")

	write := func(rel string) string {
		p := filepath.Join(in, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rec := func(id uint64, rel string, w int) scanner.ImageRecord {
		p := write(rel)
		return scanner.ImageRecord{ID: id, AbsPath: p, WorkPath: p, Filename: filepath.Base(p), Width: w, Height: w, FileSize: int64(len(rel))}
	}
	recs := []scanner.ImageRecord{
		rec(1, "2020/a.jpg", 200), // approved group: keeper (bigger)
		rec(2, "2020/a-small.jpg", 100),
		rec(3, "dup/a.jpg", 100),  // same name as another drop elsewhere is fine
		rec(4, "2021/b.jpg", 200), // pending group: untouched
		rec(5, "2021/b2.jpg", 100),
		rec(6, "2022/c.jpg", 200), // declined group: untouched
		rec(7, "2022/c2.jpg", 100),
	}
	pairs := []hash.Pair{{AID: 1, BID: 2}, {AID: 1, BID: 3}, {AID: 4, BID: 5}, {AID: 6, BID: 7}}

	cfg := config.Default()
	cfg.InputDir = in
	must(t, scanner.SaveCache(scanner.CachePath, recs))
	must(t, hash.SavePairs(hash.CachePath, pairs))
	clusters := cluster.Build(pairs, nil, cfg)
	must(t, cluster.SaveClusters(cluster.CachePath, clusters))

	s := New(cfg, nil)
	find := func(id uint64) string {
		for _, c := range s.clusters {
			if containsID(c.MemberIDs, id) {
				return c.ID
			}
		}
		t.Fatalf("no cluster holds %d", id)
		return ""
	}
	s.decisions[find(1)] = cluster.ClusterDecision{Approved: true, Resolved: true}
	s.decisions[find(6)] = cluster.ClusterDecision{Skipped: true, Resolved: true}
	pendingKeeper := find(4)
	s.decisions[pendingKeeper] = cluster.ClusterDecision{KeeperID: 5}
	must(t, cluster.SaveDecisions(cluster.StatePath, s.decisions)) // the UI saves every decision

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/clean", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var resp cleanResp
	must(t, json.Unmarshal(w.Body.Bytes(), &resp))
	dest := in + "-removed"
	if resp.Moved != 2 || resp.Groups != 1 || resp.Dest != dest || len(resp.Failed) != 0 {
		t.Fatalf("resp = %+v", resp)
	}

	for _, rel := range []string{"2020/a-small.jpg", "dup/a.jpg"} {
		if _, err := os.Stat(filepath.Join(in, rel)); !os.IsNotExist(err) {
			t.Errorf("%s still in the photo folder", rel)
		}
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("%s not in the removed folder: %v", rel, err)
		}
	}
	for _, rel := range []string{"2020/a.jpg", "2021/b.jpg", "2021/b2.jpg", "2022/c.jpg", "2022/c2.jpg"} {
		if _, err := os.Stat(filepath.Join(in, rel)); err != nil {
			t.Errorf("%s should not have moved: %v", rel, err)
		}
	}

	if len(s.clusters) != 2 {
		t.Errorf("clusters after clean = %+v, want the 2 untouched groups", s.clusters)
	}
	if len(s.records) != 5 {
		t.Errorf("records after clean = %d, want 5", len(s.records))
	}
	if got := s.decisions[find(6)]; !got.Skipped {
		t.Errorf("declined decision lost: %+v", got)
	}
	if got := s.decisions[find(4)]; got.KeeperID != 5 {
		t.Errorf("picked keeper lost: %+v", got)
	}

	f, err := os.Open(RemovedLogPath)
	must(t, err)
	defer f.Close()
	lines := 0
	for sc := bufio.NewScanner(f); sc.Scan(); lines++ {
	}
	if lines != 2 {
		t.Errorf("move log has %d lines, want 2", lines)
	}

	// Running it again has nothing left to do.
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/clean", nil))
	must(t, json.Unmarshal(w.Body.Bytes(), &resp))
	if resp.Moved != 0 {
		t.Errorf("second clean moved %d files", resp.Moved)
	}
}

// The removed folder may not sit inside the photo folder: a re-scan would
// find the moved copies again.
func TestCleanRefusesRemovedDirInsideInput(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	cfg := config.Default()
	cfg.InputDir = filepath.Join(root, "photos")
	cfg.RemovedDir = filepath.Join(root, "photos", "removed")
	must(t, os.MkdirAll(cfg.InputDir, 0o755))
	s := New(cfg, nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/clean", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
