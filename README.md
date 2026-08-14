# Metarc

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/arhuman/metarc-go)](https://goreportcard.com/report/github.com/arhuman/metarc-go)
[![Tests](https://github.com/arhuman/metarc-go/actions/workflows/test.yml/badge.svg)](https://github.com/arhuman/metarc-go/actions/workflows/test.yml)

**Compress structure before bytes.**

Metarc is an experimental archive format for source-code trees and structured file collections.
It exploits cross-file and semantic redundancy before applying `zstd`.

On my personal 6.5G source-code directory: **1.4G** with Metarc vs **1.8G** with `tar+zstd` (about **22% smaller**).

On well-known open-source repositories, current benchmarks show source trees
**4% to 19% smaller than `tar+zstd`**, measured on `git archive` exports of
pinned commits: the only form that reproduces byte for byte. Archiving a full
clone gains less, from roughly parity to about 8%, because `.git` packfiles are
already compressed and neither tool can do anything with them.

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

Each repository is measured in two forms, because one number cannot answer both
questions worth asking:

* **full clone**, `.git` included: what a user actually gets when archiving a working directory.
* **source tree only**, exported with `git archive` at the pinned commit: what the compressor actually did.

The gap between the two is the weight of the packfiles, which are already
deflate-compressed and pass through both tools untouched. On kubernetes `.git`
is 12% of the input bytes but roughly 58% of the resulting archive.

#### Full clone (.git included)

`./scripts/run_bench.sh --type size`

_marc: metarc version v0.11.1 (c87305c1, 2026-08-14T11:49:32Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _

| Repo | Original size | Files | tar+zstd size | marc size | % size of tar |
|------|---------------|-------|-------------------------|-----------|---------------|
| kubernetes | 375M | 29838 | 79.2M | 72.6M | 91.7% |
| docker-compose | 4.5M | 702 | 1.0M | 1.0M | 100.8% |
| vuejs | 9.4M | 728 | 3.2M | 3.1M | 97.3% |
| numpy |  49M | 2364 | 18.3M | 17.4M | 95.4% |
| redis |  28M | 1780 | 8.8M | 8.2M | 93.8% |
| bootstrap |  27M | 816 | 13.8M | 13.5M | 97.8% |
| express | 1.6M | 238 | 329.8K | 319.7K | 96.9% |
| react |  65M | 6884 | 17.9M | 16.8M | 93.5% |

Treat this table as indicative, not reproducible. A shallow clone does not
repack byte-identically between fetches, so the tar baseline drifts on its own:
the previous run of these same pinned commits measured kubernetes at 81.0M and
express at 345.6K against the 79.2M and 329.8K above, while marc's output was
identical to the byte in both runs. docker-compose crossing 100% comes from that
baseline side too: marc wrote the same bytes, tar's shrank.

#### Source tree only (no .git)

`./scripts/run_bench.sh --type size --corpus tree`

_marc: metarc version v0.11.1 (c87305c1, 2026-08-14T11:49:32Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _

| Repo | Original size | Files | tar+zstd size | marc size | % size of tar | full clone |
|------|---------------|-------|-------------------------|-----------|---------------|------------|
| kubernetes | 327M | 29813 | 34.6M | 28.0M | **81.1%** | 91.7% |
| react |  54M | 6859 | 8.2M | 6.8M | **83.3%** | 93.5% |
| redis |  23M | 1755 | 4.2M | 3.6M | **86.1%** | 93.8% |
| numpy |  40M | 2339 | 8.9M | 8.0M | **89.7%** | 95.4% |
| express | 1.3M | 213 | 135.8K | 123.1K | **90.6%** | 96.9% |
| docker-compose | 3.7M | 677 | 421.8K | 387.8K | **91.9%** | 100.8% |
| vuejs | 7.6M | 703 | 1.5M | 1.4M | **94.3%** | 97.3% |
| bootstrap |  20M | 791 | 6.6M | 6.3M | **95.8%** | 97.8% |

Rows sorted by advantage. This table is byte-reproducible: two builds hours
apart (v0.10.0-15 and v0.11.1) produced these figures identically. bootstrap gains the
least from dropping `.git` (2.0 points) because its source tree is itself full of
incompressible content (fonts, images, minified bundles), so removing the
packfiles unmasks little. Corpora rank by how much compressible text they hold.

> See [`docs/benchmarks.md`](docs/benchmarks.md) for the gz baseline, time benchmarks, methodology, reproducibility notes, and changelog.


### Speed

Metarc optimizes for size, not for archive wall-clock.

`./scripts/run_bench.sh --type time`

_marc: metarc version v0.11.1 (c87305c1, 2026-08-14T11:49:32Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _
_host: 26.5.2, Apple M3 Pro, 12 cores, 36G | timing: median of 3 runs, cache flushed before each run (cold) _

| Repo | Files | tar+zstd arc | marc arc | tar+zstd ext | marc ext | marc speedup (arc) |
|------|-------|------------------------|----------|-----------------------|----------|--------------------|
| kubernetes | 29838 | 0m5.262s | 0m8.818s | 0m3.436s | 0m4.436s | 1.7× slower |
| docker-compose | 702 | 0m0.309s | 0m0.186s | 0m0.261s | 0m0.160s | 1.7× faster |
| vuejs | 728 | 0m0.376s | 0m0.278s | 0m0.252s | 0m0.147s | 1.4× faster |
| numpy | 2364 | 0m0.870s | 0m1.377s | 0m0.417s | 0m0.425s | 1.6× slower |
| redis | 1780 | 0m0.704s | 0m0.733s | 0m0.402s | 0m0.264s | 1× slower |
| bootstrap | 816 | 0m0.387s | 0m0.416s | 0m0.252s | 0m0.207s | 1.1× slower |
| express | 238 | 0m0.129s | 0m0.121s | 0m0.216s | 0m0.084s | 1.1× faster |
| react | 6884 | 0m1.627s | 0m1.771s | 0m0.969s | 0m0.955s | 1.1× slower |

Archiving is slower on the four largest corpora and faster on the three
smallest, with redis at parity. Extraction is faster on six out of eight.

Against `tar+gz` the same binary is faster on **all eight** (1.1× to 2.9×, table
in [`docs/benchmarks.md`](docs/benchmarks.md)). marc's own times are identical in
both comparisons; only the baseline changes. It sits between gzip and zstd in
CPU cost while producing smaller archives than either.

> [!WARNING]
> **The first heavy run of a session reads high.** Measuring kubernetes five
> times gave 13.286s, 10.279s, 9.175s, 9.275s, 8.818s for the same binary on the
> same corpus: the first pass overstated by 45%. tar was stable throughout
> (5.26s to 5.68s), so the effect is on marc's side and cold mode runs no warmup.
> Treat any single cold row as ±15%, and discard the first.

That marc is slower at all is a deliberate default, not a regression: solid
blocks are compressed at zstd level 11. On react, measured in two alternating
passes so neither ordering nor warmup can explain the gap:

| `--zstd-level-solid` | marc archive | marc size | % of tar+zstd |
|---|---|---|---|
| 3 | 0m0.95s | 17.9M | 99.7% |
| 11 (default) | 0m1.52s | 16.8M | **93.5%** |

Level 3 archives faster than `tar+zstd` and compresses like it; level 11 costs
1.6× the time and buys 6.2 points of ratio. Both sizes reproduced exactly across
the two passes. `--zstd-level-solid 7` sits between the two, keeping about half
the ratio gain for roughly half the CPU.

> [!NOTE]
> Earlier releases claimed an archive-speed win, and the `v0.8.0` tables in
> [`docs/benchmarks.md`](docs/benchmarks.md) still show it. Two things changed:
> the solid level moved from 3 to 11, and the test machine's cold-read
> throughput roughly tripled, which removed the I/O floor that had favoured a
> parallel reader over `tar`. marc's own timings barely moved between the two
> runs; the baseline did.

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

