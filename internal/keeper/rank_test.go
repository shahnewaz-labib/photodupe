package keeper

import (
	"testing"
	"time"

	"github.com/shahnewaz-labib/photodupe/internal/scanner"
)

func r(path string, w, h int, size int64, t time.Time) scanner.ImageRecord {
	return scanner.ImageRecord{AbsPath: path, Width: w, Height: h, FileSize: size, CaptureTime: t}
}

func TestSuggestKeeper(t *testing.T) {
	order := []string{"resolution", "filesize", "earliest"}
	early := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("resolution wins", func(t *testing.T) {
		got := SuggestKeeper([]scanner.ImageRecord{
			r("small", 100, 100, 9999, late),
			r("big", 200, 200, 1, early),
		}, order)
		if got.AbsPath != "big" {
			t.Fatalf("got %q, want big", got.AbsPath)
		}
	})

	t.Run("filesize breaks resolution tie", func(t *testing.T) {
		got := SuggestKeeper([]scanner.ImageRecord{
			r("lo", 100, 100, 10, late),
			r("hi", 100, 100, 999, late),
		}, order)
		if got.AbsPath != "hi" {
			t.Fatalf("got %q, want hi", got.AbsPath)
		}
	})

	t.Run("earliest breaks res+size tie", func(t *testing.T) {
		got := SuggestKeeper([]scanner.ImageRecord{
			r("newer", 100, 100, 10, late),
			r("older", 100, 100, 10, early),
		}, order)
		if got.AbsPath != "older" {
			t.Fatalf("got %q, want older", got.AbsPath)
		}
	})

	t.Run("path tiebreak is deterministic", func(t *testing.T) {
		got := SuggestKeeper([]scanner.ImageRecord{
			r("zzz", 100, 100, 10, early),
			r("aaa", 100, 100, 10, early),
		}, order)
		if got.AbsPath != "aaa" {
			t.Fatalf("got %q, want aaa", got.AbsPath)
		}
	})
}
