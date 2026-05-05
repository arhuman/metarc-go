# Metarc

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/arhuman/metarc-go)](https://goreportcard.com/report/github.com/arhuman/metarc-go)
[![Tests](https://github.com/arhuman/metarc-go/actions/workflows/test.yml/badge.svg)](https://github.com/arhuman/metarc-go/actions/workflows/test.yml)

**Compress structure before bytes.**

Metarc is an experimental archive format for source-code trees and structured file collections.
It exploits cross-file and semantic redundancy before applying `zstd`.

On my personal 6.5G source-code directory: **1.4G** with Metarc vs **1.8G** with `tar+zstd` (about **22% smaller**).

On well-known open-source repositories, current benchmarks show archives
**3–7% smaller than `tar+zstd`**, while archiving faster on the tested machine.

Not a tar replacement yet. A research-grade playground with reproducible benchmarks.

---

## Try it on your GOMODCACHE 

On real-world datasets like your Go module cache,
Metarc typically achieves modest but consistent gains.


```
# Check your GOMODCACHE size
du -sh $(go env GOMODCACHE)
```

```
# Clone the repo
git clone https://github.com/arhuman/metarc-go.git

# Install marc
cd metarc-go
make install

# Compress with tar+zstd
tar --zstd -cf /tmp/gomodcache.tar.zst -C $(go env GOMODCACHE) .

# Compress with Metarc 
marc archive /tmp/gomodcache.marc $(go env GOMODCACHE)
```

You can now check the results

```bash
ls -lh /tmp/gomodcache.*
perl -e 'printf "marc archive is %.2f%% smaller than tar archive\n", 100 * (1 - (-s "/tmp/gomodcache.marc") / (-s "/tmp/gomodcache.tar.zst"))'
```

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

## Benchmarks

More detailed benchmarks, as well as instructions to reproduce them, are available in [docs/benchmarks.md](docs/benchmarks.md)

### Compression

Metarc compression shines in directories with a lot of redundancy, where its file deduplication can outperform even `tar+zstd`:

```Bash
6.5G	code_perso
1.8G	code_perso.tar.zst
1.4G	code_perso.marc     (22% smaller)
```

But the goal is to make it at least "as good" in most common cases, that's why we mainly use popular open-source repositories (using various languages) to measure our progress in this area.

Previous comparisons used `tar+gzip`, we now use `tar+zstd` for a fairer comparison.

`./scripts/run_bench.sh --type size`

_marc: metarc version v0.8.0-5-g8045d64e-dirty (8045d64e, 2026-05-05T02:53:50Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _

| Repo | Original size | Files | tar+zstd size | marc size | % size of tar |
|------|---------------|-------|-------------------------|-----------|---------------|
| kubernetes | 376M | 29838 | 81.1M | 74.2M | 91.4% |
| docker-compose | 4.5M | 702 | 1.1M | 1.1M | 99.1% |
| vuejs | 9.9M | 728 | 3.2M | 3.2M | 97.5% |
| numpy |  50M | 2364 | 18.4M | 17.5M | 95.3% |
| redis |  29M | 1780 | 8.9M | 8.4M | 93.7% |
| bootstrap |  27M | 816 | 13.9M | 13.3M | 95.9% |
| express | 1.6M | 238 | 345.6K | 339.3K | 98.2% |
| react |  65M | 6884 | 18.5M | 17.1M | 92.4% |

> See [`docs/benchmarks.md`](docs/benchmarks.md) for the gz baseline, time benchmarks, methodology, and changelog.


### Speed

> [!NOTE]
> The latest version shows a visible compression improvement:
> New metacompression transforms and speed/compression tradeoffs (raising the zstd compression level) explain the results.
>
> Metarc is proving to be an efficient playground for exploring metacompression ideas, structural transforms, and cross-file compression strategies.

---

## Usage

### Install

```bash
git clone https://github.com/arhuman/metarc-go
cd metarc-go
make install
```
This installs `marc` to your `$GOBIN` (or `$GOPATH/bin`).

### Test

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

