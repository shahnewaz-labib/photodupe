// Package cluster merges Tier A (pHash) and Tier B (CLIP) pairs into duplicate
// groups via union-find. See PLAN §6 Phase 5.
package cluster

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/shahnewaz-labib/photodupe/internal/config"
	"github.com/shahnewaz-labib/photodupe/internal/hash"
)

// CachePath is the default clusters output location.
const CachePath = "data/clusters.json"

// SimPairsPath is where Tier B (CLIP) pairs are read from. vectordb writes this
// file in the SimPair shape.
const SimPairsPath = "data/tier_b.json"

// StatePath is where per-cluster review decisions persist. See PLAN §7.4.
const StatePath = "data/review_state.json"

// SimPair is a Tier B match: two image IDs and their cosine similarity. Stored
// with AID < BID. See PLAN §5.2. Defined here (the consumer) so vectordb can
// produce it without a dependency cycle.
type SimPair struct {
	AID   uint64  `json:"a_id"`
	BID   uint64  `json:"b_id"`
	Score float32 `json:"score"`
}

// Cluster is a duplicate group of size >= 2. See PLAN §5.3.
type Cluster struct {
	ID        string   `json:"id"`
	Tiers     []string `json:"tiers"` // contributing tiers: subset of {"A","B"}
	MemberIDs []uint64 `json:"member_ids"`
}

// ClusterDecision is the user's review choice for a cluster. See PLAN §7.4.
// KeeperID is serialized as a string (uint64 exceeds JS safe-int range).
type ClusterDecision struct {
	KeeperID uint64 `json:"keeper_id,string"` // 0 => fall back to the heuristic
	Skipped  bool   `json:"skipped"`          // true => declined: treat all members as uniques (keep all)
	Approved bool   `json:"approved"`         // true => user approved dropping the non-keepers
	Resolved bool   `json:"resolved"`         // user has approved or declined this cluster
}

// Build merges surviving Tier A and Tier B pairs into clusters. Thresholds are
// re-applied here so tightening a threshold in the UI re-filters cached pairs
// without re-hashing/re-embedding (PLAN §5 / §7.5). Pairs are only unioned when
// the corresponding tier is enabled.
func Build(tierA []hash.Pair, tierB []SimPair, cfg config.Config) []Cluster {
	// Assign a dense index to every id that appears in a surviving pair.
	idx := map[uint64]int{}
	var ids []uint64
	indexOf := func(id uint64) int {
		if i, ok := idx[id]; ok {
			return i
		}
		i := len(ids)
		idx[id] = i
		ids = append(ids, id)
		return i
	}

	type edge struct {
		a, b int
		tier string
	}
	var edges []edge

	if cfg.PHashEnabled {
		for _, p := range tierA {
			if p.Distance <= cfg.PHashThreshold {
				edges = append(edges, edge{indexOf(p.AID), indexOf(p.BID), "A"})
			}
		}
	}
	if cfg.ClipEnabled {
		for _, p := range tierB {
			if p.Score >= cfg.ClipSimilarityThresh {
				edges = append(edges, edge{indexOf(p.AID), indexOf(p.BID), "B"})
			}
		}
	}

	uf := newUnionFind(len(ids))
	for _, e := range edges {
		uf.union(e.a, e.b)
	}

	// Group members by root; collect contributing tiers per root.
	members := map[int][]uint64{}
	tiers := map[int]map[string]bool{}
	for i, id := range ids {
		r := uf.find(i)
		members[r] = append(members[r], id)
	}
	for _, e := range edges {
		r := uf.find(e.a)
		if tiers[r] == nil {
			tiers[r] = map[string]bool{}
		}
		tiers[r][e.tier] = true
	}

	var clusters []Cluster
	for r, mem := range members {
		if len(mem) < 2 {
			continue // uniques are handled by export, not stored as clusters
		}
		sort.Slice(mem, func(i, j int) bool { return mem[i] < mem[j] })
		clusters = append(clusters, Cluster{
			Tiers:     tierList(tiers[r]),
			MemberIDs: mem,
		})
	}

	// Deterministic order + stable IDs by smallest member id.
	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].MemberIDs[0] < clusters[j].MemberIDs[0]
	})
	for i := range clusters {
		clusters[i].ID = fmt.Sprintf("cluster_%03d", i+1)
	}
	return clusters
}

func tierList(set map[string]bool) []string {
	var out []string
	for _, t := range []string{"A", "B"} { // canonical order
		if set[t] {
			out = append(out, t)
		}
	}
	return out
}

// SaveClusters writes clusters as JSON, creating the parent dir if needed.
func SaveClusters(path string, clusters []Cluster) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(clusters, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// LoadClusters reads clusters previously written by SaveClusters.
func LoadClusters(path string) ([]Cluster, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var clusters []Cluster
	if err := json.Unmarshal(b, &clusters); err != nil {
		return nil, err
	}
	return clusters, nil
}

// LoadDecisions reads per-cluster review decisions. A missing file yields an
// empty map (nothing reviewed yet), not an error.
func LoadDecisions(path string) (map[string]ClusterDecision, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]ClusterDecision{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]ClusterDecision
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]ClusterDecision{}
	}
	return m, nil
}

// SaveDecisions writes per-cluster review decisions.
func SaveDecisions(path string, m map[string]ClusterDecision) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// LoadSimPairs reads Tier B pairs. A missing file yields nil (Tier B disabled or
// not yet computed), not an error.
func LoadSimPairs(path string) ([]SimPair, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pairs []SimPair
	if err := json.Unmarshal(b, &pairs); err != nil {
		return nil, err
	}
	return pairs, nil
}
