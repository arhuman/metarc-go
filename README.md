# Metarc

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/arhuman/metarc-go)](https://goreportcard.com/report/github.com/arhuman/metarc-go)
[![Tests](https://github.com/arhuman/metarc-go/actions/workflows/test.yml/badge.svg)](https://github.com/arhuman/metarc-go/actions/workflows/test.yml)

**Compress structure before bytes.**

Metacompression is another level of compression.

Metarc archives repositories much faster than `tar.gz` while staying competitive on size, by applying **structural and semantic transforms before compression**.

Instead of only compressing bytes, Metarc reduces **redundancy in meaning**:
licenses, JSON structure, logs, duplicated content, and repeated patterns across files.

> New here? Start with [`docs/metacompression.md`](docs/metacompression.md).

---

## Why this exists

Traditional tools solve different parts of the problem:

- `tar` → packs files  
- `gzip` / `zstd` → compress bytes  
- dedup tools → remove identical blobs  

**Metarc sits in between.**

It targets cross-file redundancy in real-world text-heavy data:

- source code repositories  
- configs  
- logs  
- structured datasets  

> **Metarc compresses structure before it compresses bytes.**

---

## Early benchmark

On real repositories, Metarc is already **much faster than `tar.gz`** while producing similarly sized archives — and sometimes smaller ones.

| Repo | Original | Files | tgz time | tgz size | marc time | marc size |
|------|----------|-------|----------|----------|-----------|-----------|
| kubernetes | 374M | 29254 | 17.7s | 96M | **2.9s** | 97M |
| docker-compose | 4.5M | 706 | 0.37s | 1.2M | **0.086s** | 1.1M |
| vuejs | 9.8M | 732 | 0.43s | 3.3M | **0.094s** | 3.3M |
| numpy | 50M | 2371 | 1.64s | 19M | **0.37s** | 19M |
| redis | 28M | 1784 | 1.06s | 9.7M | **0.24s** | 9.0M |
| bootstrap | 27M | 820 | 0.72s | 15M | **0.16s** | 14M |
| express | 1.6M | 242 | 0.13s | 356K | **0.028s** | 356K |
| react | 65M | 6888 | 3.61s | 21M | **0.63s** | 18M |

(tar version used: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8)

**Takeaway:**
- much faster than `tar.gz`
- often similar size
- sometimes smaller

You can reproduce this table with `scripts/run_bench.sh`, or test any repository with `scripts/compare_on_repo.sh`:

```bash
# Reproduce the full benchmark table
./scripts/run_bench.sh

# Benchmark a single repo
./scripts/compare_on_repo.sh --name django --repo https://github.com/django/django

# Verify round-trip integrity only (exit code 0 = success)
./scripts/compare_on_repo.sh --name django --repo https://github.com/django/django --mode test

# Show progress/log output
./scripts/compare_on_repo.sh --name django --repo https://github.com/django/django --mode log
```

---

## What makes Metarc different

Metarc is not just a byte-stream compressor.

It can **rewrite data into a lower-entropy representation** before handing it to the final compressor.

Current transforms include:

- content deduplication  
- license canonicalization  
- JSON canonicalization  
- log template extraction  

Each transform is:
- versioned  
- reversible at the format level  
- applied only when it appears worth it  

---

## Built for experimentation

Metarc is designed as a **codebase for experimenting with meta-compression**.

You can:

- implement new transforms in code  
- tweak heuristics and trade-offs  
- explore cross-file compression strategies  
- test ideas on real datasets  

This is not a plugin system.  
It is a compact architecture for iterating on compression ideas quickly.
(Only 2000 lines of Go currently)

---

## Archive format

A `.marc` archive is a self-contained binary:

- blob region (data)
- compressed SQLite catalog (metadata)
- footer for fast lookup
- content-addressable deduplication
- optional solid block compression

For full details, see [`docs/architecture.md`](docs/architecture.md).

---

## Usage

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

## Why this is interesting

Metarc is useful in two ways:

1. **a fast, practical archive format**
2. **a real platform for testing semantic compression ideas**

The second part is the real ambition.

> What if an archiver understood what it was archiving?

---

## Status

Metarc is **experimental, but already usable and useful**.

The current implementation validates the core idea:
semantic preprocessing + standard compression can already produce strong results.

---

## Documentation

* [`docs/metacompression.md`](docs/metacompression.md) — conceptual background
* [`docs/architecture.md`](docs/architecture.md) — format, pipeline, transforms

---

## Build

```bash
make build
```

## Install

```bash
make install
```

This installs `marc` to your `$GOBIN` (or `$GOPATH/bin`).

## Test

```bash
make test
```

---

## License

MIT -- see [LICENSE](LICENSE).

---
