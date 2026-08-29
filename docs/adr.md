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
- Superseded as an approach by ADR-019 (A4): a solid block's own extension group supplies a better dictionary than a trained one, with no training step and no frozen artifact. The 0.4% recorded here was also measured against a dictionary that solid mode never consulted.

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

---

## ADR-019: Inter-block continuity, a group's first block is the dictionary of the rest

**Status**: Accepted
**Date**: 2026-09-26

### Context

ADR-017 sized the zstd window to the solid block, so a block's last byte can reference its first. That closes the window axis *inside* a block and leaves it open *across* blocks: an extension group larger than one block still restarts from zero context at every boundary. On the kubernetes tree, `.go` is 166.3 MB and spans 6 blocks of 32 MiB; blocks 1 to 5 each begin by re-deriving a model of Go source that block 0 already paid for.

This is the axis the ablation axiom admits: a bounded matching window beyond the block size, not local textual redundancy. It also replaces the static embedded dictionaries of the compression roadmap's Item 4. A dictionary here is real content from the corpus being archived, so there is no training step and no frozen artifact whose relevance decays.

A throwaway probe against klauspost measured the ceiling before any production code was written, reconstituting 32 MiB blocks per extension and encoding blocks 1..n with `WithEncoderDictRaw` at level 11:

| group | size | blocks | gain |
|---|---|---|---|
| `.go` | 166.3 MB | 6 | -1.42% |
| `.json` | 42.6 MB | 2 | -13.08% |

The gain tracks content, not volume: JSON gains nine times more than Go from a group four times smaller. Only multi-block groups gain at all, and on the kubernetes tree only two extensions exceed 32 MiB.

### Decision

Blocks 1..n of an extension group are compressed with the **raw** bytes of that group's first block as a zstd dictionary (patch-from semantics). Block 0 of every group stays plain, which is what bounds the dependency depth.

1. **Write side.** The accumulator retains a copy of the group's first block and releases it when the group ends, so exactly one dictionary (at most one block, 32 MiB by default) is resident at a time. `setExtension` already marks group boundaries; a flush there now also ends the group.
2. **Encoder structure.** A dictionary is fixed at encoder construction, so the dictionary travels on the job and a worker rebuilds its encoder when the job's dictionary differs from the one it holds. Blocks of a group arrive consecutively, so this is one rebuild per group boundary rather than one per block: 15 blocks on the kubernetes tree, 5 of them dictionary-compressed across 2 groups. Building a fresh encoder per dictionary-compressed block was rejected because level-11 match-table construction dominates a flush, which is the same reason the pool reuses encoders at all. The submission-order write and the single-goroutine catalog transaction are untouched.
3. **Sole-member blocks are always plain.** A block holding exactly one blob is streamed on read with no dictionary available, so it is neither dictionary-compressed nor retained as a dictionary. This covers the oversized path *and* a blob that merely fills a block on its own: the first implementation excluded only the former and produced an unreadable archive on the full kubernetes corpus, where a 33 MB `.idx` file followed a 644 KB pooled block. The reader's invariant check caught it rather than a silent corruption.
4. **Catalog.** `blobs.dict_block_id` records, for a dictionary-compressed block, the `block_id` whose raw content is its dictionary; NULL means plain. No index, per ADR-018.
5. **Read side.** A non-NULL `dict_block_id` means decoding that block first. The dictionary is held in its own field rather than in `solidCache`, whose 4-entry FIFO would evict it partway through a group and force a re-decode for every later block. A dictionary block that itself claims a dictionary is rejected as a forged catalog rather than recursed into.
6. **The dictionary is gated on a group that can span blocks.** Only an extension group whose files total more than the solid block size produces a second block, so only such a group can gain anything, while every retaining group costs a block copy plus a second level-11 encoder in the pool. The archiver already drains the whole scan before it sorts files by extension, so it sums `Info.Size()` per extension there (`runtime.solidDictExtensions`, no extra I/O and no extra pass) and passes the qualifying set to the writer as `store.WithSolidDictExtensions`. The threshold is strictly greater than one block: a group that exactly fills one block produces one block and has nothing to chain. An unset set means no group is eligible, so a caller that cannot compute it pays no memory for a gain it cannot predict. Eligibility is decided at the group boundary and held for the group, because a dictionary can only be used by a block that follows the retained one: an eligibility turning true mid-group would arrive after the block it would have retained was released. Raw totals over-estimate block count (a group's blocks are compressed), which admits a few groups that turn out to produce one block; that costs one retained block nothing reads and never affects correctness.
7. **`--no-inter-block-dict` disables the axis outright**, so the ablation study can measure it (`.claude/doc/ABLATION_PROCEDURE.md` section 0). It follows `--no-solid` as a boolean opt-out on a mechanism that is otherwise on, and is deliberately not folded into `--dict-compress`, which is a different and measured-dead mechanism.

No margin was added above one block. The measurement did not motivate one: on the kubernetes tree the two qualifying groups are `.go` at 166.3 MB and `.json` at 42.6 MB, both many times the 32 MiB block, and no corpus measured produced a group in the narrow band just above one block where a tiny second block might not repay its encoder. A margin would be a guess about a case none of the four corpora exhibits, so the rule stays the arithmetic one.

Measured in archive bytes, against the previous default:

| corpus | before | after | delta | pp |
|---|---|---|---|---|
| kubernetes tree (263 MB, no `.git`) | 29 402 427 | 28 998 973 | -403 454 | **-1.372** |
| kubernetes full (590 MB, with `.git`) | 267 346 529 | 266 942 017 | -404 512 | -0.151 |
| react | 17 895 568 | 17 896 545 | +977 | +0.005 |
| loghub | 2 048 557 | 2 048 634 | +77 | +0.004 |

The kill criterion was 0.5 pp on kubernetes. The tree corpus, which is what the probe measured and what isolates the compressible material, returns -1.372 pp against a -1.38 pp prediction. The same ~404 KB on the full corpus is only -0.151 pp because 267 MB of that archive is incompressible git packfiles that no preprocessor touches; the gain is identical in bytes, diluted in percentage.

The gate's effect, measured as medians of three interleaved runs per point (archive bytes and peak RSS):

| corpus | largest group | HEAD size / RSS | ungated size / RSS | gated size / RSS | chained blocks |
|---|---|---|---|---|---|
| kubernetes tree | `.go` 166.3 MB | 29 402 427 / 766 MB | 28 998 973 / 1090 MB | 28 998 973 / 1133 MB | 5 of 15 |
| kubernetes full | `.go` 166.3 MB | 267 346 529 / 927 MB | 266 942 017 / 1246 MB | 266 942 017 / 1245 MB | 5 |
| react | `.js` 18.7 MB | 17 895 568 / 475 MB | 17 896 545 / 466 MB | 17 896 545 / 472 MB | 0 |
| loghub | `.csv` 6.2 MB | 2 048 557 / 279 MB | 2 048 634 / 304 MB | 2 048 634 / 265 MB | 0 |

The gate is exactly neutral where the gain is real: both kubernetes archives keep their byte-identical gain and their memory, because both have groups that genuinely span blocks.

Measuring the corpora that gain nothing corrected an assumption behind this work. react and loghub were expected to be paying the memory cost for no gain, and they were not: **neither ever produced a single dictionary-compressed block**, even ungated. Their largest extension groups (18.7 MB and 6.2 MB) are under the 32 MiB block, so no group had a second block to chain and the write path never retained a dictionary. Their +977 and +77 bytes are not a dictionary but the all-NULL `dict_block_id` column, and their RSS was never elevated. The pre-gate mechanism was therefore already self-limiting on those corpora; what the gate adds is that the limit is now *stated as a rule* and enforced before any block is retained, rather than emerging from block arithmetic after the fact. The gate is what makes the property checkable, and three tests now pin it.

### Rejected: a `blocks` table

The proposal made `dict_block_id` a column of a new `blocks` table, since `offset` and `clen` already repeat on every blob row of a block. Measured instead as an additive column on `blobs`: the catalog chunk grew 1309 bytes for 22 911 blobs, about 0.06 compressed bytes per blob, because the value is constant across a block's rows and zstd encodes that away. The duplication costs nothing worth a schema migration, which is the same finding ADR-018 recorded for `offset`/`clen`.

### Consequences

- (+) Gain on the one axis zstd cannot see by itself: shared context beyond the block size. It costs no training, no embedded artifact and no second pass over the data.
- (+) Backward compatible in both directions. The column is additive, so an archive written before this change reads with no behavior change at all (`detectBlobColumn` probes for it, following the `block_id` precedent), and every block of such an archive is plain. Archives written with it are unreadable by binaries that predate it, which is correct and loud: a decoder handed no dictionary fails with `unknown dictionary` rather than decoding wrong.
- (+) Dependency depth is bounded to 1 by construction, so random access stays bounded: reading any blob costs at most its own block plus one dictionary block. The reader asserts the invariant instead of trusting it.
- (+) The memory cost now lands only where the gain does. A corpus with no group larger than a block retains no dictionary and builds no second encoder, so it keeps the previous write path exactly; only a corpus whose groups genuinely span blocks pays. The gate reads per-extension totals the scan has already collected, so the decision itself is free.
- (+) The axis is ablatable. `--no-inter-block-dict` returns the kubernetes tree to HEAD's peak RSS (771 MB against 766 MB) and to 0 dictionary-compressed blocks, which is what makes the axis measurable independently rather than inferable from a size delta.
- (-) Peak RSS archiving the kubernetes tree rises from ~766 MB to ~1133 MB, and the full corpus from ~927 MB to ~1245 MB, on the corpora that qualify. The retained dictionary is only 32 MiB of that; the rest is plain and dictionary-bearing encoders coexisting in the pool, each holding about 104 MB of level-11 match tables for a 32 MiB window. This compounds the ceiling problem ADR-017 already flagged, and the gate bounds which corpora meet it rather than lowering it.
- (-) Wall time on the tree rises from 4.79 s to 5.18 s medians (+8%) over three interleaved runs, from the per-group encoder rebuilds. Unqualified corpora are unaffected.
- (-) Nothing gained where no extension group exceeds the block size, which is most corpora. react and loghub move by under 0.01 pp, in the wrong direction, entirely from the all-NULL `dict_block_id` column: with the gate, neither retains a dictionary, and neither did before it.
- (-) Archiving now drains the whole scan before opening the writer, because the gate needs per-extension totals over the entire tree (over every root, for a multi-root archive). Directory and file metadata were already fully buffered before any content was read, so this moves no memory; it does mean a scan error surfaces before the output file is created rather than after.
- (-) `--no-inter-block-dict` does not reproduce a pre-A4 archive byte for byte: the tree comes back to 29 403 885 against HEAD's 29 402 427, the 1458-byte difference being the all-NULL `dict_block_id` column and its DDL, consistent with the 1309 bytes measured for the column above. The flag removes the mechanism, not the schema.
- Supersedes the static-dictionary approach of ADR-011 and the compression roadmap's Item 4.

---

## ADR-020: A transform can be registered for decode only

**Status**: Accepted
**Date**: 2026-09-26

### Context

`log-date-subst/v2` was registered and always on, and it cost archive bytes. Measured on `experiments/ablation/corpus/loghub`, three interleaved runs per point:

| variant | archive bytes | wall |
|---|---|---|
| as shipped | 2 048 634 | 0.18 to 0.21 s |
| `--disable-transform log-date-subst/v2` | 2 047 879 | 0.09 s |

Disabling it saved 755 bytes and halved the wall time. `--explain` showed why the regression stayed invisible: the transform fired on 1 file out of 100 (`Windows_2k.log`) and claimed roughly 92.9 KB saved.

The mechanism is in the transform's own doc comment. It replaces each timestamp with a 10 to 13 byte token of which 8 are a `unix_nanos int64` big-endian. Those 8 bytes are near-incompressible, while the text they replace (`2016-09-29 02:04:xx`) is highly redundant line to line and is exactly what zstd models well. The transform therefore trades a compressible pattern for an incompressible one, and it does so losslessly, so nothing in the round-trip tests could catch it.

The root cause of it ever being selected is `CostEstimate`. It counts the text bytes removed and ignores the binary bytes added, so its estimate bears no arithmetic relation to the archive delta. A cost model that measures only one side of a substitution cannot predict a substitution's effect, and the planner's `gain > cpu_cost` rule trusted that estimate.

Deleting the entry from `plan.Registry` was not available. `runtime.resolveTransform` searches the registry by ID and fails with `archive requires transform %q which is not registered` when absent (ADR-012), so removing the entry would make every existing archive containing a logdate-transformed file unextractable. Retiring a transform from selection and removing its decoder are different decisions, and only the first one is wanted here.

### Decision

A registry entry carries a write capability, so one list stays the single source of truth for both paths.

1. **`plan.Registry` becomes `[]plan.Entry`**, where `Entry` pairs a `marc.Transform` with a `Write bool`. Read capability is implicit and always true: presence in the registry means the transform is decodable, because removing it would break archives that already use it, and there is no case where a registered transform should be undecodable. A single field expresses the whole useful state space, and the invariant "registered implies decodable" becomes structural rather than a convention. A bitmask was rejected: the state space is closed and a named field reads better than decoding `1|2`.
2. **The write-side chain filters on `Write`.** `store.writeFileWithSHA` skips an entry whose `Write` is false before any `Applicable` or `CostEstimate` call, so a decode-only transform costs the write path nothing.
3. **The extract path iterates the whole registry, unfiltered.** `resolveTransform` needed no logic change beyond the field access: an archive written by an older binary resolves its transform exactly as before.
4. **`log-date-subst/v2` is present with `Write: false`.** It is never selected when archiving and always available when extracting.
5. **`RegistryIDs` returns only the write-selectable IDs.** It feeds the `--disable-transform` help text and the archive's `transforms` meta value, and both describe choices a caller can make. Offering a transform that can no longer be selected, or recording it as part of an archive's active set, would be misleading.
6. **`js-line-subst/v1` and `py-line-subst/v1` are registered with `Write: false`.** They were previously absent from the registry and explained by a 12-line comment. Registering them decode-only puts the intent in the code, and makes any archive that ever carried them extractable.

Measured after the change, three interleaved runs per point against the previous binary:

| corpus | before | after | delta |
|---|---|---|---|
| loghub | 2 048 634 | 2 047 871 | -763 |
| kubernetes tree (263 MB, no `.git`) | 28 998 973 | 28 998 968 | -5 |
| react | 17 896 545 | 17 896 802 | +257 |

loghub saves 8 bytes more than `--disable-transform` does, because the flag leaves the `transforms` meta string intact while the retirement drops `,log-date-subst/v2` from it: 18 uncompressed characters that cost 8 bytes in the compressed catalog.

The react figure is the same mechanism in the other direction, and it is not a data-path change. The blob regions of the before and after archives are byte-identical at 17 513 856 bytes, and the plan log is identical entry for entry (255 dedup, 2 license-canonical, 27 passthrough, 6616 raw), so logdate applied to nothing there before or after. The whole +257 is in the compressed catalog, whose only content difference is the shorter meta string. Level-11 zstd on a 382 KB catalog is not monotonic in input size, so removing 18 bytes of a compressible string cost 257 compressed bytes on react and saved 8 on loghub. This is the class of effect ADR-019 recorded for its all-NULL `dict_block_id` column.

### Rejected: a second registry for decode-only transforms

A separate `DecodeRegistry` would keep `Registry` typed as `[]marc.Transform` and leave every existing call site untouched. It was rejected because it splits the invariant across two lists: a transform present in one and absent from the other is a silent bug, and every future reader has to know which list to consult for which path. The capability belongs to the entry, not to which list it was filed in.

### Rejected: fixing `CostEstimate` instead

The estimate could account for the binary bytes it adds, which would make the planner decline the transform on its own. That is the better fix in principle and it is not sufficient here: the transform would still be selected wherever its corrected estimate happened to come out positive, and the substitution it performs is adverse to the final compressor by construction, not only when the arithmetic says so. Retiring it from selection states the finding; correcting the estimate would only move the threshold. The `CostEstimate` defect is recorded here so the next transform's cost model is not written the same way.

### Consequences

- (+) Every existing archive still extracts, byte-exactly, including one whose entries carry `log-date-subst/v2`. A test builds such an archive through the registry seam and round-trips it, so the guarantee is pinned rather than asserted.
- (+) The loghub regression is gone with no flag needed, and wall time on that corpus halves from 0.18 to 0.09 s because the transform no longer scans and rewrites its one matching file.
- (+) Retiring a transform is now a one-field change with no effect on the read path, so a future transform that measures badly can be withdrawn without a format decision.
- (+) `jsline` and `pyline` stop being documented-by-absence. `marc --help` reports all three as `decode-only` rather than omitting them.
- (-) The `transforms` meta value of a new archive no longer lists every transform the binary can decode, only the ones its planner could select. A reader wanting the decode set must consult the binary, not the archive. Nothing in the codebase reads this value back; it is recorded for forward compatibility.
- (-) react gains 257 bytes and the kubernetes tree loses 5, both from catalog compression noise on a changed meta string rather than from any change to stored content. The effect is under 0.002 pp and the blob regions are provably identical.
- (-) `plan.Registry` is now a package-local type rather than `[]marc.Transform`, so a test that substitutes a registry constructs `plan.Entry` values. One existing test helper (`internal/plan/plan_test.go:withRegistry`) was updated for this.
- Closes P0.1 of `.claude/doc/metacompression-proposals.md`. The broken syslog padding preservation contract that item also records was already addressed separately by `encodeDisabledFmts`, which matches `fmtSyslog`, `fmtLog4j` and `fmtPython` on decode but never emits them.
