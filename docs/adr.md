# Architecture Decision Records — Metarc

Decisions made during the Go rewrite of `jntar`. Each ADR captures context, the choice made, and consequences. The initial spec (`INITIAL_OBJECTIVE.md`) is the baseline; divergences are noted explicitly.

---

## ADR-001: Go as the implementation language

**Status**: Accepted  
**Date**: 2025-01

### Context
The original `jntar` POC is JavaScript (Node.js). The spec called for a production-grade rewrite with a modular architecture, good CPU performance (parallel hashing), and a statically linked binary that is easy to distribute.

### Decision
Go. Static compilation (`CGO_ENABLED=0`), native goroutines for the parallel pipeline, SQLite available via a pure-Go driver, BLAKE3 library available.

### Consequences
- No runtime to ship; single cross-platform binary
- `CGO_ENABLED=0` rules out standard CGo SQLite bindings → pure-Go driver (`modernc.org/sqlite`)
- Parallel hashing via goroutines with no friction

---

## ADR-002: SQLite as the catalog backend

**Status**: Accepted  
**Date**: 2025-01

### Context
The spec was explicit: "do not invent a custom database ('ArnaudDB')". The catalog must support complex queries, joins, and aggregations over millions of files.

### Decision
Pure-Go SQLite (`modernc.org/sqlite`). Schema defined in `pkg/marc/format.go:SchemaDDL` — single source of truth.

### Consequences
- (+) Expressive queries, ACID transactions, zero external dependency
- (+) `VACUUM INTO` enables clean catalog serialization at archive close
- (-) No concurrent writers → the pipeline requires a single writer goroutine (see ADR-008)
- The `nodes` + `content_signatures` + `analysis_*` tables from the initial spec were simplified into `entries` + `blobs` + `names` + `entry_blobs` (see ADR-014)

---

## ADR-003: Single-file `.marc` format

**Status**: Accepted  
**Date**: 2025-02  
**Supersedes**: split-file format (SQLite + `.blobs` sidecar)

### Context
The spec described: header + manifest + object store + footer, without mandating single-file vs split-file. The initial implementation used two files (`archive.marc` = SQLite, `archive.marc.blobs` = raw blobs). This complicated distribution and integrity checking.

### Decision
Single-file format: magic (8B) → blob region → catalog chunk (zstd-compressed SQLite) → footer (24B). Reading starts by seeking to EOF-24.

### Consequences
- (+) One file to move, copy, or verify
- (+) BLAKE3 checksum covers the entire content
- (-) Two-phase write: blobs first, catalog embedded at close via `VACUUM INTO`
- Backward compatibility: `DetectFormat()` auto-detects legacy split-file archives

---

## ADR-004: BLAKE3-256 for content addressing

**Status**: Accepted  
**Date**: 2025-01

### Context
The spec mentioned BLAKE3 and "quick signature". A collision-resistant hash is needed for dedup; a fast hash is needed for pre-filtering.

### Decision
- **FullHash**: BLAKE3-256 over the entire file → dedup key in `blobs.sha`
- **QuickSig**: 16 bytes = BLAKE3(head 4KB || tail 4KB), for files larger than 8KB

### Consequences
- BLAKE3 is faster than SHA-256 on modern CPUs (AVX2/NEON)
- QuickSig is implemented but the multi-pass pipeline (size → QuickSig → FullHash) is **not yet active** — all files go straight to FullHash (see ADR-009)

---

## ADR-005: VACUUM INTO for catalog serialization

**Status**: Accepted  
**Date**: 2025-02

### Context
The catalog is an ordinary SQLite database during writes (WAL mode, performance pragmas). At close time, it must be serialized and embedded in the `.marc` file.

### Decision
`VACUUM INTO tmpfile` produces a clean, defragmented copy with no WAL pages. The temporary file is then read, zstd-compressed, and written as chunk `0x02`.

### Consequences
- (+) No custom serialization: reuses SQLite's own on-disk format
- (+) The VACUUM copy is compact and portable
- (-) Requires temporary disk space (~size of the catalog)

---

## ADR-006: Solid blocks as the default storage feature

**Status**: Accepted  
**Date**: 2025-03  
**Replaces**: `small-text-pack` from the initial spec

### Context
The spec listed `small-text-pack` as an MVP priority: group small similar files together to improve the compression window. The implementation chose a more general approach.

### Decision
`solidAccumulator` concatenates consecutive blobs into a single zstd frame (default threshold: 32 MiB, raised from 4 MiB on 2026-05-03 and from 16 MiB on 2026-08-14; see ADR-017). The accumulator also flushes on a file-extension change once the block holds at least 1 MiB, so large blocks stay extension-pure while rare extensions pool together. All blobs are eligible, not just small ones. Enabled by default, can be disabled with `--no-solid`.

### Consequences
- (+) Exploits cross-file redundancy (common import headers, YAML keys, license preambles) without any format knowledge
- (+) The compressor sees a larger context than a single isolated file
- (-) Decompressing one blob within a solid block requires decompressing the whole block (mitigated by an LRU cache of 4 blocks)
- The `small-text-pack` concept from the spec is dropped in favor of this content-agnostic approach

---

## ADR-007: Conservative planner — `gain > cpu` (strict)

**Status**: Accepted  
**Date**: 2025-01

### Context
The spec's "golden rule": the planner must be able to say "I won't do anything here" when estimated gain is less than CPU/overhead cost. The question was whether equality counts as a reason to apply or skip.

### Decision
Strict condition: `gain > cpu`. On a tie, the transform is skipped. Every decision is logged in `plan_log`.

### Consequences
- (+) Prevents expensive transforms from firing where they bring no benefit (e.g., JSON canonicalization on tiny files)
- (+) `plan_log` enables post-hoc analysis via `marc inspect --plan-log`
- Cost/gain heuristics are size-proportional; no I/O in the planning phase

---

## ADR-008: Single-writer pipeline with sequencer goroutine

**Status**: Accepted  
**Date**: 2025-02

### Context
SQLite has no concurrent writers. BLAKE3 hashing is CPU-bound and benefits from parallelism. Both constraints must be satisfied.

### Decision
4-stage pipeline: scan (1) → BLAKE3 workers (N=NumCPU) → sequencer (1) → writer (1). The sequencer buffers out-of-order results and forwards them in scan order.

### Consequences
- (+) Parallel hashing saturates all available cores
- (+) Scan order is preserved → `parent_id` FK references are always valid at insert time
- (+) Single writer avoids all SQLite lock contention
- The sequencer can accumulate memory if one worker is slow (`resultCh` cap: 256)

---

## ADR-009: Simplified analysis pipeline — direct BLAKE3

**Status**: Accepted (provisional)  
**Date**: 2025-02

### Context
The spec described a multi-pass pipeline: (1) group by size, (2) QuickSig for collisions, (3) FullHash only for survivors. The goal is to avoid unnecessary computation.

### Decision
For now, all files go directly to FullHash BLAKE3. The multi-pass pipeline is **planned but not implemented**.

### Consequences
- (+) Simpler code, uniform pipeline
- (-) CPU wasted on files with unique sizes (where no exact duplicate is possible)
- Revisit when benchmarks identify this as a bottleneck

---

## ADR-010: Batched SQLite transactions every 1000 entries

**Status**: Accepted  
**Date**: 2025-02

### Context
One COMMIT per entry causes one fsync per file — prohibitive on large corpora. A single global COMMIT at the end risks total data loss on crash.

### Decision
Commit every 1000 entries. A new `BEGIN` follows immediately.

### Consequences
- (+) Reduces fsyncs by ~1000×
- (-) Up to 999 entries may be lost on crash (acceptable for an archiving tool)
- The value 1000 is arbitrary; not exposed as a parameter yet

---

## ADR-011: Dictionary compression — experimental, candidate for removal

**Status**: Experimental  
**Date**: 2025-03

### Context
The idea: train a zstd dictionary on a sample of the corpus and use it for all blobs. The dictionary seeds zstd's internal model with knowledge of frequent patterns.

### Decision
Implemented (`--dict-compress`), stored in `meta.dict`. Two modes: prescan (upfront walk of up to 500 files) and online (samples collected during archiving).

### Consequences
- In practice, gains are marginal (≈0.4% on real-world repositories)
- Solid blocks (ADR-006) already exploit cross-file redundancy more effectively
- **This feature is a candidate for removal.** Do not build on top of it.

---

## ADR-012: Hard error on unknown transform ID at extract time

**Status**: Accepted  
**Date**: 2025-02

### Context
If an archive was created with `transform-foo/v1` and the extracting binary does not know that transform, the options are: silently ignore it, copy the (transformed) blob as-is, or fail.

### Decision
Hard error if `entries.transform` is non-empty and not found in the registry. No silent partial extraction.

### Consequences
- (+) Silent data corruption is impossible
- (-) Archives are not extractable with a binary that predates the transform
- A format versioning mechanism can relax this constraint later

---

## ADR-013: Lossy transforms require explicit opt-in

**Status**: Accepted  
**Date**: 2025-02

### Context
`json-canonical`, `license-canonical`, and `log-template` restore a canonical form, not the original bytes. The spec did not specify whether these should be on by default.

### Decision
These transforms are not in the default registry. They require an explicit flag. Only `dedup/v1` (lossless) is active by default.

### Consequences
- (+) Conservative default: extraction reproduces the exact source files
- (-) Users must explicitly opt in and understand the trade-off
- Revisit if a mechanism to store the original alongside the canonical form is added

---

## ADR-014: Line-level token substitution for source code (`go-line-subst/v1`)

**Status**: Accepted
**Date**: 2026-04

### Context
Empirical analysis on the Kubernetes corpus (16,931 Go files, 168.5 MB) revealed that 45% of all lines are repeated more than 100 times across files (`if err != nil {`, `return nil`, license headers, etc.). Four encoding approaches were benchmarked: 7-byte ASCII tokens (v1), 2-byte exact-match tokens (v2), 3-byte tab-normalized tokens (v3), and tab-preserved 2-byte tokens (v4). Only v2 was neutral on solid blocks; all tab-normalization variants degraded solid-block compression by 16-17%.

### Decision
Implement v2: exact-match, 2-byte tokens (`\x00` + 1-byte dictionary index). Static dictionary of 105 common Go patterns embedded as a Go array literal (no `//go:embed`). Placed before `dedup/v1` in the transform registry; BlobSink handles dedup internally. Streaming I/O via `bufio.Reader.ReadString('\n')`. Dictionary is immutable per transform version — changes require bumping to `go-line-subst/v2`.

### Consequences
- (+) +9.6% per-file compression gain on Go-heavy corpora
- (+) Neutral on solid blocks (+/-0.2%) — safe as default
- (+) Lossless, exact byte-identical round-trip
- (+) Extensible to other languages (Python, C, JS) with same mechanism, different dictionaries
- (-) +0.9s archiving overhead on Kubernetes (2.9s → 3.8s)
- (-) No benefit on non-Go files (falls through to plain dedup)
- Registry versioning added: `meta.transforms` records the active transform set

---

## ADR-016: Human-readable byte sizes use SI labels with binary (1024) divisors

**Status**: Accepted  
**Date**: 2026-05

### Context
CLI output (bench, explain/plan summary) displays byte sizes in human-readable form. Two conventions exist:
- **IEC**: labels KiB/MiB/GiB, divisors 1024/1024²/1024³
- **SI**: labels KB/MB/GB, divisors 1000/1000²/1000³

Most Unix tools (ls, du, df without `-H`) historically use KB/MB/GB with 1024-based divisors — a hybrid that is technically incorrect but widely understood by users of developer tooling.

### Decision
Use **SI labels (KB/MB/GB/TB) with binary divisors (powers of 1024)**. This matches the convention of common Unix tools and is what developers expect when inspecting archive or benchmark output.

### Consequences
- (+) Familiar to the target audience (developers, sysadmins)
- (+) Consistent across all size-formatting functions (`humanBytes` in bench, `formatBytes` in archive)
- (-) Technically imprecise: displayed "GB" means 2³⁰ bytes, not 10⁹

---

## ADR-015: SQL schema divergence from the initial spec

**Status**: Accepted  
**Date**: 2025-02

### Context
The spec envisioned: `nodes` (tree), `content_signatures` (hashes), `analysis_license`, `analysis_json`, etc. Those tables reflect a workflow where analysis is a separate phase from writing.

### Decision
Simplified schema oriented around direct archiving:

| Initial spec | Implementation |
|---|---|
| `nodes` | `entries` + `names` (separate interning) |
| `content_signatures` | `blobs` (sha, offset, clen, ulen) |
| `analysis_*` | removed — replaced by `entries.transform` + `entries.params` |
| *(no table)* | `entry_blobs` (many-to-many entries↔blobs) |
| *(no table)* | `plan_log` (planner decision log) |

### Consequences
- (+) More compact schema, suited to the archive-direct workflow (no separate analysis phase)
- (+) `plan_log` adds decision traceability that was absent from the spec
- (-) No `analysis_*` tables: format-specific analysis (JSON, license) is inline in the transforms, not stored separately
- Source of truth: `pkg/marc/format.go:SchemaDDL`

---

## ADR-017: Solid block geometry, the encoder window follows the block

**Status**: Accepted
**Date**: 2026-08-14

### Context

The 2026-05 ablation study (`experiments/ablation/`) established that solid blocks carry essentially all of metarc's gain over tar+zstd, while content transforms contribute nothing measurable on source corpora. That makes block geometry, not new transforms, the lever worth pulling.

Reading the encoder setup exposed a defect in the geometry itself. `ZstdConfig.WindowSize` was plumbed to all three encoders but never populated, so klauspost applied its per-level default of 8 MiB (identical for levels 3, 7 and 11). Blocks were 16 MiB. The tail of every full block could not reference its head: half of the 4 MiB to 16 MiB bump shipped in Item 3 was unreachable by the matcher.

A second defect affected small corpora. The accumulator flushed on every file-extension change regardless of size, so a corpus with many rare extensions produced dozens of undersized frames, each paying its own header and entropy tables while seeing no cross-file context. This is the mechanism behind the express (+2.0 pp) and docker-compose (+2.6 pp) regressions recorded in `docs/benchmarks.md` when blocks grew to 16 MiB.

### Decision

Three changes, each measured separately on corpora stripped of `.git` (git pack files are incompressible and would dilute every percentage):

1. The solid encoder's window defaults to the smallest power of two covering the block (`store.solidWindowFor`). Applied to the solid site only: an isolated blob or the catalog gains nothing from a wide window and would only cost RAM. `--zstd-window` overrides it everywhere, and the resolved value is recorded in `meta`.
2. An extension change flushes only once the block holds at least `DefaultMinSolidBlockSize` (1 MiB). Files arrive extension-sorted, so rare extensions pool on their own with no separate routing.
3. The default block size moves from 16 MiB to 32 MiB, now that widening it also widens the window.

Measured effect of the three together, against the previous default:

| corpus | delta |
|---|---|
| express | -2.61% |
| docker-compose | -2.43% |
| react | -0.79% |
| kubernetes | -1.65% |
| loghub | +0.003% (24 bytes of added meta) |

### Rejected: sorting files on (extension, basename, size)

The intra-extension order is scan order, so vendored copies of the same file land far apart. Sorting on basename (the git packfile heuristic) was implemented and measured, and it lost on every corpus: kubernetes +2.40%, express +0.92%, docker-compose +0.49%, react +0.32%. Directory locality, which the stable sort preserves, is a better proxy for content similarity on source trees than name similarity is. Reverted, with the finding recorded at the sort site so it is not retried blind.

### Consequences

- (+) Ratio improves on every corpus that has enough material to fill a block, and the small-corpus regression from Item 3 is recovered.
- (+) Write-side only: the frame header carries the window and klauspost's decoder accepts up to 512 MiB, so archives stay readable by binaries that predate this change.
- (-) Peak RSS on the kubernetes corpus rises to ~361 MiB archiving and ~349 MiB extracting, from 258/270 MiB. Both figures already exceeded the 256 MiB Phase-6 ceiling before this change; that ceiling needs re-deriving or the read cache (4 decompressed blocks, `reader.go`) needs shrinking.
- (-) 64 MiB blocks measured better still on kubernetes (-2.58% vs 16 MiB) but only there, for 148 MiB more peak RSS. Available via `--solid-block-size`.
- Item 5 of the compression roadmap (streaming segments) stays closed: the limit it targeted was the window, which is now sized correctly.

---

## ADR-018: The catalog carries no write-only structures

**Status**: Accepted
**Date**: 2026-08-14

### Context

Measuring the catalog rather than assuming its cost changed the priorities. On the kubernetes corpus (35 684 entries, 22 911 blobs) the catalog was 2.2 MB, or 7.4% of a 29.1 MB archive. That is not a rounding error, and it grows as files get smaller: on express it dominated.

Breaking the uncompressed catalog down with `dbstat` showed where the bytes were, and one class behaves unlike the rest. Hash bytes are uniformly random: 22 911 BLAKE3 values extracted and recompressed at zstd level 19 went from 733 152 to 733 183 bytes, so they cost their full width no matter what the final compressor does. Everything else in the catalog (row headers, ids, offsets, repeated transform names) compresses to roughly a third.

### Decision

Four changes, none of which alter what the archive can express:

1. **Write-only indexes are dropped before serialization.** `blobs.sha`, `names.name` and `entries.parent_id` are indexed to answer lookups the writer performs (dedup, name interning, parent resolution). Extraction joins names by id and walks entries by id, so no reader needs them. `UNIQUE` column constraints were replaced by explicit indexes to make them droppable, following the existing `idx_blobs_source_sha` precedent.
2. **`entry_blobs` is `WITHOUT ROWID`**, collapsing its table and its `(entry_id, seq)` autoindex into a single B-tree.
3. **`blobs.sha` stores a 16-byte prefix** (`marc.StoredSHALen`) once the archive is finalized. The truncation happens at finalize, so dedup, the transform result cache and the blob index all keep comparing full 32-byte hashes; only the serialized record narrows. At 128 bits, collision odds for a million-blob archive stay below 1e-26.
4. **The catalog is compressed at level 11.** It is compressed once from a fully buffered copy, so the level never touches the hot path.

Measured against the previous defaults, in archive bytes:

| corpus | delta |
|---|---|
| express | -10.69% |
| docker-compose | -9.91% |
| kubernetes | -5.17% |
| react | -4.90% |
| loghub | -0.37% |

The kubernetes catalog went from 2.2 MB (7.4% of the archive) to 1.1 MB (4.0%). Small corpora gain most, because the catalog is a larger share of a small archive.

### Consequences

- (+) The gain is arithmetic, not heuristic: it removes bytes rather than betting that a model predicts the data better. It applies to every corpus.
- (+) Dedup semantics are unchanged, so no archive is at greater risk of a false content match while being written.
- (-) `blobs.sha` is no longer a complete BLAKE3-256. Cross-archive deduplication (metacompression.md §11.5) remains possible but must compare prefixes. `Reader.QueryBlobSHAs` returns `[]byte` per blob rather than `[32]byte`.
- (-) A catalog opened directly with `sqlite3` (the `marc inspect --raw` workflow) has no index on `parent_id` or `names.name`; ad-hoc queries filtering on those columns will table-scan. Recreate them locally if needed.
- Not done, and measured as low value: moving the per-blob `offset`/`clen` of solid blobs into a `blocks` table. Those columns repeat identically across a block, so zstd already encodes them cheaply; the change is worth making only when `blocks` is needed for another reason, such as the inter-block dictionary of the compression roadmap.
