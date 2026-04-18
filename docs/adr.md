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
`solidAccumulator` concatenates consecutive blobs into a single zstd frame (default threshold: 4 MB). All blobs are eligible, not just small ones. Enabled by default, can be disabled with `--no-solid`.

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

## ADR-014: SQL schema divergence from the initial spec

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
