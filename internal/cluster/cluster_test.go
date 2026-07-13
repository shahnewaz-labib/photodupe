package cluster

import (
	"testing"

	"github.com/shahnewaz-labib/photodupe/internal/config"
	"github.com/shahnewaz-labib/photodupe/internal/hash"
)

func TestBuild(t *testing.T) {
	cfg := config.Default() // PHashThreshold=10, ClipThresh=0.92
	cfg.PHashEnabled = true
	cfg.ClipEnabled = true

	tierA := []hash.Pair{
		{AID: 1, BID: 2, Distance: 3}, // chain 1-2-3 => one cluster
		{AID: 2, BID: 3, Distance: 5},
		{AID: 6, BID: 7, Distance: 50}, // above threshold => dropped
	}
	tierB := []SimPair{
		{AID: 3, BID: 8, Score: 0.95}, // links 8 into the 1-2-3 group, adds tier B
		{AID: 10, BID: 11, Score: 0.99}, // separate Tier B cluster
		{AID: 20, BID: 21, Score: 0.5},  // below threshold => dropped
	}

	got := Build(tierA, tierB, cfg)

	if len(got) != 2 {
		t.Fatalf("got %d clusters, want 2: %+v", len(got), got)
	}

	// First cluster (smallest min id = 1): members {1,2,3,8}, tiers [A,B]
	c0 := got[0]
	if c0.ID != "cluster_001" {
		t.Errorf("first cluster ID = %q, want cluster_001", c0.ID)
	}
	if !equalIDs(c0.MemberIDs, []uint64{1, 2, 3, 8}) {
		t.Errorf("c0 members = %v, want [1 2 3 8]", c0.MemberIDs)
	}
	if !equalStr(c0.Tiers, []string{"A", "B"}) {
		t.Errorf("c0 tiers = %v, want [A B]", c0.Tiers)
	}

	// Second cluster: {10,11}, tier [B]
	c1 := got[1]
	if !equalIDs(c1.MemberIDs, []uint64{10, 11}) {
		t.Errorf("c1 members = %v, want [10 11]", c1.MemberIDs)
	}
	if !equalStr(c1.Tiers, []string{"B"}) {
		t.Errorf("c1 tiers = %v, want [B]", c1.Tiers)
	}
}

func TestBuildThresholdTightening(t *testing.T) {
	cfg := config.Default()
	cfg.PHashThreshold = 4 // tighter than the pair's distance

	tierA := []hash.Pair{{AID: 1, BID: 2, Distance: 8}}
	got := Build(tierA, nil, cfg)
	if len(got) != 0 {
		t.Fatalf("tightened threshold should drop the pair, got %+v", got)
	}
}

func TestBuildTierDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.PHashEnabled = false
	cfg.ClipEnabled = true

	tierA := []hash.Pair{{AID: 1, BID: 2, Distance: 1}}
	tierB := []SimPair{{AID: 3, BID: 4, Score: 0.99}}
	got := Build(tierA, tierB, cfg)
	if len(got) != 1 || !equalIDs(got[0].MemberIDs, []uint64{3, 4}) {
		t.Fatalf("disabling Tier A should leave only the Tier B cluster, got %+v", got)
	}
}

func equalIDs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
