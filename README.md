# photodupe

Find duplicate and near-duplicate photos in a local folder, review them in a web
UI, and copy the ones you want to keep into a new folder. **Your source files are
never modified, moved, or deleted** — photodupe only reads them and writes copies
of the keepers elsewhere.

No cloud, no account, no Google Photos API. Everything runs locally.

## Status

- ✅ **Perceptual-hash deduplication (Tier A)** — catches exact and near-exact
  duplicates: re-encodes, resizes, quality changes, minor edits.
- 🚧 **CLIP semantic similarity (Tier B)** — "same scene, different shot" matching
  via image embeddings. Designed and stubbed, not wired yet. The toggle is
  visible but disabled in the UI.

## Requirements

- **Go 1.22+** to build.
- **libheif** (optional) for HEIC/HEIF (iPhone) photos. Without it, HEIC files are
  skipped with a count, not an error.
  - macOS: `brew install libheif`
  - Debian/Ubuntu: `apt install libheif-examples`
  - Fedora: `dnf install libheif-tools`
  - photodupe shells out to the `heif-convert` CLI from that package.

Pure Go otherwise — no Docker, no native libraries, no CGO for the Tier A
pipeline. Builds and runs on Linux, macOS, and Windows.

## Build

```sh
make build        # produces ./photodupe
# or: go build -o photodupe .
```

## Usage

The web UI is the easiest way in:

```sh
./photodupe ui        # serves http://localhost:8080
```

Then in the browser:
1. **Run** tab — pick your input folder and an (empty) output folder, click
   **Start dedup**, watch progress.
2. **Review** tab — each duplicate group shows its photos side by side with the
   suggested keeper highlighted. Change the keeper, or mark a group "not actually
   duplicates". Save.
3. **Settings** tab — tweak the pHash threshold, re-cluster instantly, and
   **copy keepers to the output folder**.

### CLI (scriptable)

Each stage can also run on its own; results are cached under `data/` so later
stages don't repeat work.

```sh
./photodupe scan   --input ~/photos     # find images -> data/scan_cache.json
./photodupe hash                        # perceptual hash -> data/tier_a.json
./photodupe cluster                     # group duplicates -> data/clusters.json
./photodupe export --output ~/deduped   # copy keepers + uniques
./photodupe ui                          # review in the browser

./photodupe all --input ~/photos --output ~/deduped   # run it all, then serve UI
```

## How it works

```
input folder
   │  scan     walk folder, read dimensions + capture time, convert HEIC
   ▼
   │  hash     64-bit perceptual hash per image; pairs within Hamming distance
   ▼           (Tier A) are near-duplicates
   │  cluster  union-find merges all matching pairs into duplicate groups
   ▼
   │  review   pick a keeper per group (web UI)
   ▼
   │  export   copy each group's keeper + every non-duplicate into the output dir
   ▼
output folder (deduplicated copies; originals untouched)
```

**Keeper heuristic** (default, overridable per group): highest resolution →
largest file size → earliest capture time → path order.

## Safety

- Source files are opened read-only. photodupe never writes into the input tree.
- Export refuses to run if the output folder is empty, equals the input folder,
  or is a parent of it.
- Output filename collisions get a numeric suffix (`img (2).jpg`); nothing is
  overwritten. Use a fresh output folder to avoid accumulating copies across
  re-exports.

## Parameters

Tunable in the Settings tab or `data/config.json`:

- `phash_threshold` (default 10) — max Hamming distance for a Tier A match.
  Lower = stricter. **Tightening re-clusters instantly; loosening beyond the last
  run needs a full re-run** (the UI warns you).
- `keeper_order` — the keeper ranking criteria.

## Data & caches

Everything runtime lives under `data/` (gitignored):

| file | contents |
|---|---|
| `scan_cache.json` | scanned image records |
| `tier_a.json` | perceptual-hash duplicate pairs |
| `clusters.json` | duplicate groups |
| `review_state.json` | your per-group keep/skip decisions (survives restarts) |
| `config.json` | current parameters |
| `heic_cache/` | JPEGs converted from HEIC |
| `thumbs/` | review thumbnails |

Delete `data/` to start over; your photos are untouched.
