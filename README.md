# Metarc

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/arhuman/metarc-go)](https://goreportcard.com/report/github.com/arhuman/metarc-go)
[![Tests](https://github.com/arhuman/metarc-go/actions/workflows/test.yml/badge.svg)](https://github.com/arhuman/metarc-go/actions/workflows/test.yml)

**Compress structure before bytes.**

Metarc is an experimental archiver exploring *metacompression*:  
reducing structural and semantic redundancy across files before applying standard compression.

---

## What is metacompression?

Traditional compressors (like `gzip`, `zstd`) operate on byte streams.

Metarc explores a different idea:

> **compress meaning first, bytes second**

Instead of only compressing raw data, it tries to:
- deduplicate repeated content across files
- normalize structured formats (JSON, logs, etc.)
- detect common patterns (licenses, boilerplate, generated code)

Then it applies a standard compressor on top.

The goal is to unlock optimizations that byte-level compression alone cannot see.

---

## Current status

Metarc is **experimental, but already usable**.

- Works on real repositories
- Supports multiple transforms and strategies
- Designed for experimentation and iteration

---

## Why Metarc exists

Metarc is not (yet) trying to replace `tar`.

It exists to explore a different space:

- cross-file compression
- semantic transforms
- corpus-aware optimization
- new compression heuristics

Think of it as a **playground for compression ideas**, not a finished product.

---

## Performances

More detailed Benchmarks as well as instructions to produce yours are available in [docs/benchmarks.md](docs/benchmarks.md)

### Compression

Metarc compression shines in directory with a lot of redundancy where it's file dedup outperforms even tar + zstd :

```Bash
6.5G	code_perso
1.4G	code_perso.marc
1.8G	code_perso.tar.zst
```

But the goal is to make it at least "as good" in most common cases, that's why we mainly use standard popular repositories (using various languages) to measure our progress in this area.

Previous comparisons used `tar + gzip`, we now use `tar + zstd` for a fairer comparison.

_ marc: metarc version v0.6.0-7-gc68d3c0-dirty (c68d3c0, 2026-04-24T15:55:29Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _

| Repo | Original size | Files | tar+zstd | tar size | marc | marc size | % size of tar |
|------|---------------|-------|---------------------|----------|------|-----------|---------------|
| kubernetes | 327M | 29813 | 0m12.580s | 36.1M | 0m4.270s | 36.0M | 99.9% |
| docker-compose | 3.7M | 677 | 0m0.403s | 448.8K | 0m0.093s | 474.3K | 105.7% |
| vuejs | 7.6M | 703 | 0m0.327s | 1.6M | 0m0.106s | 1.6M | 102.3% |
| numpy |  40M | 2339 | 0m0.904s | 9.0M | 0m0.395s | 9.0M | 101.1% |
| redis |  23M | 1755 | 0m0.652s | 4.2M | 0m0.279s | 4.4M | 103.2% |
| bootstrap |  20M | 791 | 0m0.325s | 7.0M | 0m0.140s | 6.7M | 95.9% |
| express | 1.3M | 213 | 0m0.101s | 146.4K | 0m0.034s | 152.7K | 104.3% |
| react |  54M | 6859 | 0m2.608s | 8.5M | 0m0.955s | 8.4M | 98.9% |


### Speed

> [!NOTE]
> Early speed comparisons against `tar + gzip` overstated Metarc’s advantage: most of the gap came from using `zstd`, not from Metarc’s architecture alone.
>
> A fairer comparison against `tar + zstd` shows that Metarc is not yet competitive with an optimized tar-based pipeline.
>
> Its value today is different: Metarc is already a usable playground for exploring metacompression ideas, structural transforms, and cross-file compression strategies.

---

## Usage

### Install

```bash
git clone https://github.com/arhuman/metarc-go
cd metarc-go
make install
```
This installs `marc` to your `$GOBIN` (or `$GOPATH/bin`).

## Test

```bash
make test
```

### Create an archive

```bash
marc archive repo.marc ./my-repo
```

### Extract

```bash
marc extract repo.marc --dest restored/
```

### Inspect

```bash
marc inspect repo.marc
```

### Benchmark

```bash
marc bench ./my-repo
```
---

## Documentation

* [`docs/metacompression.md`](docs/metacompression.md) conceptual background
* [`docs/architecture.md`](docs/architecture.md) format, pipeline, transforms
* [`docs/benchmarks.md`](docs/benchmarks.md) benchmarks

---

## Contributing

- :star: **Star this repo** if you find it useful
- :bug: **[Report a bug](https://github.com/arhuman/metarc-go/issues/new?template=bug_report.md)**
- :bulb: **[Suggest a feature](https://github.com/arhuman/metarc-go/issues/new?template=feature_request.yml)**
- :wrench: **[Propose a transform](https://github.com/arhuman/metarc-go/issues/new?template=transform_idea.yml)**

---

## License

MIT -- see [LICENSE](LICENSE).

