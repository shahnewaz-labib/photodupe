// Package hash computes perceptual hashes and finds Tier A (near-exact)
// duplicate pairs. See PLAN §6 Phase 2.
package hash

import (
	"image"
	"log/slog"
	"math/bits"
	"os"
	"runtime"
	"sort"
	"sync"

	"github.com/corona10/goimagehash"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

// Pair is a Tier A match: two image IDs and their Hamming distance. Stored with
// AID < BID so pairs are unique/undirected. See PLAN §5.2.
type Pair struct {
	AID      uint64 `json:"a_id"`
	BID      uint64 `json:"b_id"`
	Distance int    `json:"distance"`
}

type hashed struct {
	id   uint64
	hash uint64
}

// ComputeHash returns the 64-bit perception hash of the image at path.
func ComputeHash(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return 0, err
	}
	ph, err := goimagehash.PerceptionHash(img)
	if err != nil {
		return 0, err
	}
	return ph.GetHash(), nil
}

// FindPairs hashes every record (in parallel — decoding dominates) and returns
// all pairs with Hamming distance <= threshold. onProgress, if non-nil, is
// called as images are hashed.
//
// ponytail: naive O(n^2) comparison after hashing. Fine at the stated scale
// (tens of thousands: ~1e9 popcounts, a few seconds, one-time). Swap in a
// BK-tree or LSH banding if n grows past ~100k.
func FindPairs(recs []scanner.ImageRecord, threshold int, onProgress func(done, total int)) []Pair {
	hashes := hashAll(recs, onProgress)

	var pairs []Pair
	for i := 0; i < len(hashes); i++ {
		for j := i + 1; j < len(hashes); j++ {
			d := bits.OnesCount64(hashes[i].hash ^ hashes[j].hash)
			if d <= threshold {
				a, b := hashes[i].id, hashes[j].id
				if a > b {
					a, b = b, a
				}
				pairs = append(pairs, Pair{AID: a, BID: b, Distance: d})
			}
		}
	}
	return pairs
}

func hashAll(recs []scanner.ImageRecord, onProgress func(done, total int)) []hashed {
	workers := runtime.NumCPU()
	jobs := make(chan scanner.ImageRecord)
	results := make(chan hashed)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rec := range jobs {
				h, err := ComputeHash(rec.WorkPath)
				if err != nil {
					slog.Warn("phash", "path", rec.WorkPath, "err", err)
					continue
				}
				results <- hashed{id: rec.ID, hash: h}
			}
		}()
	}

	go func() {
		for _, rec := range recs {
			jobs <- rec
		}
		close(jobs)
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	out := make([]hashed, 0, len(recs))
	done := 0
	for h := range results {
		out = append(out, h)
		done++
		if onProgress != nil {
			onProgress(done, len(recs))
		}
	}
	// Stable order so pair output is deterministic across runs.
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}
