package review

import (
	"testing"

	"github.com/shahnewaz-labib/photodupe/internal/cluster"
	"github.com/shahnewaz-labib/photodupe/internal/config"
)

// carryDecisions must follow groups by membership, not by positional ID.
func TestCarryDecisionsFollowsMembers(t *testing.T) {
	t.Chdir(t.TempDir()) // SaveDecisions writes under data/

	old := []cluster.Cluster{
		{ID: "cluster_001", MemberIDs: []uint64{1, 2}},
		{ID: "cluster_002", MemberIDs: []uint64{3, 4}},
		{ID: "cluster_003", MemberIDs: []uint64{5, 6}},
		{ID: "cluster_004", MemberIDs: []uint64{7, 8}},
	}
	s := &Server{
		cfg: config.Default(),
		// After a stricter re-cluster: {1,2} dissolved, so everything shifts
		// down one ID; {5,6} gained a member; {7,8} is unchanged.
		clusters: []cluster.Cluster{
			{ID: "cluster_001", MemberIDs: []uint64{3, 4}},
			{ID: "cluster_002", MemberIDs: []uint64{5, 6, 9}},
			{ID: "cluster_003", MemberIDs: []uint64{7, 8}},
		},
		decisions: map[string]cluster.ClusterDecision{
			"cluster_001": {Skipped: true, Resolved: true}, // group dissolved: dropped
			"cluster_002": {KeeperID: 4, Resolved: true},   // keeper moves to new cluster_001
			"cluster_003": {Skipped: true, Resolved: true}, // members changed: "keep all" dropped
			"cluster_004": {Skipped: true, Resolved: true}, // unchanged group: kept, now cluster_003
		},
	}

	s.carryDecisions(old)

	want := map[string]cluster.ClusterDecision{
		"cluster_001": {KeeperID: 4, Resolved: true},
		"cluster_003": {Skipped: true, Resolved: true},
	}
	if len(s.decisions) != len(want) {
		t.Fatalf("decisions = %+v, want %+v", s.decisions, want)
	}
	for id, w := range want {
		if got := s.decisions[id]; got != w {
			t.Errorf("%s = %+v, want %+v", id, got, w)
		}
	}
	saved, err := cluster.LoadDecisions(cluster.StatePath)
	if err != nil || len(saved) != len(want) {
		t.Errorf("persisted decisions = %+v (err %v), want %d entries", saved, err, len(want))
	}
}
