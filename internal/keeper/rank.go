// Package keeper picks which image in a duplicate group to keep. See PLAN §6a.
package keeper

import (
	"sort"

	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

// SuggestKeeper returns the best image to keep from members, applying the
// criteria in order (default ["resolution","filesize","earliest"]), with a
// deterministic lexicographic AbsPath tiebreak.
func SuggestKeeper(members []scanner.ImageRecord, order []string) scanner.ImageRecord {
	if len(members) == 0 {
		return scanner.ImageRecord{}
	}
	sorted := make([]scanner.ImageRecord, len(members))
	copy(sorted, members)

	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		for _, crit := range order {
			if c := compare(crit, a, b); c != 0 {
				return c < 0 // c<0 means a ranks ahead of b
			}
		}
		return a.AbsPath < b.AbsPath // deterministic final tiebreak
	})
	return sorted[0]
}

// compare returns <0 if a should rank ahead of b for the given criterion, >0 if
// behind, 0 if tied.
func compare(crit string, a, b scanner.ImageRecord) int {
	switch crit {
	case "resolution":
		return -cmp(int64(a.Width)*int64(a.Height), int64(b.Width)*int64(b.Height)) // higher first
	case "filesize":
		return -cmp(a.FileSize, b.FileSize) // larger first
	case "earliest":
		return cmp(a.CaptureTime.UnixNano(), b.CaptureTime.UnixNano()) // earlier first
	default:
		return 0 // unknown criterion is a no-op
	}
}

func cmp(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
