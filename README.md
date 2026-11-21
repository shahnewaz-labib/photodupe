# photodupe

Find duplicate and near-duplicate photos in a local folder, review them in a web
UI, and copy the ones you want to keep into a new folder. **Nothing is ever
deleted.** photodupe only reads your source folder, with one opt-in exception:
**Clean up approved** moves the left-out copies of groups you approved into a
separate holding folder, from which they can be moved back.

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

Then in the browser, four steps:
1. **Choose photos**: pick your photo folder. photodupe counts the photos
   straight away. Optionally set match strictness (Strict / Standard / Loose).
2. **Scan**: watch progress as it reads, compares and groups your photos.
3. **Results**: see how many extra copies were found and how much space a
   clean copy saves. photodupe has already picked the best copy in each group,
   so reviewing is optional. Switch strictness here, and the numbers update
   instantly. To check the picks, open **Review groups**: click a photo (or press
   `1`–`9`) to keep it, then **Approve** (`A`) or **Decline, keep all** (`D`, for
   "not duplicates"). Both move on to the next group. `Space` compares side by
   side, `←` / `→` go back or skip, and `Z` undoes. Tabs filter by To review,
   Worth a look, Approved and Declined.
   - **Clean up approved** (optional, any time) moves the left-out copies of
     every approved group out of your photo folder into `<photo folder>-removed`
     (or `removed_dir` in `config.json`). Subfolders are kept, nothing is
     deleted, the photo you kept stays put, and every move is logged in
     `data/removed_log.jsonl`. Cleaned groups disappear from the review.
4. **Save copies**: pick an output folder (asked only here), see exactly what
   will be copied and left out, then copy. When it's done, **Open folder**
   opens the result.

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

- Source files are opened read-only, and nothing is ever deleted. The only
  action that changes the input folder is **Clean up approved**: it *moves*
  the left-out copies of approved groups to the holding folder, and only after
  checking that the photo being kept still exists. The holding folder may not
  be inside the input folder (a re-scan would find the copies again).
- Export refuses to run if the output folder is empty, equals the input folder,
  or is a parent of it.
- Output filename collisions get a numeric suffix (`img (2).jpg`); nothing is
  overwritten. Use a fresh output folder to avoid accumulating copies across
  re-exports.

## Parameters

Tunable in the Settings tab or `data/config.json`:

- `phash_threshold` (default 10) — max Hamming distance for a Tier A match.
  Lower = stricter. The UI shows this as Strict (5) / Standard (10) / Loose (14).
  Scans cache pairs up to the Loose level, so switching between those levels
  re-clusters instantly; going looser than the cached level needs a re-scan
  (the UI offers it). Saved review decisions follow their groups across
  re-clusters.
- `keeper_order` — the keeper ranking criteria.

## Data & caches

Everything runtime lives under `data/` (gitignored):

| file | contents |
|---|---|
| `scan_cache.json` | scanned image records |
| `tier_a.json` | perceptual-hash duplicate pairs |
| `clusters.json` | duplicate groups |
| `review_state.json` | your per-group keeper and approve/decline decisions (survives restarts) |
| `removed_log.jsonl` | one line per file moved by Clean up approved (`from`, `to`, `time`) |
| `config.json` | current parameters |
| `heic_cache/` | JPEGs converted from HEIC |
| `thumbs/` | review thumbnails |

Delete `data/` to start over; your photos are untouched (keep
`removed_log.jsonl` if you may want to move cleaned-up copies back).
