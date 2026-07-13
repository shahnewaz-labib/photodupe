# photodupe — Implementation Plan (v2)

> This document is the single source of truth for the implementing agent. It is
> self-contained: you do not need any prior conversation or external context to
> build from it. Where a decision has been made, it is stated as a decision, not
> an option. Where something must be verified against real data or a live API at
> build time, it is called out explicitly. Do not silently assume — if reality
> contradicts this document, stop and flag it.

---

## 0. Purpose

A local, cross-platform CLI + web-UI tool (Go) that:

1. Scans a user-selected local folder of images.
2. Detects exact/near-exact duplicates (perceptual hash) **and** semantically
   similar images (CLIP embeddings via ONNX Runtime).
3. Clusters matches into duplicate groups.
4. Serves a local web UI where the user can:
   - Select the input folder and output folder.
   - Tweak detection parameters.
   - Start the dedup run and watch live progress.
   - Review each duplicate group (a photo shown alongside the photos determined
     to be its duplicates) and choose which to keep.
5. On the user's confirmation, **copies the kept images into a new output
   directory**. Originals are never modified, moved, or deleted.

**There is no Google Photos integration of any kind.** No API, no OAuth, no
uploading, no downloading, no online deletion. The tool operates purely on local
files: read from the input directory, write copies to the output directory. This
is a hard scope boundary — do not add any network/cloud photo-service code.

---

## 1. Locked Decisions

These were decided explicitly. Do not deviate without asking the user.

| Topic | Decision |
|---|---|
| **Output behavior** | **Copy keepers only.** For every duplicate group, copy the chosen keeper into the output directory. Every image that is *not* part of any duplicate group (a "unique") is also copied. Rejected duplicates are **not** copied. Source files are never modified, moved, or deleted. |
| **Similarity engines** | **pHash + CLIP both.** Perceptual hash (Tier A) and CLIP semantic similarity (Tier B). CLIP requires ONNX Runtime (native shared lib + CGO) and a model file. |
| **HEIC/HEIF support** | **Supported.** Convert via libheif's `heif-convert` CLI tool. If the tool is not installed, HEIC files are skipped with a warning + count (never a hard crash). |
| **Vector store** | **Qdrant via Docker.** Local single-node, no auth, collection `photo_embeddings`, 512-dim, Cosine distance. |
| **Deletion** | **None.** The tool never deletes anything, anywhere. "Rejecting" a duplicate simply means it is not copied to the output directory. |
| **Cross-platform target** | **Linux is the primary, must-work target.** macOS and Windows are best-effort: the Go code must be written portably (no Linux-only syscalls, use `filepath` not hardcoded `/`, etc.) and the plan documents per-OS setup, but if a platform-specific issue arises, Linux takes priority. |

---

## 2. Assumptions (flag if any are wrong)

**Environment**
- Development/target machine: Linux primary (macOS/Windows best-effort). Docker
  available (for Qdrant).
- Go 1.22+ installed.
- Internet access available during **setup only** (download ONNX model, Go
  modules, Docker image, onnxruntime shared lib). The tool runs fully offline at
  runtime.
- `python3` + `pip` available for the **one-time** ONNX model prep step only
  (not used at runtime; only if a pre-converted model can't be downloaded).

**Data**
- The user places images into a local folder (any structure, may be nested) and
  points the tool at it via the UI. There is **no** requirement for Google
  Takeout structure, sidecar JSON files, or any particular naming — those are
  **not assumed to exist**. (If sidecar JSON *does* exist, we opportunistically
  read timestamps from it — see Phase 1 — but the tool must work with a plain
  folder of loose image files and nothing else.)
- Photo count: up to "tens of thousands". Single local Qdrant instance +
  in-memory union-find are sufficient. If the count exceeds ~500k, the pHash
  pairwise strategy and batching would need revisiting — flag if discovered.
- Images are standard consumer formats: `.jpg`, `.jpeg`, `.png`, `.gif`,
  `.webp`, `.bmp`, `.heic`, `.heif` (case-insensitive). RAW/proprietary formats
  are out of scope.

**Keeper heuristic (default, user-overridable per group in the UI)**
Order of preference for choosing which image in a group to keep:
1. Highest pixel resolution (`Width * Height`).
2. Largest file size (bytes).
3. Earliest capture timestamp (see Phase 1 for how `CaptureTime` is derived).
Ties broken by lexicographic path order (deterministic).

**CLIP / ONNX**
- Model: OpenAI CLIP **ViT-B/32**, vision tower, exported to ONNX, producing the
  final **512-dim** projected image embedding (via `get_image_features`, not the
  raw pooled vision output — see Phase 3a). The number **512** is hardcoded in
  the Qdrant collection config and buffer sizes; if a different variant is used,
  update every occurrence.
- Preprocessing (must be replicated exactly in Go — mismatch silently produces
  garbage embeddings): resize shorter side to 224 preserving aspect ratio,
  center-crop to 224×224, convert to CHW float32, normalize with
  `mean = [0.48145466, 0.4578275, 0.40821073]`,
  `std  = [0.26862954, 0.26130258, 0.27577711]`.

**Thresholds (starting points — expected to be tuned via the UI)**
- **Tier A (pHash):** Hamming distance ≤ **10** (out of 64 bits).
- **Tier B (CLIP):** cosine similarity ≥ **0.92**.

---

## 3. Architecture Overview

```
User picks input dir + output dir + params in the Web UI
        │
        ▼
┌──────────────┐
│  scanner/    │  walk input dir, find images, convert HEIC, read metadata
└──────┬───────┘
       │  []ImageRecord (cached to data/scan_cache.json)
       ├───────────────────────────┐
       ▼                           ▼
┌──────────────┐          ┌──────────────┐
│  hash/       │          │  embed/      │  CLIP preprocessing + ONNX inference
│  pHash       │          │  CLIP+ONNX   │
└──────┬───────┘          └──────┬───────┘
       │ Tier A pairs            │ 512-d vectors
       │ (data/tier_a.json)      ▼
       │                  ┌──────────────┐
       │                  │  vectordb/   │  Qdrant: upsert + kNN search
       │                  │  Qdrant      │
       │                  └──────┬───────┘
       │                         │ Tier B pairs (data/tier_b.json)
       ▼                         ▼
      ┌────────────────────────────────┐
      │  cluster/ (union-find)         │  merge Tier A + Tier B into groups
      │  → data/clusters.json          │  (CHEAP: re-runnable on param change
      └───────────────┬────────────────┘   without re-hashing/re-embedding)
                      ▼
              ┌───────────────┐
              │  keeper/      │  suggest keeper per group
              └───────┬───────┘
                      ▼
              ┌───────────────┐
              │  review/      │  Web UI: progress, review, params, export
              │  HTTP server  │
              └───────┬───────┘
                      │ user confirms keepers
                      ▼
              ┌───────────────┐
              │  export/      │  copy keepers + uniques → output dir
              └───────────────┘
```

**Key performance principle:** the expensive stages (hashing, embedding) write
their results to cache files and Qdrant. The cheap stage (clustering) reads those
caches. Changing a **threshold parameter** must only re-run clustering (and, for
the CLIP threshold, re-query Qdrant) — it must **not** force a re-scan, re-hash,
or re-embed. This makes UI parameter tuning fast. See Phase 5 and Phase 7.

---

## 4. Repository Structure

```
photodupe/
  go.mod
  go.sum
  main.go                       # CLI entrypoint: subcommand dispatch (stdlib flag)
  internal/
    config/
      config.go                 # Config struct + load/save (JSON file + flag/env override)
    scanner/
      scanner.go                # walk input dir, find images + optional sidecar, convert HEIC, metadata
      scanner_test.go
    hash/
      phash.go                  # goimagehash wrapper + pairwise Tier A search
      phash_test.go
    embed/
      preprocess.go             # resize/crop/normalize → []float32 CHW
      clip.go                   # ONNX Runtime session mgmt + Embed()
      preprocess_test.go        # parity test vs Python reference (highest-risk test)
      clip_test.go
    vectordb/
      qdrant.go                 # init collection, upsert, kNN search
      qdrant_test.go
    cluster/
      unionfind.go
      cluster.go                # combine Tier A + Tier B → groups
      cluster_test.go
    keeper/
      rank.go                   # keeper-selection heuristic
      rank_test.go
    export/
      export.go                 # copy keepers + uniques → output dir
      export_test.go
    pipeline/
      pipeline.go               # orchestrates scan→hash→embed→cluster with progress reporting
      progress.go               # thread-safe progress state (phase, counts, %) read by the UI
    review/
      server.go                 # net/http server + JSON API + static file serving
      handlers.go               # route handlers
      templates/
        index.html              # single-page UI shell (tabs)
      static/
        app.js                  # vanilla JS: tab switching, progress polling, review, params
        style.css
  models/
    clip-vit-b32.onnx           # downloaded/converted; gitignored; documented in README
  scripts/
    convert_clip_to_onnx.py     # one-time Python conversion (only non-Go artifact)
    run-qdrant.sh               # docker run helper
  data/                          # gitignored — all runtime caches + state live here
    scan_cache.json
    tier_a.json
    tier_b.json
    clusters.json
    review_state.json
    config.json
    heic_cache/                 # converted HEIC→JPEG temp files
  testdata/                      # sample images for tests (committed)
  Makefile
  README.md
  .gitignore
```

`.gitignore` must include: `models/`, `data/`, `qdrant_storage/`, plus the usual
Go build artifacts.

---

## 5. Data Contracts (exact shapes — the agent must match these)

### 5.1 `ImageRecord` (core struct)

```go
type ImageRecord struct {
    ID          uint64    `json:"id"`          // FNV-1a hash of AbsPath (stable point ID for Qdrant)
    AbsPath     string    `json:"abs_path"`    // absolute path to the ORIGINAL image file
    WorkPath    string    `json:"work_path"`   // path used for decoding: == AbsPath, or the converted JPEG for HEIC
    Filename    string    `json:"filename"`    // base filename of the original
    CaptureTime time.Time `json:"capture_time"`// sidecar JSON > EXIF DateTimeOriginal > file mtime
    FileSize    int64     `json:"file_size"`   // bytes of the ORIGINAL file
    Width       int       `json:"width"`
    Height      int       `json:"height"`
    IsHEIC      bool      `json:"is_heic"`
}
```

### 5.2 Cache/output files (all JSON in `data/`)

- `scan_cache.json`: `[]ImageRecord`.
- `tier_a.json`: `[]Pair` where
  `Pair{ AID uint64; BID uint64; Distance int }` (Hamming distance).
- `tier_b.json`: `[]SimPair` where
  `SimPair{ AID uint64; BID uint64; Score float32 }` (cosine similarity).
- `clusters.json`: `[]Cluster` (see 5.3).
- `review_state.json`: `map[string]ClusterDecision` keyed by cluster ID (see 7.4).
- `config.json`: serialized `Config` (see 5.4).

### 5.3 `Cluster`

```go
type Cluster struct {
    ID       string   `json:"id"`        // e.g. "cluster_001", stable across a run
    Tiers    []string `json:"tiers"`     // which tiers contributed: ["A"], ["B"], or ["A","B"]
    MemberIDs []uint64 `json:"member_ids"`// image IDs; look up full records via an ID→ImageRecord map
}
```

Only groups of size ≥ 2 are clusters. Size-1 images are "uniques" and are handled
directly by the export step (always copied), not stored as clusters.

### 5.4 `Config`

```go
type Config struct {
    InputDir              string  `json:"input_dir"`
    OutputDir             string  `json:"output_dir"`
    QdrantAddr            string  `json:"qdrant_addr"`             // default "localhost:6334" (gRPC) — verify port, see Phase 4
    ModelPath             string  `json:"model_path"`             // default "models/clip-vit-b32.onnx"
    OnnxLibPath           string  `json:"onnx_lib_path"`          // path to libonnxruntime.{so,dylib,dll}
    HeifConvertPath       string  `json:"heif_convert_path"`      // "" → auto-detect via exec.LookPath
    PHashThreshold        int     `json:"phash_threshold"`        // default 10
    PHashEnabled          bool    `json:"phash_enabled"`          // default true
    ClipSimilarityThresh  float32 `json:"clip_similarity_thresh"` // default 0.92
    ClipEnabled           bool    `json:"clip_enabled"`           // default true
    ClipTopK              int     `json:"clip_top_k"`             // default 10 (kNN per image)
    KeeperOrder           []string`json:"keeper_order"`           // default ["resolution","filesize","earliest"]
    ServerAddr            string  `json:"server_addr"`            // default "localhost:8080"
}
```

Config precedence (highest wins): explicit CLI flag > `data/config.json` >
built-in defaults. The UI reads and writes `data/config.json` via the API.

---

## 6. Phase-by-Phase Plan

Build order is given in §8. Each phase lists concrete tasks and a deliverable.

### Phase 0 — Scaffolding

1. `go mod init github.com/shahnewaz-labib/photodupe`.
2. Create the directory structure in §4 with stub files.
3. `Makefile` targets: `build`, `test`, `run-qdrant`, `run` (build + start UI).
4. `.gitignore` as specified in §4.

**Deliverable:** `go build ./...` succeeds with stubs; tool does nothing yet.

---

### Phase 1 — Scanner

**Goal:** walk the input dir, produce `[]ImageRecord`, cache to
`data/scan_cache.json`.

**Tasks:**
1. Implement `ScanDir(cfg Config, progress *Progress) ([]ImageRecord, error)`:
   - `filepath.WalkDir` over `cfg.InputDir`.
   - Match extensions (case-insensitive): `.jpg .jpeg .png .gif .webp .bmp .heic
     .heif`. Skip everything else silently.
   - Compute `ID = FNV-1a(AbsPath)` as `uint64`.
   - **Metadata (`CaptureTime`), best-effort in this order:**
     1. Optional sidecar JSON in the same folder: check `<filename>.json` and
        `<filename>.supplemental-metadata.json`. If present and it contains
        `photoTakenTime.timestamp` (Unix seconds as string or number), use it.
        **Sidecar files are optional and not assumed to exist** — absence is
        normal, not an error.
     2. EXIF `DateTimeOriginal` via `github.com/rwcarlsen/goexif/exif`.
     3. File mtime.
   - `FileSize` from `os.Stat` on the original.
   - `Width`/`Height` via `image.DecodeConfig` for non-HEIC (cheap, no full
     decode). For HEIC, get dimensions after conversion.
2. **HEIC handling:**
   - Set `IsHEIC = true` for `.heic`/`.heif`.
   - Resolve `heif-convert`: use `cfg.HeifConvertPath` if set, else
     `exec.LookPath("heif-convert")`.
   - If found: convert each HEIC to a JPEG in `data/heic_cache/<ID>.jpg`, set
     `WorkPath` to that JPEG. Skip re-conversion if the cache file already exists
     (idempotent).
   - If not found: log a warning **once**, set the record aside, and **skip all
     HEIC files**, counting them. Report the skipped count at end of scan. Do
     **not** crash.
   - For non-HEIC, `WorkPath = AbsPath`.
3. Progress: report files-scanned count into `*Progress` as you go (see 6.pipeline).
4. Write `data/scan_cache.json`. On a re-run, if the cache exists and
   `InputDir` is unchanged, the pipeline may reuse it (make this a flag:
   `--rescan` forces a fresh scan).
5. `scanner_test.go`: use `testdata/` with a few real small images (jpg + png) and
   one fabricated sidecar JSON. Assert count, that dimensions/size are read, and
   that sidecar timestamp wins over mtime. (Do not require HEIC in tests unless a
   sample + converter is available; guard HEIC tests with a `heif-convert`
   presence check and `t.Skip` otherwise.)

**Deliverable:** `photodupe scan` prints found/skipped counts and writes
`data/scan_cache.json`.

---

### Phase 2 — Perceptual Hashing (Tier A)

**Goal:** compute a pHash per image, produce Tier A duplicate pairs.

**Tasks:**
1. Dependency: `github.com/corona10/goimagehash`.
2. `ComputeHash(workPath string) (*goimagehash.ExtImageHash, error)`:
   decode via stdlib `image.Decode` (register jpeg/png/gif; webp via
   `golang.org/x/image/webp`; bmp via `golang.org/x/image/bmp`), then
   `goimagehash.PerceptionHash(img)` (64-bit).
3. Store the 64-bit hash per image ID in memory.
4. **Pairwise Tier A search** (respect scale):
   - If `n < 5000`: naive O(n²) XOR+popcount is fine (completes in seconds).
   - If `n ≥ 5000`: bucket by the top 16 bits of the hash and only compare within
     + adjacent buckets, OR a simple LSH banding scheme. Comment the chosen
     approach. Measure before over-optimizing.
   - `// ponytail: naive O(n^2) below 5k; bucketed path above. Swap to a BK-tree
     if n grows past ~100k.`
5. Emit all pairs with Hamming distance ≤ `cfg.PHashThreshold` to `data/tier_a.json`.
6. `phash_test.go`: two images that are recompressed/resized copies must produce
   a pair; two clearly different images must not.

**Deliverable:** `photodupe hash` writes `data/tier_a.json`.

---

### Phase 3 — CLIP Embeddings via ONNX Runtime (Tier B, part 1)

Highest-risk phase. Do it in isolation with the parity test **before** wiring it
into the pipeline.

#### 3a. One-time model prep (Python, run once — NOT part of the Go runtime)

`scripts/convert_clip_to_onnx.py`:
1. **First, try to download a pre-converted ONNX CLIP ViT-B/32** from Hugging Face
   (search "clip-vit-base-patch32 onnx"). If a suitable image-embedding `.onnx`
   exists that outputs a 512-dim projected embedding, download it to
   `models/clip-vit-b32.onnx` and skip conversion. Preferred (less error-prone).
2. If converting yourself, export the **projected image features** (512-dim), not
   the raw pooled vision output (768-dim):
   ```python
   import torch
   from transformers import CLIPModel

   model = CLIPModel.from_pretrained("openai/clip-vit-base-patch32").eval()

   class ImageEncoder(torch.nn.Module):
       def __init__(self, m): super().__init__(); self.m = m
       def forward(self, pixel_values):
           return self.m.get_image_features(pixel_values=pixel_values)

   wrapper = ImageEncoder(model)
   dummy = torch.randn(1, 3, 224, 224)
   torch.onnx.export(
       wrapper, dummy, "models/clip-vit-b32.onnx",
       input_names=["pixel_values"], output_names=["image_embeds"],
       opset_version=17,
       dynamic_axes={"pixel_values": {0: "batch"}, "image_embeds": {0: "batch"}},
   )
   ```
3. **Verify** the exported model's output dimension is **512** (inspect with the
   `onnx` Python package or Netron) before proceeding. If it is not 512, stop and
   fix the export — do not continue with a mismatched dimension.
4. Record the exact input name (`pixel_values`) and output name (`image_embeds`)
   — the Go code must reference these exact strings.

#### 3b. Go-side preprocessing (`internal/embed/preprocess.go`)

1. Decode `WorkPath` (reuse Phase 2 decode).
2. Resize shorter side to 224 preserving aspect ratio using
   `golang.org/x/image/draw` (`draw.CatmullRom.Scale`), then center-crop to
   224×224.
3. Produce a flat `[]float32` of length `1*3*224*224` in **CHW** row-major order:
   `idx = c*224*224 + h*224 + w`, value
   `= (pixel[h][w][c]/255.0 - mean[c]) / std[c]` with the constants in §2.
   Channel order is **RGB** (c=0 R, 1 G, 2 B).
4. **Parity test (`preprocess_test.go`) — highest-priority test in the project:**
   commit a reference tensor produced by Python's `CLIPImageProcessor` (or the
   manual torchvision transform) for one committed `testdata` image, saved as a
   `.npy` or flat float32 `.bin`. Assert the Go output matches element-wise within
   `1e-4`. Mismatched preprocessing produces embeddings that look fine (no error)
   but give meaningless similarity — this test is the guard.

#### 3c. Go-side ONNX inference (`internal/embed/clip.go`)

1. Dependency: `github.com/yalue/onnxruntime_go`.
2. **onnxruntime shared library** must be present:
   - Download the release matching OS/arch from
     `https://github.com/microsoft/onnxruntime/releases`
     (`libonnxruntime.so` Linux, `.dylib` macOS, `onnxruntime.dll` Windows).
   - Set its path via `ort.SetSharedLibraryPath(cfg.OnnxLibPath)` before
     `ort.InitializeEnvironment()`.
   - **Document the exact onnxruntime version used** in the README —
     `onnxruntime_go` targets a specific version range; check its README for the
     compatible version at implementation time and pin it.
3. API:
   ```go
   type CLIPEncoder struct { /* session + tensor handles */ }
   func NewCLIPEncoder(cfg Config) (*CLIPEncoder, error)
   func (e *CLIPEncoder) Embed(pixelData []float32) ([]float32, error) // 512-dim
   func (e *CLIPEncoder) EmbedBatch(batch [][]float32) ([][]float32, error) // optional
   func (e *CLIPEncoder) Close() error
   ```
4. Input tensor name `pixel_values`, output tensor name `image_embeds` — must
   match the exported model exactly (3a step 4).
5. **L2-normalize** each output vector in Go (`v / ||v||`) unless verified the
   model already emits unit vectors. Cosine similarity assumes unit vectors.
6. Start with single-image inference; add batching only if measured too slow.
7. `clip_test.go`: embed the same image twice → identical (or ~identical) vectors;
   embed two visually-similar test images → cosine > embed of two dissimilar ones.

**Deliverable (combined with Phase 4):** `photodupe embed` computes an
embedding per image, L2-normalizes it, and upserts into Qdrant.

---

### Phase 4 — Vector DB / Qdrant (Tier B, part 2)

1. Local Qdrant: `scripts/run-qdrant.sh` →
   `docker run -p 6333:6333 -p 6334:6334 -v "$(pwd)/qdrant_storage:/qdrant/storage" qdrant/qdrant`.
   (6333 = REST, 6334 = gRPC. The Go client uses gRPC — **verify the client's
   expected port/address at build time** and set `Config.QdrantAddr` accordingly,
   default `localhost:6334`.)
2. Dependency: `github.com/qdrant/go-client`.
3. `internal/vectordb/qdrant.go`:
   ```go
   func Connect(addr string) (*qdrant.Client, error)
   func InitCollection(c *qdrant.Client, name string, dim int) error // idempotent: create if absent
   func UpsertEmbedding(c *qdrant.Client, coll string, id uint64, vec []float32, payload map[string]any) error
   func SearchSimilar(c *qdrant.Client, coll string, vec []float32, topK int, scoreThreshold float32) ([]SearchResult, error)
   type SearchResult struct { ID uint64; Score float32 }
   ```
4. Collection: size **512**, distance **Cosine**.
5. Payload per point: `{ "id": <uint64>, "path": <abs_path> }` (so results map
   back to records).
6. Point ID = `ImageRecord.ID` (FNV-1a of abs path) → upserts are idempotent;
   re-running overwrites rather than duplicating.
7. After all embeddings are upserted, for each image run `SearchSimilar` with
   `topK = cfg.ClipTopK`, `scoreThreshold = cfg.ClipSimilarityThresh`, drop
   self-matches, and emit each `(AID, BID, Score)` pair to `data/tier_b.json`.
   Deduplicate symmetric pairs (store with `AID < BID`).
8. `qdrant_test.go`: guard with a Qdrant-reachable check (`t.Skip` if not up).
   First validate with dummy random vectors (insert known-near vectors, assert
   they're returned as neighbors) before relying on real embeddings.

**Deliverable:** `photodupe embed` (which now does embed → upsert → search)
writes `data/tier_b.json`.

---

### Phase 5 — Clustering (cheap, re-runnable)

1. `internal/cluster/unionfind.go`: union-find with path compression + union by
   rank, keyed by image `ID` (map `uint64`→dense index).
2. `internal/cluster/cluster.go`:
   - Load `tier_a.json` (if `cfg.PHashEnabled`) and `tier_b.json`
     (if `cfg.ClipEnabled`).
   - **Apply thresholds at cluster time too**, so tightening a threshold in the
     UI re-filters cached pairs without re-hashing/re-embedding: only union a Tier
     A pair if `Distance ≤ cfg.PHashThreshold`, only union a Tier B pair if
     `Score ≥ cfg.ClipSimilarityThresh`. (The cached files may contain looser
     pairs from a previous, looser run — filtering here makes tuning instant.
     Note: *loosening* a threshold below what the cache was generated with
     requires a re-hash/re-embed; the UI must warn about this — see §7.5.)
   - Union every surviving pair from both tiers.
   - Extract connected components; keep only size ≥ 2 as `Cluster`s. Tag each
     with the tier(s) that contributed at least one internal edge.
   - Assign stable IDs `cluster_001`, `cluster_002`, … ordered by smallest member
     ID (deterministic across runs given the same input).
   - Write `data/clusters.json`.
3. `cluster_test.go`: given synthetic Tier A/B pair sets, assert the expected
   groups and tier tags form; assert threshold filtering drops the right edges.

**Deliverable:** `photodupe cluster` writes `data/clusters.json`. Fast enough
to re-run on every parameter tweak.

---

### Phase 6 — Keeper Selection + Export

**6a. Keeper (`internal/keeper/rank.go`)**
```go
func SuggestKeeper(members []ImageRecord, order []string) ImageRecord
```
Apply `cfg.KeeperOrder` criteria in sequence (default
`["resolution","filesize","earliest"]`), ties broken by lexicographic `AbsPath`.
`rank_test.go` covers each ordering + tie-break determinism.

**6b. Export (`internal/export/export.go`)**
```go
func Export(cfg Config, records []ImageRecord, clusters []Cluster,
            decisions map[string]ClusterDecision, progress *Progress) (ExportReport, error)
```
Behavior:
1. Compute the set of **keeper IDs**:
   - For each cluster: the keeper chosen in `decisions` (user override), or the
     heuristic default if the cluster was never touched.
   - Every image **not** in any cluster (a unique) is a keeper.
2. Copy each keeper's **original file** (`AbsPath`, not the HEIC-converted
   `WorkPath`) into `cfg.OutputDir`.
   - Preserve the original filename. On filename collision in the flat output dir,
     append ` (2)`, ` (3)`, … before the extension. (Do **not** silently
     overwrite.) `// ponytail: flat output + numeric suffix on collision; mirror
     the source tree instead only if the user asks.`
   - Copy bytes verbatim (`io.Copy`), preserve mtime via `os.Chtimes`. Never touch
     the source.
3. `ExportReport{ Copied int; Skipped int; Collisions int; Errors []string }`.
4. **Safety guardrails (do not simplify these away):**
   - Refuse to run if `OutputDir` is empty, equals `InputDir`, or is a
     parent/ancestor of `InputDir` (would risk writing into the source tree).
     Return a clear error.
   - Create `OutputDir` if absent.
5. `export_test.go`: temp dirs, a couple of clusters + uniques, assert exactly the
   keepers + all uniques land in output, collisions get suffixed, sources
   untouched, and the same-dir/ancestor guardrails trigger.

**Deliverable:** invoked from the UI "Export" action (and available as
`photodupe export`).

---

### Phase 7 — Web UI (`internal/review`)

Plain `net/http`, no web framework. One HTML page (`index.html`) with three tabs
driven by a small vanilla-JS file (`app.js`). No JS build step, no npm. Serve
`static/` and `templates/` via `embed.FS` so the binary is self-contained and
cross-platform (`//go:embed`).

**7.1 Tabs (all in one page, JS switches which panel is visible):**

- **Tab 1 — Run.**
  - Input directory: a text field to type/paste an absolute path, **plus** a
    server-side directory browser (see 7.3) so the user can navigate the server's
    filesystem to pick a folder. (Browsers cannot hand the server a real
    folder path from a native picker — the server must list directories itself.)
  - Output directory: same widget.
  - A collapsible "Parameters" section (see 7.5) — editable before starting.
  - "Start dedup" button → `POST /api/run`. While running, show a **live progress
    view**: current phase (scan/hash/embed/cluster), counts (e.g. "hashed
    1234/5000"), a progress bar, and a running log tail. Poll `GET /api/status`
    every ~1s. Disable "Start" while a run is in progress.

- **Tab 2 — Review.**
  - Lists all clusters (fetched from `GET /api/clusters`): a card per cluster with
    a tier badge (A / B / A+B), member count, and a thumbnail strip.
  - Clicking a cluster opens the detail view: **the keeper photo shown prominently
    alongside every photo determined to be its duplicate**, each at a readable
    size, each labeled with resolution / file size / capture time. The suggested
    keeper is highlighted. The user can:
    - Change which member is the keeper (radio/click).
    - Optionally mark the whole cluster "not actually duplicates" (skip → all
      members treated as uniques, all copied).
    - Save the decision → `POST /api/clusters/{id}/decision`.
  - Navigation: next/prev cluster, and a "resolved / pending" indicator so the
    user can work through the set without losing their place.

- **Tab 3 — (optional) Settings/Export.**
  - Full parameter form (7.5) with Save.
  - "Export kept photos to output dir" button → `POST /api/export`, showing the
    resulting `ExportReport` (copied/skipped/collisions/errors).
  - (If you prefer, fold Export into Tab 1 or Tab 2 — but the parameter form and
    an explicit Export trigger must exist somewhere obvious.)

**7.2 Image serving:** `GET /api/image?id=<uint64>&thumb=1`.
- Resolve the ID → `ImageRecord`, serve the file from `WorkPath` (so HEIC shows
  as its converted JPEG).
- `thumb=1` → serve a cached, downscaled thumbnail (generate on first request into
  `data/thumbs/<id>.jpg`, e.g. max 256px, then serve from cache).
- **Path-traversal guard:** never accept a raw path from the client. Only serve
  files whose `ImageRecord` exists in the loaded scan set and whose resolved path
  is within `InputDir` (or `data/heic_cache`). Reject anything else with 403.

**7.3 Directory browser API:** `GET /api/browse?path=<abs>` →
`{ "path": "...", "parent": "...", "dirs": ["...", ...] }` listing only
subdirectories of `path` (default to the user's home or filesystem root if `path`
is empty). This backs the folder-picker widget. Read-only; never writes.

**7.4 Review state persistence:**
```go
type ClusterDecision struct {
    KeeperID uint64 `json:"keeper_id"` // 0 if cluster marked "not duplicates"
    Skipped  bool   `json:"skipped"`   // true → treat all members as uniques
    Resolved bool   `json:"resolved"`  // user has reviewed this cluster
}
```
Stored in `data/review_state.json` (map cluster ID → decision), written on every
decision so progress survives server restarts.

**7.5 Parameters form (the "tweak params" requirement):**
Editable fields, mapped to `Config`: `PHashThreshold`, `PHashEnabled`,
`ClipSimilarityThresh`, `ClipEnabled`, `ClipTopK`, `KeeperOrder`.
- `POST /api/config` saves them to `data/config.json`.
- Changing **only** thresholds/enables/keeper-order → offer a "Re-cluster" button
  that re-runs **only** Phase 5 (fast) and refreshes the Review tab. Do not
  re-hash/re-embed.
- **Warn** the user when a change requires expensive recomputation: specifically,
  *lowering* `PHashThreshold` below, or `ClipSimilarityThresh` below, the value
  the caches were generated with (cached pair files only contain pairs at least as
  tight as the generating run). In that case the UI must state that a full re-run
  (hash/embed) is needed and offer the "Start dedup" path instead.
  `// ponytail: cache stores pairs at the loosest generated threshold; tightening
  is free (filter), loosening needs a re-run.`

**7.6 API summary:**
| Method + path | Purpose |
|---|---|
| `GET /` | serve the single-page UI |
| `GET /api/status` | current pipeline progress/phase/counts (poll) |
| `POST /api/run` | start scan→hash→embed→cluster with current config |
| `POST /api/recluster` | re-run only clustering (fast param tuning) |
| `GET /api/clusters` | list clusters + members + decisions |
| `POST /api/clusters/{id}/decision` | save a ClusterDecision |
| `GET /api/image?id=&thumb=` | serve original/thumbnail |
| `GET /api/browse?path=` | list subdirectories (folder picker) |
| `GET /api/config` / `POST /api/config` | read/write parameters |
| `POST /api/export` | copy keepers + uniques → output dir; return report |

**Deliverable:** `photodupe review` (or `photodupe ui`) starts the server on
`cfg.ServerAddr`; the three tabs work end-to-end.

---

### Phase 8 — Pipeline Orchestration, CLI Wiring, Cross-Platform

**8a. Pipeline + progress (`internal/pipeline`)**
- `Progress` is a thread-safe struct (mutex or atomics) holding: current phase,
  per-phase total + done counts, a bounded recent-log ring buffer, and a
  `Running bool`. The pipeline updates it; `GET /api/status` reads it.
- `RunAll(cfg, progress)` runs scan → hash → embed → cluster in sequence,
  reusing caches where valid (respect `--rescan` / a "force" flag from the UI).

**8b. CLI (`main.go`, stdlib `flag` + manual subcommand dispatch — no cobra)**
Subcommands: `scan`, `hash`, `embed`, `cluster`, `export`, `review` (alias `ui`),
`all` (= scan→hash→embed→cluster, then start the review server). Each accepts
flags that override `Config`. `all` and `review` are the primary user entry
points; the per-phase subcommands exist for debugging/tests.

**8c. Logging:** stdlib `log/slog`. Progress indicators for the long phases
(scan/hash/embed) both to stderr and into `Progress` for the UI.

**8d. Cross-platform notes (document in README):**
- Pure-Go parts (scanner, hash, cluster, keeper, export, review) are portable —
  use `filepath`, `embed.FS`, no hardcoded separators, no Linux-only syscalls.
- **CGO / onnxruntime**: the one native dependency. Ship per-OS setup docs and let
  `Config.OnnxLibPath` point at the right shared lib. Linux is the guaranteed
  target; macOS/Windows documented best-effort.
- **HEIC**: `heif-convert` install differs per OS (`apt install libheif-examples`,
  `brew install libheif`, Windows via prebuilt binaries) — document each; degrade
  gracefully (skip + warn) when absent.
- **Qdrant**: Docker is cross-platform; `run-qdrant.sh` (plus note the equivalent
  `docker run` for Windows PowerShell).

**Deliverable:** `photodupe all --input <dir> --output <dir>` (or fully
UI-driven: `photodupe ui`) runs the whole flow and serves the UI.

---

## 7. Testing Strategy

- Unit tests per package as noted. **The preprocessing-parity test (3b) is the
  single highest-value test** — mismatched normalization is a silent
  correctness killer.
- Integration test: a `testdata/` folder with ~10 committed images including 2–3
  known true duplicates (resized/recompressed copies) and 2–3 known
  similar-but-distinct images. Run scan → hash → cluster (pHash path, no external
  deps) and assert the expected Tier A groups form. Gate the CLIP+Qdrant portion
  behind availability checks (`t.Skip` when Qdrant/onnxruntime lib absent) so the
  core suite runs anywhere.
- Manual validation checkpoint after Phase 5: eyeball real clusters against real
  images and tune thresholds in the UI before trusting export.
- Export tests must assert **sources are never modified** and the same-dir /
  ancestor guardrails fire.

---

## 8. Suggested Build Order

1. Phase 0 — scaffolding.
2. Phase 1 — scanner (pure Go, testable alone).
3. Phase 2 — pHash + Tier A (pure Go).
4. Phase 5 — clustering (pure logic; testable with synthetic pairs, before CLIP
   exists).
5. Phase 6 — keeper + export (pure Go; gives a working pHash-only end-to-end:
   scan→hash→cluster→export).
6. Phase 7 (partial) — UI Run + Review tabs wired to the pHash-only pipeline, so
   there's a usable tool before touching ONNX.
7. Phase 4 — Qdrant client, validated with dummy vectors.
8. Phase 3 — CLIP/ONNX, isolated, with the parity test, then wired into embed →
   Qdrant → Tier B.
9. Phase 7 (finish) — params, re-cluster, export triggers, thumbnails.
10. Phase 8 — pipeline/progress polish, CLI wiring, cross-platform docs.

This front-loads pure-Go, dependency-free work, yields a working pHash-only tool
early, and isolates the one risky integration (ONNX correctness) behind its
parity test.

---

## 9. Open Questions to Resolve During Implementation

- Module path is `github.com/shahnewaz-labib/photodupe` (decided).
- **onnxruntime version** compatible with the current `onnxruntime_go` release —
  pin it, document it (Phase 3c).
- **Qdrant Go client address/port** (REST 6333 vs gRPC 6334) — verify against the
  client version in use (Phase 4).
- **Pre-converted CLIP ONNX availability** on Hugging Face — use it if a 512-dim
  image-embedding export exists; otherwise run the conversion script (Phase 3a).
- **Final thresholds** — tune empirically via the UI after the Phase 5 manual
  checkpoint; the values here (pHash ≤ 10, CLIP ≥ 0.92) are starting points.

---

## 10. Hard Constraints Recap (do not violate)

1. **No cloud / no Google API.** Local files only: read input dir, write copies to
   output dir.
2. **Never modify, move, or delete source files.** Output is copies only.
3. **Never delete anything anywhere.** "Rejecting" a duplicate = not copying it.
4. **Export guardrails** (empty/same-as-input/ancestor output dir) must be
   enforced.
5. **Cross-platform:** portable Go; Linux is the guaranteed target, macOS/Windows
   best-effort and documented.
6. **Preprocessing parity test must exist and pass** before CLIP results are
   trusted.
