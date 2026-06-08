# Metarc — Benchmarks

All benchmarks run against shallow clones of real-world open source repositories,
archived with `marc` vs `tar+zstd`, on the same machine.

> **Cold-cache timing requires sudo**
>
> `run_bench.sh` now uses cold-cache timing by default. To enforce this, it drops filesystem caches between runs, which requires `sudo` on Linux.
>
> If `sudo` is not available, run:
>
> ```bash
> ./scripts/run_bench.sh --type time --hot
> ```
>
> Hot-cache results are valid, but they measure a different scenario: data already cached in memory. They reduce I/O/cache effects and mostly reflect CPU, hashing, traversal, and compression behavior. Do not compare hot-cache timings directly with cold-cache timings.

## Performance

### Size

#### vs tar+zstd

`./scripts/run_bench.sh --type size`

_ marc: metarc version v0.10.0-15-g686841db-dirty (686841db, 2026-08-14T04:29:13Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _

| Repo | Original size | Files | tar+zstd size | marc size | % size of tar | previous |
|------|---------------|-------|-------------------------|-----------|---------------|----------|
| kubernetes | 376M | 29838 | 81.0M | 72.6M | 89.6% | 91.4% |
| docker-compose | 4.5M | 702 | 1.1M | 1.0M | 95.5% | 99.1% |
| vuejs | 9.9M | 728 | 3.2M | 3.1M | 95.9% | 97.5% |
| numpy |  49M | 2364 | 18.4M | 17.4M | 94.6% | 95.3% |
| redis |  28M | 1780 | 8.9M | 8.2M | 92.5% | 93.7% |
| bootstrap |  28M | 816 | 14.1M | 13.5M | 95.5% | 95.9% |
| express | 1.6M | 238 | 345.6K | 319.7K | 92.5% | 98.2% |
| react |  65M | 6884 | 18.4M | 16.8M | 91.1% | 92.4% |

> The `previous` column is the 2026-05-05 run (v0.8.0-5). Every corpus improved,
> after the solid-block geometry change and the catalog work of 2026-08-14.
>
> **These ratios understate the change, and they are not exactly reproducible.**
> The bench archives each clone *including its `.git`*. On kubernetes that is
> 48 MB of packfiles: 12% of the input bytes, but roughly 58% of the resulting
> archive, because packs are already deflate-compressed and pass through both
> tools untouched. Only 25 of the 29 838 files live in `.git`, so the file count
> hides it. The same tree with `.git` removed archives to 29.4 MB rather than
> 72.6 MB, and the measured gain there is about twice what this table shows:
> express -10.7%, docker-compose -9.9%, kubernetes -5.2%, react -4.9% in
> absolute archive bytes against the pre-2026-08-14 binary.
>
> The pinned commit fixes the tree but not the packfile: `git fetch --depth 1`
> does not repack byte-identically between runs, so the tar baseline itself
> drifts (bootstrap 13.9M to 14.1M, react 18.5M to 18.4M across two runs of the
> same commit). Treat sub-0.5 pp differences in this table as noise.

#### vs tar+gz

 `./scripts/run_bench.sh --type size --compression gz`

_ marc: metarc version v0.10.0-15-g686841db-dirty (686841db, 2026-08-14T04:29:13Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _

| Repo | Original size | Files | tar+gz size | marc size | % size of tar | previous |
|------|---------------|-------|-------------------------|-----------|---------------|----------|
| kubernetes | 376M | 29838 | 90.0M | 72.6M | 80.6% | 82.4% |
| docker-compose | 4.5M | 702 | 1.2M | 1.0M | 90.9% | 94.4% |
| vuejs | 9.9M | 728 | 3.3M | 3.1M | 95.4% | 96.9% |
| numpy |  50M | 2364 | 18.9M | 17.4M | 92.1% | 92.7% |
| redis |  28M | 1780 | 9.0M | 8.2M | 91.4% | 92.7% |
| bootstrap |  28M | 816 | 14.9M | 13.5M | 90.2% | 90.3% |
| express | 1.6M | 238 | 354.0K | 319.7K | 90.3% | 95.8% |
| react |  66M | 6884 | 19.8M | 16.8M | 84.5% | 86.3% |

> Against tar+gz, marc shines on large, Go-heavy or mixed-language repos.
> Against tar+zstd, Metarc is now better on all repos.

### Time

#### vs tar+zstd

 `./scripts/run_bench.sh --type time`

_ marc: metarc version v0.8.0-5-g8045d64e-dirty (8045d64e, 2026-05-05T02:53:50Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _
_ host: 26.3.1, Apple M3 Pro, 12 cores, 36G | timing: median of 3 runs, cache flushed before each run (cold) _

| Repo | Files | tar+zstd arc | marc arc | tar+zstd ext | marc ext | marc speedup (arc) |
|------|-------|------------------------|----------|-----------------------|----------|--------------------|
| kubernetes | 29838 | 0m18.748s | 0m12.464s | 0m16.159s | 0m6.277s | 1.5× faster |
| docker-compose | 702 | 0m0.716s | 0m0.256s | 0m0.556s | 0m0.180s | 2.8× faster |
| vuejs | 728 | 0m0.826s | 0m0.370s | 0m0.605s | 0m0.174s | 2.2× faster |
| numpy | 2364 | 0m1.727s | 0m1.594s | 0m1.306s | 0m0.490s | 1.1× faster |
| redis | 1780 | 0m1.375s | 0m0.941s | 0m1.034s | 0m0.339s | 1.5× faster |
| bootstrap | 816 | 0m0.842s | 0m0.570s | 0m0.585s | 0m0.207s | 1.5× faster |
| express | 238 | 0m0.491s | 0m0.179s | 0m0.254s | 0m0.095s | 2.7× faster |
| react | 6884 | 0m4.636s | 0m2.268s | 0m3.658s | 0m1.201s | 2× faster |


#### vs tar+gz

`./scripts/run_bench.sh --type time --compression gz`

_ marc: metarc version v0.8.0-5-g8045d64e-dirty (8045d64e, 2026-05-05T02:53:50Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _
_ host: 26.3.1, Apple M3 Pro, 12 cores, 36G | timing: median of 3 runs, cache flushed before each run (cold) _

| Repo | Files | tar+gz arc | marc arc | tar+gz ext | marc ext | marc speedup (arc) |
|------|-------|------------------------|----------|-----------------------|----------|--------------------|
| kubernetes | 29838 | 0m23.998s | 0m11.476s | 0m15.937s | 0m6.579s | 2.1× faster |
| docker-compose | 702 | 0m0.749s | 0m0.255s | 0m0.536s | 0m0.176s | 2.9× faster |
| vuejs | 728 | 0m0.890s | 0m0.340s | 0m0.540s | 0m0.189s | 2.6× faster |
| numpy | 2364 | 0m2.724s | 0m1.453s | 0m1.323s | 0m0.495s | 1.9× faster |
| redis | 1780 | 0m1.884s | 0m1.143s | 0m1.007s | 0m0.352s | 1.6× faster |
| bootstrap | 816 | 0m1.304s | 0m0.449s | 0m0.593s | 0m0.226s | 2.9× faster |
| express | 238 | 0m0.415s | 0m0.146s | 0m0.317s | 0m0.088s | 2.8× faster |
| react | 6884 | 0m5.349s | 0m1.887s | 0m3.271s | 0m1.274s | 2.8× faster |

> marc is still faster that tar+zstd and tar+gz, due to parallel BLAKE3 hashing and lightweight transforms 
> BUT current version traded some speed for better compression (zstd compression level) and some transforms
> were added that slow down even further.

---

## Usage

Reproduce these results with the benchmark scripts in `scripts/`.

### Size table

```sh
./scripts/run_bench.sh --type size
```

Outputs a markdown table with original size, tar size, marc size, and ratio columns.

### Time table

```sh
./scripts/run_bench.sh --type time
```

Outputs a markdown table with archive and extract timing for both tar and marc.

### Full table (legacy)

```sh
./scripts/run_bench.sh --type legacy
```

Outputs all columns combined (default if `--type` is omitted).

### Options

| Flag | Values | Default | Description |
|------|--------|---------|-------------|
| `--type` | `size`, `time`, `legacy` | `legacy` | Selects output columns |
| `--compression` | `zstd`, `gz` | `zstd` | Final compressor for tar baseline |
| `--hot` | (flag) | off (cold by default) | Use warm-cache methodology: prime the page cache before each timed run + 1 warmup. See *Choosing cold vs hot* below. |

### Choosing cold vs hot

The bench script measures **wall-clock**, and wall-clock depends heavily on whether the input files are in the OS page cache. The two modes answer different questions; pick the one that matches your goal.

| | Cold (default) | Hot (`--hot`) |
|---|---|---|
| What it measures | Realistic I/O-bound wall-clock: how long the tool actually takes when files have to come off disk. | CPU-bound encoding speed: how fast the tool's pipeline runs when reads come from RAM. |
| Variance | Higher (real disk I/O is noisy). | < 3% (priming + warmup hold the OS state constant). |
| Methodology | No warmup. Median of 3 timed runs, with `purge` (macOS) or `sync && drop_caches` (Linux) before each. | 1 untimed warmup, then median of 3 timed runs with `prime_cache` before each. |
| Needs sudo | **Yes** — script primes the credential once at startup; falls back to warm with one warning if unavailable. | No. |
| Use when | reporting numbers users will actually experience; reproducing pre-2026-05-03 historical numbers; running unattended in CI for honest wall-clock. | tracking small regressions during development; comparing CPU efficiency between tool versions. |

#### Why cold is the default

`scripts/run_bench.sh` and `scripts/compare_on_repo.sh` briefly defaulted to warm cache (added 2026-05-03 to kill the ~30% variance of the old single-run methodology). That worked for variance, but had an unintended side effect: warm-cache numbers systematically favour I/O-bound tools (tar+zstd reading from RAM) over CPU-bound tools (marc, gated by parallel BLAKE3 regardless of cache state). On kubernetes, warm tar+zstd landed at ~2 s while cold tar+zstd took ~14 s — a 7× spread that made every "marc vs tar+zstd" claim depend on which question you'd asked.

The default flipped to cold the same day because:

1. **It matches what users actually experience.** Archiving a fresh checkout, a CI artifact, a server's logs — none of those have the input files pre-warmed in RAM. The cold number is the user-facing number.
2. **It matches the historical baseline.** Tables earlier in this file were captured with effectively cold-cache methodology. Cold default = honest before/after comparisons.
3. **It avoids accidentally publishing warm numbers as if they were realistic.** Anyone copying numbers from the bench into a README or release notes now gets the right number by default.

`--hot` is the opt-in for development work where stable variance matters more than realism — e.g. "did this commit change marc's CPU pipeline by more than 1%?" Cold-cache I/O noise would drown that signal; warm-cache surfaces it cleanly.

### Single repo

Use `compare_on_repo.sh` directly to benchmark one repository:

```sh
./scripts/compare_on_repo.sh \
  --name react \
  --repo https://github.com/facebook/react \
  --type size
```

Append `--mode log` to see progress output, or `--mode test` to verify round-trip integrity only.

---

## Changelog

2026-08-14 (later): **Catalog diet.**
Write-only indexes (`blobs.sha`, `names.name`, `entries.parent_id`) are dropped
before the catalog is serialized, `entry_blobs` became `WITHOUT ROWID`,
`blobs.sha` stores a 16-byte prefix, and the catalog is compressed at level 11.
See ADR-018. The kubernetes catalog went from 2.2 MB (7.4% of the archive) to
1.1 MB (4.0%); small corpora gain most because the catalog is a larger share of
a small archive. With `.git` removed: express -10.7%, docker-compose -9.9%,
kubernetes -5.2%, react -4.9%, loghub -0.4% against the pre-2026-08-14 binary.
Both size tables above cover this and the geometry change below.

2026-08-14: **Solid block geometry: window follows the block, 16 MiB → 32 MiB, small extensions pool.**
The size tables above were re-run for this change. The **time tables were not**:
they need cold-cache mode, which needs `sudo` for the page-cache flush.

Three changes (see ADR-017): the solid encoder's match window now defaults to the
block size (it was stuck at klauspost's 8 MiB per-level default, so the second
half of every 16 MiB block was unreachable); an extension change no longer
flushes a block holding less than 1 MiB, so rare extensions pool instead of each
paying for an undersized frame; and the default block size moves to 32 MiB.

Measured on corpora with `.git` removed, since git pack files are incompressible
and dilute every percentage. Deltas are against the previous default:

| corpus | delta |
|---|---|
| express | -2.61% |
| docker-compose | -2.43% |
| react | -0.79% |
| kubernetes | -1.65% |
| loghub | +0.003% |

The express and docker-compose figures recover the regression the 16 MiB bump
introduced below. Peak RSS on kubernetes rises to ~361 MiB archiving and
~349 MiB extracting (from 258/270 MiB). 64 MiB blocks were measured too
(-2.58% on kubernetes, no gain elsewhere, +148 MiB RSS) and are available via
`--solid-block-size`.

2026-05-03 (Item 3): **Solid blocks: 4 MiB → 16 MiB, with extension-aligned flush.**
The default solid block size moves from 4 MiB to 16 MiB and the accumulator now
flushes on every file-extension change (the archive pipeline already sorts by
extension, so each block ends up extension-pure). Larger, single-language blocks
keep more cross-file context inside one zstd frame.

Effect on the size table vs the previous `--type size` run (zstd baseline):
- kubernetes: 92.8% → 91.4%  (−1.4 pp; the biggest absolute win)
- react:      93.6% → 92.4%  (−1.2 pp)
- numpy:      96.0% → 95.3%  (−0.7 pp)
- bootstrap:  96.6% → 96.0%  (−0.6 pp)
- redis:      94.3% → 93.7%  (−0.6 pp)
- vuejs:      97.0% → 97.4%  (+0.4 pp)
- express:    96.2% → 98.2%  (+2.0 pp)
- docker-compose: 96.5% → 99.1%  (+2.6 pp)

Where it pays off: corpora with enough material per extension to fill a 16 MiB
block (kubernetes, react, numpy). Where it regresses: tiny corpora (express,
docker-compose) where total bytes per extension are far below the new block
threshold, so the bigger block doesn't add context but the per-block framing
overhead is a larger fraction of the archive. Item 4 (per-extension dicts) is
the planned fix for the small-corpus regression.

The gz size and time tables are pending a re-run; only the zstd tables above
were refreshed.

2026-05-03 (later): **Cold cache is now the default; `--hot` opts into warm methodology.**
The earlier 2026-05-03 change (warm-cache methodology) had an unintended side effect: it systematically favoured I/O-bound tools (tar+zstd reading from RAM) over CPU-bound tools (marc), making "marc vs tar+zstd" comparisons misleading for users who archive cold files. The default is now back to cold cache (matches the methodology of the pre-2026-05-03 historical tables, so before/after comparisons are honest). Pass `--hot` to use warm-cache low-variance methodology when tracking small CPU-pipeline regressions during development.
**Note: Cold cache require `sudo` to flush the disk cache.**

2026-05-03: **Bench methodology changed — old timing tables are not directly comparable.**
`scripts/run_bench.sh` and `scripts/compare_on_repo.sh` now report the **median of 3 timed runs** with cache priming + 1 warmup. Run-to-run variance dropped from ~30% under the old single-run methodology to under 3%. (Note: this was reverted to a cold-cache default the same day — see the entry above. The median-of-3 + warmup machinery is still used in `--hot` mode.)

2026-04-23:  **Last updated** 
Pin repository used in tests to a specific commit for reproducible results.
(Means that comparing to previous results is meaningful)
metarc version v0.6.0-6-g41aa53a-dirty (41aa53a, 2026-04-24T15:22:51Z)
Transforms:
  dedup/v1                  enabled
  go-line-subst/v1          enabled
  license-canonical/v1      enabled
  near-dup-delta/v1         stub

2026-04-22: 
With `go-line-subst/v1` transform enabled

---
