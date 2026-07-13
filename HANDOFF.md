# photodupe — Handoff / Status

**Audience:** the next engineer or agent picking this up. Read this together with
`PLAN.md` (the original full design). Where this document and `PLAN.md` disagree,
**this document wins** — it records what was actually built and the decisions made
along the way. `PLAN.md` is the aspirational full design (including CLIP/Qdrant,
which are not built yet).

Repo: `github.com/shahnewaz-labib/photodupe` (private). Branch: `master`.
First commit: `init` (`1e343c3`).

---

## 1. What this tool is

A local, cross-platform Go CLI + web-UI that finds duplicate/near-duplicate
images in a folder and copies the **keepers** into a new folder. It never
modifies, moves, or deletes source files. No cloud, no Google API, no account.

Two intended similarity tiers:
- **Tier A — perceptual hash (pHash):** built and working. Catches exact and
  near-exact duplicates (re-encode, resize, quality change, small edits).
- **Tier B — CLIP semantic similarity:** designed, stubbed, **not built**. The
  seam exists (`pipeline.Embedder` interface, disabled UI toggle, `tier_b.json`
  loading in the clusterer), but there is no embedder implementation and no
  Qdrant wiring.

---

## 2. Current status: what is DONE

Build order followed `PLAN.md §8` but front-loaded the pure-Go path so a usable
pHash-only tool shipped first. All of the following is implemented, tested, and
was verified end-to-end (unit tests + a real-browser Playwright run of the whole
UI flow).

| Phase | Package(s) | State |
|---|---|---|
| 0 Scaffold | `main.go`, `internal/config` | ✅ |
| 1 Scanner | `internal/scanner` | ✅ (incl. HEIC via `heif-convert`, sidecar/EXIF/mtime capture time) |
| 2 pHash / Tier A | `internal/hash` | ✅ (parallel hashing, naive O(n²) pairing) |
| 5 Clustering | `internal/cluster` | ✅ (union-find, tier tagging, threshold re-filter) |
| 6 Keeper + Export | `internal/keeper`, `internal/export` | ✅ (copy-only, guardrails, collision suffix) |
| 7a UI Run + Review | `internal/review` | ✅ |
| 7b Thumbnails + warning | `internal/review`, `internal/pipeline` | ✅ |
| 8 Pipeline/CLI/docs | `internal/pipeline`, `main.go`, `README.md` | ✅ |

**Tests:** 16 pass (`go test ./...`). Includes a full-pipeline integration test
(`internal/pipeline/pipeline_test.go`) that runs scan→hash→cluster in an isolated
temp cwd. Build and `go vet` are clean.

### Verified behaviors (not just "tests pass")
- Scanned a real macOS HEIC → converted to JPEG cache, dimensions read from the
  conversion, `work_path` points at the cache.
- Real photo + JPEG-recompressed copy → Hamming distance 0, clustered together;
  a 180°-rotated variant correctly NOT matched (pHash is not rotation-invariant —
  expected).
- Full UI in a browser: pick folders → run → live progress → review grid with
  rendered thumbnails and highlighted keeper → save decision (persisted) →
  export. Export kept the chosen keeper + uniques, dropped duplicates, left the
  source tree untouched.
- Thumbnail endpoint returns ~70× smaller images than full.
- Loosening warning fires when the pHash threshold is raised above the last run's.

### Bugs found and fixed during verification (so you don't re-introduce them)
1. **uint64 IDs lose precision in JavaScript** (`...522888` became `...523000`).
   Image IDs and `keeper_id` are serialized as **JSON strings** (`json:",string"`
   on `memberView.ID` and `cluster.ClusterDecision.KeeperID`), and `app.js` sends
   `keeper_id` as a string. Do not "simplify" these back to numbers.
2. **Image serving 403'd after a server restart** because the path-traversal
   guard needs `cfg.InputDir`, which was empty when the UI started fresh. Fixed by
   persisting config to `data/config.json` on `scan`/`export`/`all` and from the
   UI's `POST /api/config`. If you change how config is loaded, keep this.
3. **Bad test fixture:** a smooth gradient is pathological for DCT-based pHash
   (too many coefficients near the median threshold → unstable hash). Tests use a
   high-contrast "blocks" pattern instead. Use structured fixtures for pHash tests.

---

## 3. Architecture as built

```
input folder
  │ scan     internal/scanner   -> data/scan_cache.json  ([]ImageRecord)
  ▼ hash     internal/hash      -> data/tier_a.json       ([]Pair)  + data/run_meta.json
  ▼ cluster  internal/cluster   -> data/clusters.json     ([]Cluster)
  ▼ review   internal/review    -> data/review_state.json (decisions)   [web UI]
  ▼ export   internal/export    -> OUTPUT dir (copies)
```

`internal/pipeline` orchestrates scan→hash→(embed)→cluster and owns the
thread-safe `Progress` that the UI polls. `main.go` is stdlib-`flag` subcommand
dispatch.

### Package responsibilities
- **config** — `Config` struct, JSON load/save, defaults. Precedence: CLI flag >
  `data/config.json` > defaults.
- **scanner** — walk dir, match image extensions, FNV-1a(abspath) → `ID`, capture
  time (sidecar JSON > EXIF > mtime), dimensions via `image.DecodeConfig`, HEIC →
  cached JPEG via `heif-convert`.
- **hash** — perceptual hash (`corona10/goimagehash`), parallel over CPUs,
  pairwise Hamming, emit pairs ≤ threshold.
- **cluster** — union-find merge of Tier A + Tier B pairs, threshold re-filter at
  cluster time, tier tagging, stable `cluster_NNN` IDs. **Also the home of
  `SimPair`, `ClusterDecision`, and all the JSON load/save for clusters, sim
  pairs, and review state** (kept here to avoid an import cycle — `export` and
  `review` both import `cluster`; `cluster` imports only `hash` + `config`).
- **keeper** — `SuggestKeeper(members, order)`: resolution → filesize → earliest →
  path tiebreak.
- **export** — copy keepers + uniques to output dir; guardrails; collision suffix;
  preserves mtime; never touches source.
- **pipeline** — `RunAll`, `Recluster`, `Progress`/`Snapshot`, `RunMeta` +
  `LooseningWarning`, and the `Embedder` interface (the Tier B seam).
- **review** — `net/http` server (Go 1.22 method+wildcard routing), `embed.FS` for
  `templates/` + `static/`, all API handlers, thumbnail generation.

### Data contracts (as built — note deviations from PLAN.md)
- `scanner.ImageRecord` — matches `PLAN §5.1`.
- `hash.Pair{AID,BID uint64; Distance int}` — `a_id`/`b_id` JSON keys (PLAN used
  slightly different names; the built names are authoritative).
- `cluster.SimPair{AID,BID uint64; Score float32}` — Tier B pair shape; a future
  embedder must write `data/tier_b.json` in exactly this shape.
- `cluster.Cluster{ID string; Tiers []string; MemberIDs []uint64}` — matches PLAN.
- `cluster.ClusterDecision{KeeperID uint64 (json ",string"); Skipped, Resolved
  bool}` — `keeper_id` is a **string** in JSON.

### HTTP API (all under the UI server)
| Method + path | Purpose |
|---|---|
| `GET /` | single-page UI |
| `GET /static/...` | assets |
| `GET /api/status` | pipeline progress snapshot (polled ~1s) |
| `POST /api/run` | start scan→hash→cluster (async, 409 if already running) |
| `POST /api/recluster` | re-cluster from cached pairs; returns `{clusters, warning}` |
| `GET /api/clusters` | clusters with member details + suggested keeper + decision |
| `POST /api/clusters/{id}/decision` | save a `ClusterDecision` |
| `GET /api/image?id=&thumb=` | serve full image or cached thumbnail |
| `GET /api/browse?path=` | list subdirectories (server-side folder picker) |
| `GET /api/config` / `POST /api/config` | read/write params |
| `POST /api/export` | copy keepers → output dir; returns `export.Report` |

---

## 4. Deviations from PLAN.md (decisions made during build)

1. **CLIP/Qdrant deferred.** User explicitly chose to ship pHash-only. `PLAN.md`
   Phases 3 (CLIP/ONNX) and 4 (Qdrant) are not built. `Config.ClipEnabled`
   defaults to **false**; the UI's CLIP controls are **disabled** with a
   "coming soon" note.
2. **Most of Phase 7b folded into 7a.** The params form, `POST /api/recluster`,
   and the export trigger were built during the UI phase. Only thumbnails and the
   loosening warning were left for 7b (both now done).
3. **No stub files for unbuilt packages.** `internal/vectordb`, `internal/embed`,
   `scripts/convert_clip_to_onnx.py`, `scripts/run-qdrant.sh`, `models/` — none of
   these exist yet (YAGNI). Create them when building Tier B.
4. **pHash pairing is naive O(n²)** after hashing (see the `ponytail:` comment in
   `internal/hash/phash.go`). Fine for tens of thousands; swap to a BK-tree/LSH if
   n grows past ~100k.
5. **Folder picker** is a server-side directory browser (`/api/browse`) plus a
   free-text path field — browsers can't hand the server a real filesystem path
   from a native picker.
6. **`ClusterDecision`/`SimPair`/state IO live in `cluster`**, not `review`, to
   avoid an import cycle. `PLAN §7.4` implied `review` owns review state; the build
   put the types + persistence in `cluster` and `review` calls into it.

---

## 5. What's NEXT (in priority order)

### 5.1 Tier B — CLIP semantic similarity (the big one)
This is `PLAN.md` Phases 3 + 4. The integration seam is ready:
`pipeline.RunAll` already calls `embedder.Run(cfg, recs, onProgress)` when
`cfg.ClipEnabled && embedder != nil`, and the clusterer already unions
`tier_b.json` pairs. To finish:

1. **Model prep** (`scripts/convert_clip_to_onnx.py`) — prefer a pre-converted
   ONNX CLIP ViT-B/32 from Hugging Face; else export `get_image_features`
   (512-dim). Verify output dim is 512. See `PLAN §6 Phase 3a`.
2. **`internal/embed`** — `preprocess.go` (resize shorter→224, center-crop, CHW
   RGB float32, CLIP mean/std) + `clip.go` (`yalue/onnxruntime_go` session).
   **Write the preprocessing-parity test first** (compare against Python
   `CLIPImageProcessor` within 1e-4) — this is the single highest-risk correctness
   bug. See `PLAN §6 Phase 3b/3c`.
3. **`internal/vectordb`** — Qdrant client (`qdrant/go-client`): init collection
   (512, Cosine), upsert (point ID = `ImageRecord.ID`), kNN search. `scripts/
   run-qdrant.sh` for the Docker container. See `PLAN §6 Phase 4`.
4. **Implement `pipeline.Embedder`** in a new type that ties embed → upsert →
   search → writes `data/tier_b.json` as `[]cluster.SimPair` (AID<BID, dedup
   symmetric). Wire it in `main.go` `cmdUI`/`cmdAll` (currently pass `nil`).
5. **Wire the `embed` CLI subcommand** (currently prints "not implemented").
6. **Re-enable the CLIP UI controls** in `internal/review/templates/index.html`
   (remove the `disabled` attrs + "coming soon" note) and flip
   `Config.ClipEnabled` default back to true once it works.
7. **Extend `RunMeta`/`LooseningWarning`** to also cover the CLIP threshold
   (currently only tracks `phash_threshold`).

Cross-platform caveat: this adds the onnxruntime **native shared library** (per-OS
binary, referenced via `Config.OnnxLibPath` / `ort.SetSharedLibraryPath`) and
Docker for Qdrant. Document per-OS setup. Linux is the guaranteed target.

### 5.2 Smaller follow-ups / known rough edges
- **Export is a separate manual step** from saving decisions. A user hit this
  confusion: "Save decision" only records the choice; you must click **Export**
  (Settings tab) to actually copy files. Consider: rename the button to "Export
  kept photos → output folder", and/or add an export button on the Review tab,
  and/or show a "N clusters resolved, M pending" counter.
- **Re-exporting into a non-empty output dir accumulates `(2)` copies** (collision
  suffixing, by design — never overwrites). Consider warning if the output dir is
  non-empty, or offer "clear output first" (destructive — guard carefully).
- **Default port 8080 collides with Docker** on this machine. Consider changing
  the default `ServerAddr` to something less contested, or auto-pick a free port.
- **No pagination/virtualization** in the Review tab. Thousands of clusters ×
  many images will be heavy in the DOM. Add lazy rendering / pagination if needed.
- **`heif-convert` runs one process per HEIC file** (serial in the scan loop).
  Fine for now; parallelize if HEIC-heavy libraries are slow.
- **No "resolved/pending" filter** in Review, and no next/prev navigation between
  clusters (PLAN §7.1 mentioned these). Currently it renders all clusters in one
  scrolling list.

---

## 6. Assumptions baked in (verify before relying on them)

**Environment**
- Go 1.22+ (developed/tested on 1.25.1, darwin/arm64). Uses Go 1.22 `net/http`
  method+wildcard routing (`"POST /api/clusters/{id}/decision"`) and `embed.FS`.
- `heif-convert` (from libheif) is optional; absent → HEIC files skipped with a
  count, never a crash. Detected via `Config.HeifConvertPath` or
  `exec.LookPath("heif-convert")`.
- All runtime state is written to a **relative `data/` directory** (constants like
  `"data/scan_cache.json"`). The tool therefore assumes it is run from a stable
  working directory. The integration test `os.Chdir`s into a temp dir to isolate.
  If you add a `--data-dir` flag, thread it through all the `*Path` constants.

**Data**
- Input is a plain folder of loose image files, possibly nested. **Google Takeout
  structure is NOT required.** Sidecar JSON (`<file>.json` or
  `<file>.supplemental-metadata.json`) is read opportunistically for capture time
  if present; absence is normal.
- Supported extensions (case-insensitive): `.jpg .jpeg .png .gif .webp .bmp .heic
  .heif`. RAW/proprietary formats are out of scope. Video is out of scope.
- Scale assumption: up to tens of thousands of images. In-memory union-find and
  naive O(n²) pHash pairing are adequate at that scale. Beyond ~100k, revisit
  (see §5.1 note and the `ponytail:` comment in `phash.go`).
- `ImageRecord.ID = FNV-1a(absolute path)`. **IDs are tied to absolute paths** —
  moving the input folder changes all IDs (re-scan needed). This is intentional
  (idempotent upserts keyed by path) but means caches are not portable across
  machines/paths.

**Behavior / safety (do not weaken)**
- Source files are read-only. Export writes only copies.
- Export refuses: empty output dir, output == input, output is an ancestor of
  input. (It does **not** currently forbid output being *inside* input — a
  possible future guard; see `export.validateDirs`.)
- "Keeper" default ranking: resolution → filesize → earliest capture → path.
- Nothing is ever deleted anywhere.

**Frontend**
- Vanilla JS, no build step, no framework, no npm. Served from `embed.FS`.
- uint64 IDs cross the JSON boundary as **strings** (see §2 bug #1).
- Progress is **polled** (`GET /api/status` every ~1s), not SSE/WebSocket.

**Thresholds (starting points, tune empirically)**
- pHash Hamming ≤ 10. CLIP cosine ≥ 0.92 (unused until Tier B is built).

---

## 7. How to run, build, test, verify

```sh
make build                 # -> ./photodupe   (or: go build -o photodupe .)
make test                  # go test ./...    (16 tests)
go vet ./...

./photodupe ui --addr localhost:8090        # web UI (8080 collides with Docker here)
./photodupe all --input ~/photos --output ~/deduped --addr localhost:8090
# per-stage: scan --input DIR | hash | cluster | export --output DIR
```

To verify a change end-to-end (the way it was done here): generate a few images
with a known duplicate (e.g. `sips` on macOS to make a resized/recompressed
copy), run the pipeline, and drive the UI in a browser (Playwright MCP was used).
Confirm: cluster forms, keeper highlighted, export copies keepers + uniques,
**source files unchanged**.

`data/` is gitignored; delete it to start clean. Source photos are never touched.

---

## 8. File map (quick reference)

```
main.go                         CLI dispatch: scan/hash/cluster/export/ui/all
internal/config/config.go       Config + load/save + defaults
internal/scanner/               scanner.go (walk, HEIC, metadata), cache.go
internal/hash/                  phash.go (hash+pair), cache.go
internal/cluster/               unionfind.go, cluster.go (Build + all JSON IO + types)
internal/keeper/rank.go         SuggestKeeper heuristic
internal/export/export.go       copy keepers, guardrails
internal/pipeline/              pipeline.go (RunAll/Recluster + Embedder seam),
                                progress.go, meta.go (RunMeta + LooseningWarning)
internal/review/                server.go, handlers.go, thumb.go,
                                templates/index.html, static/app.js, static/style.css
PLAN.md                         original full design (incl. unbuilt CLIP/Qdrant)
README.md                       user-facing docs
HANDOFF.md                      this file
```
