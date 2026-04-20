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

> [!NOTE]
> Early speed comparisons against `tar + gzip` overstated Metarc’s advantage: most of the gap came from using `zstd`, not from Metarc’s architecture alone.
>
> A fairer comparison against `tar + zstd` shows that Metarc is not yet competitive with an optimized tar-based pipeline.
>
> Its value today is different: Metarc is already a usable playground for exploring metacompression ideas, structural transforms, and cross-file compression strategies.

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

## Benchmarks

| Repo | Original size | Files | tgz compression | tgz size | metarc compression | metarc size | % size of tgz |
|------|---------------|-------|-----------------|----------|-------------------|-------------|----------------|
| kubernetes | 374M | 29254 | 0m17.685s | 96M | 0m2.902s | 97M | 90.6% |
| docker-compose | 4.5M | 706 | 0m0.374s | 1.2M | 0m0.086s | 1.1M | 97.7% |
| vuejs | 9.8M | 732 | 0m0.431s | 3.3M | 0m0.094s | 3.3M | 100.7% |
| numpy | 50M | 2371 | 0m1.645s | 19M | 0m0.369s | 19M | 98.2% |
| redis | 28M | 1784 | 0m1.065s | 9.7M | 0m0.239s | 9.0M | 99.5% |
| bootstrap | 27M | 820 | 0m0.729s | 15M | 0m0.158s | 14M | 93.6% |
| express | 1.6M | 242 | 0m0.129s | 356K | 0m0.028s | 356K | 100.4% |
| react | 65M | 6888 | 0m3.612s | 21M | 0m0.633s | 18M | 92.8% |

Results vary depending on dataset size and redundancy patterns.

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

---

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

* [`docs/metacompression.md`](docs/metacompression.md) — conceptual background
* [`docs/architecture.md`](docs/architecture.md) — format, pipeline, transforms

---

## License

MIT -- see [LICENSE](LICENSE).

