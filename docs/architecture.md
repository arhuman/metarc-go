# Metarc — Architecture Reference

**Version**: 0.1.0  
**Last updated**: 2026-04-17

---

## Table of Contents

1. [Overview](#1-overview)
2. [Package Map](#2-package-map)
3. [Binary Format (.marc)](#3-binary-format-marc)
4. [SQLite Schema](#4-sqlite-schema)
5. [Data Flow: Archive](#5-data-flow-archive)
6. [Data Flow: Extract](#6-data-flow-extract)
7. [Transform System](#7-transform-system)
8. [Storage Features](#8-storage-features)
9. [Store Internals](#9-store-internals)
10. [Worker Pipeline](#10-worker-pipeline)
11. [Planner Heuristic](#11-planner-heuristic)
12. [Key Types](#12-key-types)
13. [Design Decisions](#13-design-decisions)

---

## 1. Overview

Metarc is a **semantic meta-compression archiver**. It operates above a final byte-stream compressor (zstd) by exploiting structural and cross-file redundancy in text corpora — source code, configs, logs, structured datasets.

```
Source files
    │
    ▼
[scan] ─────── filesystem walk, basic metadata
    │
    ▼
[analyze] ──── BLAKE3 hashing (parallel workers)
    │
    ▼
[plan] ──────── cost/gain heuristic → select Transform
    │
    ▼
[store] ─────── apply transform, write blobs, build catalog
    │
    ▼
output.marc
```

**Position in the ecosystem**:

| Tool | Focus |
|------|-------|
| Borg / Restic | Block-level dedup, incremental backup |
| **Metarc** | **Semantic dedup + cross-file redundancy, single-file archive** |
| gzip / zstd | Byte-stream compression |

---

## 2. Package Map

```
metarc/
├── cmd/metarc/          CLI entry point (Cobra commands)
│   ├── main.go          Error formatting, exit codes
│   ├── root.go          Command registration
│   ├── archive.go       `marc archive` + printPlanSummary
│   ├── extract.go       `marc extract`
│   ├── inspect.go       `marc inspect`
│   └── bench.go         `marc bench`
│
├── pkg/marc/            Stable external contract (format + interfaces)
│   ├── format.go        Magic, chunks, footer, DetectFormat, SchemaDDL
│   └── transform.go     Transform interface, Entry, Facts, Result, BlobSink
│
├── internal/
│   ├── scan/            Filesystem walk → chan marc.Entry
│   ├── analyze/         BLAKE3 FullHash, QuickSig
│   ├── plan/            Decide() heuristic, Registry
│   ├── catalog/         Planned module (currently stub)
│   ├── store/           Writer, Reader, blobSink, solidAccumulator, dict
│   │   └── transforms/
│   │       ├── dedup.go              dedup/v1 (lossless, default)
│   │       ├── json/canonical.go     json-canonical/v1 (opt-in)
│   │       ├── license/canonical.go  license-canonical/v1 (opt-in)
│   │       ├── logtempl/template.go  log-template/v1 (opt-in)
│   │       └── delta/neardelta.go    near-dup-delta/v1 (stub)
│   └── runtime/         Archive/Extract orchestration, worker pool
│
└── playground/          Local test fixtures (not committed)
```

### Responsibilities

| Package | Responsibility |
|---------|---------------|
| `cmd/metarc` | CLI parsing, flag binding, output formatting |
| `pkg/marc` | Format spec, Transform interface, stable public types |
| `internal/scan` | Walk filesystem, collect `marc.Entry` without analyzing content |
| `internal/analyze` | BLAKE3-256 hashing, QuickSig computation |
| `internal/plan` | Select transform per entry using cost/gain heuristic |
| `internal/store` | Write/read blob chunks, manage SQLite catalog, compress |
| `internal/runtime` | Orchestrate scan → hash → plan → store; CLI bridge |

---

## 3. Binary Format (.marc)

### Layout

```
Offset            Field                    Size    Notes
────────────────────────────────────────────────────────────────
0                 Magic                    8B      "METARC\x01\x00"
8                 Blob region              var     Sequence of chunks
                  ┌─ Blob chunk (0x01)     5+N     [Type][Len uint32 BE][payload]
                  └─ Solid block (0x03)    5+N     [Type][Len uint32 BE][zstd frame]
CatalogOffset     Catalog chunk (0x02)     5+N     [Type][Len uint32 BE][zstd SQLite]
EOF-24            Footer                   24B     Not included in checksum
────────────────────────────────────────────────────────────────
Footer layout:
  [CatalogOffset 8B BE] [BlobRegionOffset 8B BE] [Checksum 8B]
  BlobRegionOffset is always 8 (= len(Magic)).
  Checksum = first 8 bytes of BLAKE3 over all bytes before the footer.
```

### Chunk Types

| Byte | Name | Payload |
|------|------|---------|
| `0x01` | Blob | Raw, zstd, or zstd+dict compressed blob data |
| `0x02` | Catalog | zstd-compressed SQLite database |
| `0x03` | Solid block | Single zstd frame containing multiple concatenated blobs |

### Compression Modes (blobs.compressed)

| Value | Name | Description |
|-------|------|-------------|
| 0 | None | Raw, uncompressed |
| 1 | Zstd | Standard zstd |
| 2 | ZstdDict | zstd with shared dictionary from `meta.dict` |
| 3 | Solid | Blob is a slice within a solid block chunk |

### Reading Algorithm

```
1. Seek to EOF-24, read footer (CatalogOffset, BlobRegionOffset, Checksum)
2. Seek to CatalogOffset, read chunk header [0x02][Len]
3. Read compressed catalog payload (limit: 512 MB decompressed)
4. Decompress to temp SQLite file, open read-only
5. Load dictionary from meta table (if present)
6. WalkEntries: reconstruct paths via parent_id chain
7. Per file: OpenBlob → seek to offset+5, decompress if needed
```

### Writing Algorithm

```
1. Write magic (8B)
2. Per blob: compress → write chunk [0x01][Len][payload], record offset in blobs
3. At Close: flush solid accumulator
4. VACUUM INTO temp file → compress → write catalog chunk [0x02][Len][payload]
5. Write footer with catalog offset and BLAKE3 checksum
```

### Legacy Format (split-file)

Older archives consist of two files:
- `archive.marc` — SQLite database (detected by "SQLite format 3\000" header)
- `archive.marc.blobs` — raw blob sidecar

`DetectFormat()` auto-detects and `OpenReader` handles both paths transparently.

---

## 4. SQLite Schema

```sql
-- Archive metadata (version, dictionary)
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);

-- String interning: every filename stored once, referenced by name_id
CREATE TABLE names (id INTEGER PRIMARY KEY, name TEXT UNIQUE);

-- Filesystem tree
CREATE TABLE entries (
    id             INTEGER PRIMARY KEY,
    parent_id      INTEGER REFERENCES entries(id),  -- NULL for root
    name_id        INTEGER NOT NULL REFERENCES names(id),
    mode           INTEGER NOT NULL,                -- fs.FileMode bits
    mtime_ns       INTEGER,                         -- nanoseconds since epoch
    size           INTEGER,                         -- bytes (0 for dirs/symlinks)
    uid            INTEGER NOT NULL DEFAULT 0,
    gid            INTEGER NOT NULL DEFAULT 0,
    transform      TEXT,                            -- transform ID or NULL
    params         BLOB,                            -- ≤1KB inline per-entry params
    symlink_target TEXT                             -- non-empty for symlinks
);
CREATE INDEX idx_entries_parent ON entries(parent_id);

-- Content-addressable blob store
CREATE TABLE blobs (
    id           INTEGER PRIMARY KEY,
    sha          BLOB UNIQUE NOT NULL,    -- BLAKE3-256 (32 bytes)
    offset       INTEGER NOT NULL,        -- chunk header start in .marc file
    clen         INTEGER NOT NULL,        -- compressed length
    ulen         INTEGER NOT NULL,        -- uncompressed length
    compressed   INTEGER NOT NULL DEFAULT 1,
    block_id     INTEGER,                 -- solid block ID (NULL if standalone)
    block_offset INTEGER                  -- byte offset within solid block
);

-- Many-to-many: one entry may reference multiple blobs (ordered by seq)
CREATE TABLE entry_blobs (
    entry_id INTEGER REFERENCES entries(id),
    seq      INTEGER,
    blob_id  INTEGER REFERENCES blobs(id),
    PRIMARY KEY (entry_id, seq)
);

-- Optional transform decision log (deleted unless --keep-plan-log)
CREATE TABLE plan_log (
    entry_id       INTEGER REFERENCES entries(id),
    transform_id   TEXT,           -- NULL for raw writes
    estimated_gain INTEGER,
    estimated_cpu  INTEGER,
    applied        INTEGER,        -- 0 | 1
    reason         TEXT
);
```

**Key invariants:**
- `blobs.sha` is UNIQUE → content-addressable dedup at hash level
- `entries.parent_id` forms an implicit rooted tree (NULL = root ".")
- `blobs.offset` points to the chunk **header**, not the payload (reader skips 5 bytes)
- For solid blocks, `offset` is the solid block chunk header; `block_offset` is the slice start within the decompressed block

---

## 5. Data Flow: Archive

```
marc archive output.marc source-dir
         │
         ▼
runtime.ArchiveWithOpts(ctx, marcPath, srcDir, compressor, keepLog, opts)
         │
         ├─ [optional prescan] store.TrainDictionary(srcDir)
         │     Walk up to 500 files ≤64KB each; requires ≥8 samples
         │     → zstd.BuildDict → pass bytes to WithDictCompress
         │
         ├─ store.OpenWriter(marcPath, WithCompressor, WithSolidBlockSize, ...)
         │     Create temp SQLite, apply schema, write magic, begin tx
         │
         ├─ scan.Walk(srcDir) → chan marc.Entry  (buffered 1024)
         │
         ├─ Worker Pool (N goroutines, default = NumCPU)
         │     Each worker:
         │       ← Entry from seqCh (with sequence number)
         │       → analyze.FullHash(file) → sha [32]byte
         │       → AnalyzedEntry{entry, sha, seq} to resultCh (buffered 256)
         │
         ├─ Sequencer goroutine
         │     Collects out-of-order AnalyzedEntry from resultCh
         │     Forwards in scan order to orderedCh (buffered 256)
         │
         └─ Writer goroutine (single)
               For each AnalyzedEntry:
                 w.WriteEntryWithSHA(ctx, entry, sha)
                   ├─ Directory  → INSERT entries (mode, mtime, uid, gid)
                   ├─ Symlink    → INSERT entries (mode, symlink_target)
                   └─ File       → writeFileWithSHA()
                         │
                         ├─ plan.Decide(entry, facts)
                         │     Iterate Registry; first applicable with gain>cpu wins
                         │
                         ├─ t.Apply(ctx, entry, file, sink)   [if transform selected]
                         │     └─ sink.Write(ctx, reader)
                         │           ├─ BLAKE3 hash (or use pre-computed)
                         │           ├─ Dedup: SELECT id FROM blobs WHERE sha=?
                         │           ├─ If hit: return existing BlobID  ← dedup!
                         │           └─ If miss:
                         │                 ├─ Route to solid accumulator OR direct
                         │                 ├─ Compress (none / zstd / zstd+dict)
                         │                 ├─ Write chunk [Type][Len][payload]
                         │                 └─ INSERT blobs → return new BlobID
                         │
                         ├─ INSERT entries (transform, params)
                         ├─ INSERT entry_blobs (entry_id, seq, blob_id)
                         └─ INSERT plan_log (applied, reason, gain, cpu)

               Every 1000 entries: COMMIT + BEGIN new tx
               At end: w.Close()
                   ├─ Flush solid accumulator (compress pending buffer, UPDATE blobs)
                   ├─ COMMIT final tx
                   ├─ VACUUM INTO serial temp file
                   ├─ zstd-compress catalog → write chunk [0x02][Len][payload]
                   └─ Write footer [CatalogOffset][8][Checksum]
```

---

## 6. Data Flow: Extract

```
marc extract archive.marc -C dest/
         │
         ▼
runtime.Extract(ctx, marcPath, destDir)
         │
         ├─ store.OpenReader(marcPath)
         │     DetectFormat → single-file or split-file path
         │     Single-file: read footer → seek catalog → decompress (≤512MB) → temp SQLite
         │     Load dict from meta table if present
         │
         ├─ Pass 1: r.WalkEntries(fn)  [parents before children, ORDER BY id]
         │     Reconstruct relPath via parent_id chain
         │     Validate: filepath.Clean(destDir+relPath) must stay inside destDir
         │     ├─ Directory  → os.MkdirAll + restoreMetadata
         │     ├─ Symlink    → validate target, os.Symlink + restoreMetadata
         │     └─ File       → extractFile()
         │           ├─ resolveTransform(entry.Transform)
         │           │     Lookup in plan.Registry by ID
         │           │     Error if ID non-empty and not found (prevents silent corruption)
         │           ├─ If transform: t.Reverse(Result{BlobIDs, Params}, blobReader, file)
         │           └─ If raw: r.OpenBlob(blobID) → io.Copy → file
         │
         │     r.OpenBlob(blobID):
         │       Query blobs: offset, clen, ulen, compressed, block_id, block_offset
         │       ├─ Solid (block_id set): openSolidBlob
         │       │     solidCache[chunkOffset] hit → slice [blockOffset:blockOffset+ulen]
         │       │     miss → read+decompress full block, cache (LRU max 4), slice
         │       └─ Normal: SectionReader(arcFile, offset+5, clen) + zstd if needed
         │
         └─ Pass 2: r.WalkEntries(fn)  [dirs only]
               Restore directory mtimes (children may have modified them in pass 1)
```

---

## 7. Transform System

### Interface

```go
type Transform interface {
    ID() TransformID                                              // Stable versioned ID
    Applicable(ctx, Entry, Facts) bool                           // Cheap, no I/O
    CostEstimate(Entry, Facts) (gainBytes, cpuUnits int64)       // Heuristic
    Apply(ctx, Entry, src io.Reader, sink BlobSink) (Result, error)
    Reverse(ctx, Result, blobs BlobReader, dst io.Writer) error
}
```

### Registered Transforms

| ID | Mode | Applicable | Gain model | CPU model |
|----|------|-----------|------------|-----------|
| `dedup/v1` | Lossless, **default** | Any file > 0B | `size` | `size/1024` |
| `json-canonical/v1` | Lossless*, opt-in | `.json` ≤10MB | `size/4` | `size/512` |
| `license-canonical/v1` | Lossy, opt-in | `LICENSE*` filenames | `size` | `512` |
| `log-template/v1` | Lossy, opt-in | `*.log`, syslog, 1KB–50MB | `size/3` | `size/256` |
| `near-dup-delta/v1` | Stub | Never | — | — |

\* json-canonical restores canonical form, not original formatting.

### Application / Reversal

**Write side** (`Apply`):
1. Applicable() — pure predicate (filename, size, extension)
2. CostEstimate() → if `gain > cpu`: apply; else fall back to raw
3. Apply() → call `sink.Write(reader)` → returns `Result{BlobIDs, Params}`
4. `ErrNotApplicable` from Apply → retry as raw write (seek file to start)

**Read side** (`Reverse`):
1. Look up transform ID in `plan.Registry`
2. If ID present but not in registry → error (prevents silent data corruption)
3. Reverse() → open blob(s) via BlobReader, reconstruct content, write to output

### Concrete Examples

**dedup/v1** — content-addressable dedup:
```
Apply:   stream file through sink → BLAKE3 → dedup check → compress → write chunk
Reverse: open blob by ID → copy bytes to output
```

**license-canonical/v1** — license fingerprinting:
```
Apply:   read file, normalize whitespace, BLAKE3 → lookup SPDX ID in fingerprints map
         If found: store SPDX ID as Params, no blob written
         If not:   ErrNotApplicable → fallback to raw
Reverse: read Params (SPDX ID) → write canonical license text from embedded texts.go
```

**log-template/v1** — template extraction:
```
Apply:   find common prefix across lines → Params={Tmpl, Count}, blob=suffixes only
Reverse: write template+"\n" then each suffix+"\n"
```

---

## 8. Storage Features

A **Storage Feature** modifies how blobs are physically laid out in the archive. Unlike a Transform, a Feature does not alter content semantics — the bytes a reader reconstructs are identical regardless of which features were active during archiving. Features configure the *storage pipeline*; Transforms configure the *content pipeline*.

```
Transform  → content layer   (what the bytes mean; choice recorded in entries.transform)
Feature    → storage layer   (how the bytes are arranged; fully transparent to readers)
```

The two axes are orthogonal and compose independently: any combination of active Features produces a valid archive that any reader can reconstruct without knowing which Features were used.

### Registered Features

| Feature | Flag | Status | Notes |
|---------|------|--------|-------|
| Solid blocks | `--no-solid`, `--solid-block-size` | Default on | |
| Dictionary compression | `--dict-compress` | Experimental | Unconvincing — see below |

### Solid blocks

Multiple small blobs are concatenated into a single zstd frame (default threshold: 4 MB). The compressor exploits cross-file repetition (shared import headers, license preambles, YAML keys) that it would miss when compressing each blob independently. Decompressing one blob within a block requires decompressing the full block; an LRU cache (max 4 blocks) amortises this cost for sequential extraction.

Solid compression is a pure storage decision: it is not recorded in `entries.transform` and requires no reader-side logic beyond locating the block offset.

### Dictionary compression *(unconvincing)*

`--dict-compress` trains a zstd dictionary on a sample of small files and uses it for all subsequent blob compression. The motivation is to give zstd shared context that reduces the cost of encoding common patterns.

In practice the gains are marginal (≈0.4% on real-world repositories) and the 32 KB dictionary overhead often negates them. Unlike solid blocks — which directly exploit inter-file redundancy by grouping blobs into shared frames — dictionary compression only seeds zstd's internal model, which solid mode already handles more effectively. **This feature may be removed.** Prefer solid blocks.

---

## 9. Store Internals

### Writer (store.go)

```go
type Writer struct {
    outFile    *os.File        // output .marc file
    db         *sql.DB         // in-memory catalog (temp SQLite file)
    tx         *sql.Tx         // current batch transaction
    blobOff    int64           // current write position
    entryN     int64           // entries in current tx
    nameCache  map[string]int64 // filename → name_id (intern pool)
    parentMap  map[string]int64 // relPath → entry_id (parent resolution)
    compressor string           // "zstd" | "none"
    dictData   []byte           // trained zstd dictionary (nil if unused)
    solidAcc   *solidAccumulator
    fileHasher *blake3.Hasher   // rolling hash over all written bytes
}
```

### blobSink (sink.go)

```
Write(reader):
  1. Stream through BLAKE3 hasher while buffering
  2. writeData(data, sha)

WriteWithSHA(reader, sha):
  1. Read all data (no rehashing)
  2. writeData(data, sha)

writeData(data, sha):
  1. SELECT id FROM blobs WHERE sha = ?  → dedup hit → return BlobID
  2. Route: solidAcc.addBlob() OR direct write
  3. Compress: none / zstd / zstd+dict
  4. Guard: len(payload) > MaxUint32 → error
  5. Write chunk: [0x01][len uint32 BE][payload]
  6. INSERT blobs → return new BlobID
```

### Solid Accumulator (solid.go)

```
addBlob(data, sha):
  1. If buf non-empty AND buf+data > maxBlockSize: flush()
  2. If data alone > maxBlockSize AND buf empty: add then flush immediately
  3. Append data to buf, INSERT blobs row (placeholder offset=-1)
  4. Append to pending list

flush():
  1. zstd.EncodeAll(buf) → compressed
  2. Guard: len(compressed) > MaxUint32 → error
  3. Write chunk [0x03][Len][compressed]
  4. UPDATE blobs SET offset=chunkOffset, clen=len(compressed) FOR all pending
  5. Increment blockCounter, reset buf and pending
```

### Dictionary Training (dict.go)

**Prescan mode** — upfront walk before archiving:
```
TrainDictionary(root):
  Walk source tree, collect ≤500 files, ≤64KB each, ≤8MB total
  zstd.BuildDict(samples) → dict bytes
  Pass via WithDictCompress(dict) to writer
```

**Simple mode** — online collection during archiving:
```
Per blob (if size ≤ 64KB):
  collectSample(data) → buffer copy
  If len(samples) ≥ 8 AND total ≥ 32KB: trainDictFromSamples()
  If len(samples) ≥ 500: trainDictFromSamples()
Stored in meta table at Close() for reader use
```

### Reader (reader.go)

```
OpenBlob(blobID):
  Query blobs row: offset, clen, ulen, compressed, block_id, block_offset
  ├─ CompressSolid: openSolidBlob(offset, clen, blockOffset, ulen)
  │     solidCache[offset] hit → bytes.NewReader(block[blockOffset:blockOffset+ulen])
  │     miss → ReadAt(arcFile, offset+5, clen) → zstd.DecodeAll → cache → slice
  ├─ CompressNone: SectionReader(arcFile, offset+5, clen)
  ├─ CompressZstd: zstd.NewReader(SectionReader)
  └─ CompressDict: zstd.NewReader(SectionReader, WithDecoderDicts(dictData))

WalkEntries(fn):
  SELECT entries + names + entry_blobs ORDER BY entries.id
  Rebuild relPath map: id → path (parent_id lookup)
  Call fn(relPath, EntryRow) in insertion order
```

---

## 10. Worker Pipeline

```
                         ┌──────────────────────────────────┐
scan.Walk ──(chan Entry)──► seq wrapper ──► seqCh (buffered) │
                         └─────┬────────────────────────────┘
                               │
                    ┌──────────┼──────────┐
                    ▼          ▼          ▼
                 Worker 1   Worker 2   Worker N     (N = NumCPU)
                 hashFile   hashFile   hashFile
                    │          │          │
                    └──────────┼──────────┘
                               │
                         resultCh (buffered 256, unordered)
                               │
                         ┌─────▼──────────────────┐
                         │  Sequencer goroutine    │
                         │  pending map[seq]Entry  │
                         │  forward in seq order   │
                         └─────┬──────────────────-┘
                               │
                         orderedCh (buffered 256)
                               │
                         ┌─────▼──────────────────┐
                         │  Writer goroutine       │
                         │  WriteEntryWithSHA()    │
                         │  Batched tx (1000/tx)   │
                         └────────────────────────-┘
```

**Why this topology:**
- **Parallel hashing**: BLAKE3 is CPU-bound; workers saturate all cores
- **Sequencer**: Preserves scan order so parent entries always precede children in `entries.id` — required for `parent_id` FK validity and for path reconstruction during read
- **Single writer**: SQLite does not support concurrent writers; serializing all inserts avoids lock contention

---

## 11. Planner Heuristic

```go
// internal/plan/plan.go
func Decide(ctx, entry, facts) (Transform, Decision) {
    for _, t := range Registry {
        if t.Applicable(ctx, entry, facts) {
            gain, cpu := t.CostEstimate(entry, facts)
            if gain > cpu {
                return t, Decision{Applied: true, ...}
            }
            return nil, Decision{Applied: false, Reason: "gain <= cpu"}
        }
    }
    return nil, Decision{Reason: "no applicable transform"}
}
```

**Properties:**
- First-applicable-wins: Registry ordering determines priority
- Conservative: `gain > cpu` (strictly greater), not `>=`
- Every decision logged in `plan_log` for post-hoc analysis via `inspect --plan-log`
- Heuristics are size-proportional, no I/O in planning phase

---

## 12. Key Types

```go
// pkg/marc/format.go
type Entry struct {
    Path       string      // absolute disk path
    RelPath    string      // relative to archive root
    Info       fs.FileInfo // from os.Lstat
    LinkTarget string      // non-empty for symlinks
}

// pkg/marc/transform.go
type Facts struct { Size int64 }

type Result struct {
    BlobIDs []BlobID  // ordered blob references
    Params  []byte    // ≤1KB inline per-entry metadata (stored in entries.params)
}

type BlobID   int64
type TransformID string

// internal/store/store.go
type EntryRow struct {
    ID, ParentID   int64
    Name           string
    Mode           fs.FileMode
    MtimeNs, Size  int64
    UID, GID       uint32
    BlobID         int64   // first blob (0 for dirs)
    Transform      string
    Params         []byte
    LinkTarget     string
}

// internal/plan/plan.go
type Decision struct {
    TransformID              string
    EstimatedGain, EstimatedCPU int64
    Applied                  bool
    Reason                   string
}
```

---

## 13. Design Decisions

### Single-file format
The catalog (SQLite) and blobs coexist in one `.marc` file. Writing is append-only: blobs first, then the compressed catalog at `Close()`. Reading starts from the footer (EOF-24) to locate the catalog. This simplifies distribution and integrity checking.

### VACUUM INTO serialization
The catalog is an ordinary SQLite file during writes (with WAL and pragmas for performance). At `Close()`, `VACUUM INTO` produces a clean serialized copy, which is then zstd-compressed and embedded as a catalog chunk. This avoids custom serialization while reusing SQLite's on-disk format.

### Content-addressable dedup (BLAKE3)
Every blob is keyed by its BLAKE3-256 hash. Identical content from different files shares one blob, regardless of filename or path. This enables cross-directory dedup without explicit comparison.

### Solid blocks
Multiple small blobs are concatenated into a single zstd frame. The compressor exploits cross-file repetition (e.g., common import headers, config keys) that it would miss compressing each file independently.

### Worker pipeline with sequencer
Hashing is parallelized across N workers. A sequencer goroutine re-orders results back to scan order before the single writer goroutine, ensuring `parent_id` references are always valid at insert time.

### Batched transactions
SQLite commits are issued every 1000 entries instead of per-entry. This reduces fsync overhead by ~1000×. The tradeoff (loss of last ≤999 entries on crash) is acceptable for an archiving tool.

### Conservative planner (`gain > cpu`)
The planner declines to apply a transform when estimated savings don't exceed estimated cost. This prevents expensive transforms (JSON parsing, log template extraction) from degrading throughput on files where they provide little benefit.

### Error on unknown transform at extract time
If an archive was created with `transform-foo/v1` and the current binary doesn't have it, extraction returns an error rather than silently copying the (transformed, not original) blob. This prevents data corruption on version mismatch.

### Path traversal defense
During extraction, every resolved path is validated to remain within `destDir` (Zip Slip mitigation). Symlink targets are also validated to not point outside the extraction sandbox.

### Lossy transforms as opt-in
`json-canonical`, `license-canonical`, and `log-template` restore a canonical form, not the original bytes. They are not in the default Registry until a mechanism exists to store the original alongside the canonical form. Keeping them opt-in prevents silent data loss for users who don't understand the trade-off.

---

*Source of truth for the binary format: `pkg/marc/format.go`*  
*Source of truth for the schema: `pkg/marc/format.go:SchemaDDL`*
