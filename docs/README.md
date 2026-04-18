# metarc

Meta-compression archiver. Operates above a final byte-stream compressor (zstd) to reduce entropy before it sees the data.

## What metarc is

Metarc sits between deduplicating backup tools (Borg, Restic) and format-specialized compressors. Where dedup tools eliminate identical files and format compressors exploit structure within a single file, metarc targets cross-file redundancy in text corpora: source code, configs, logs, and structured datasets.

It uses a SQLite-backed catalog to track every file in the tree and a content-addressable blob store to deduplicate identical content. Before writing each blob, the planner selects which semantic transform (if any) to apply. If the estimated gain does not exceed the overhead, no transform is applied.

Archives use the `.marc` extension.

## Archive format

A `.marc` file is a single self-contained binary:

```
[Magic 8B: "METARC\x01\x00"]
[Blob region: chunks of type 0x01 (single blob) or 0x03 (solid block)]
[Catalog chunk: [Type=0x02][Len uint32 BE][zstd-compressed SQLite]]
[Footer 24B: catalog_offset(8B) | blob_region_offset=8(8B) | blake3[:8](8B)]
```

Reading an archive: seek to EOF-24, parse the footer, seek to `catalog_offset`, decompress the SQLite database to a temp file, query entries and blob offsets, seek into the blob region to retrieve content.

`DetectFormat()` auto-detects legacy split-file archives (SQLite + `.blobs` sidecar) for backward compatibility.

## CLI reference

### archive

Create a `.marc` archive from a source directory.

```sh
metarc archive <output.marc> <source-dir> [flags]
```

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--final-compressor` | `zstd` | Blob compressor: `zstd` or `none` |
| `--no-solid` | false | Disable solid block compression (fall back to per-blob) |
| `--solid-block-size` | `4MB` | Solid block size threshold |
| `--dict-compress` | _(off)_ | Dictionary compression mode: `prescan` or `simple` (experimental, see below) |
| `--keep-plan-log` | false | Retain transform decisions in the archive for later inspection |
| `--explain` | false | Retain plan decisions and print a summary after archiving |
| `--workers N` | NumCPU | Number of parallel analysis workers |

```sh
metarc archive repo.marc ./my-repo
metarc archive repo.marc ./my-repo --no-solid --explain
metarc archive repo.marc ./my-repo --solid-block-size 8MB
```

### extract

Extract a `.marc` archive to a directory.

```sh
metarc extract <archive.marc> [-C <dest-dir>]
```

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-C` | `.` | Destination directory |

```sh
metarc extract repo.marc -C restored/
```

### inspect

Print the archive manifest and stored metadata.

```sh
metarc inspect <archive.marc> [flags]
```

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--plan-log` | false | Pretty-print plan decisions stored in the archive (requires `--keep-plan-log` at archive time) |
| `--raw` | false | Print the sqlite3 command to open the catalog directly |

```sh
metarc inspect repo.marc
metarc inspect repo.marc --plan-log
metarc inspect repo.marc --raw
```

### bench

Benchmark archive performance and compression ratios against a corpus directory.

```sh
metarc bench <source-dir> [flags]
```

Flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--compressor` | `zstd` | Blob compressor: `zstd` or `none` |
| `--workers N` | NumCPU | Number of parallel analysis workers |
| `--output` | _(temp)_ | Path for the generated `.marc` (deleted after bench if not set) |
| `--json` | false | Emit JSON instead of human-readable output |

```sh
metarc bench ./my-repo --json
```

## Transforms

Each transform is versioned and reversible. The planner declines to apply a transform when estimated gain is less than the CPU and storage overhead.

| Transform ID | Status | Description |
|---|---|---|
| `dedup/v1` | Implemented | Content-addressable deduplication via BLAKE3. Identical files are stored once. |
| `license-canonical/v1` | Implemented | Replaces known license texts (MIT, Apache 2.0, BSD, ISC) with a canonical form. |
| `json-canonical/v1` | Implemented | Re-encodes JSON files with sorted keys and minimal whitespace. |
| `log-template/v1` | Implemented | Drain-style log template extraction. Groups log lines by structural pattern. |
| `near-dup-delta/v1` | Stub | Always declines. Reserved for future near-duplicate delta encoding. |

## Solid block compression (default)

By default, metarc groups blobs into solid blocks (4 MB threshold) and compresses each block as a single zstd frame. This lets the compressor exploit cross-file redundancy — repeated license headers, import blocks, YAML keys, and shared string patterns are compressed once across all files in the block.

On the compose benchmark, solid blocks reduce the archive to **96.4% of tar.gz size** (vs 123% without). Extracting a single file requires decompressing the entire block it belongs to, but sequential extraction (the common case) is equally fast thanks to an LRU cache.

Disable with `--no-solid` to fall back to per-blob compression. Adjust the block size with `--solid-block-size` (default `4MB`).

## Dictionary compression (experimental, inefficient)

`--dict-compress` trains a zstd dictionary on a sample of small files. On real-world repositories the gains are negligible (0.4% on compose) and the dictionary overhead (32 KB) often negates the savings. **This feature is unlikely to justify its complexity** and may be removed. Prefer solid blocks.

## Build

```sh
make build
```

## Test

Always run the full suite via Make:

```sh
make test
```

To target a single test:

```sh
go test ./internal/scan/... -run TestWalk
```

## Other targets

```sh
make tidy    # fmt + mod tidy
make audit   # vet + staticcheck
make cover   # test + HTML coverage report
```

## Documentation

| Document | Description |
|----------|-------------|
| [`docs/architecture.md`](docs/architecture.md) | Full architecture reference: binary format, SQLite schema, transform system, storage features, worker pipeline, planner heuristic |
| [`docs/metacompression.md`](docs/metacompression.md) | Conceptual background: what meta-compression is and how it differs from byte-stream compression |
