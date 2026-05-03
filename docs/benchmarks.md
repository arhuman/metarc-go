# Metarc — Benchmarks

All benchmarks run against shallow clones of real-world open source repositories,
archived with `marc` vs `tar+zstd`, on the same machine.

## Performance

### Size

#### vs tar+zstd

`./scripts/run_bench.sh --type size`

_marc: metarc version v0.7.0-14-gff9a306-dirty (ff9a306, 2026-05-03T10:34:58Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _

| Repo | Original size | Files | tar+zstd size | marc size | % size of tar |
|------|---------------|-------|-------------------------|-----------|---------------|
| kubernetes | 376M | 29838 | 81.1M | 75.3M | 92.8% |
| docker-compose | 4.5M | 702 | 1.1M | 1.1M | 96.5% |
| vuejs | 9.9M | 728 | 3.3M | 3.2M | 97.0% |
| numpy |  50M | 2364 | 18.4M | 17.7M | 96.0% |
| redis |  28M | 1780 | 8.9M | 8.4M | 94.3% |
| bootstrap |  27M | 816 | 13.9M | 13.4M | 96.6% |
| express | 1.6M | 238 | 345.7K | 332.5K | 96.2% |
| react |  65M | 6884 | 18.5M | 17.3M | 93.6% |


#### vs tar+gz

 `./scripts/run_bench.sh --type size --compression gz`

_marc: metarc version v0.7.0-14-gff9a306-dirty (ff9a306, 2026-05-03T10:34:58Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _

| Repo | Original size | Files | tar+gz size | marc size | % size of tar |
|------|---------------|-------|-------------------------|-----------|---------------|
| kubernetes | 376M | 29838 | 90.0M | 75.3M | 83.6% |
| docker-compose | 4.5M | 702 | 1.2M | 1.1M | 91.9% |
| vuejs | 9.8M | 728 | 3.3M | 3.2M | 96.5% |
| numpy |  50M | 2364 | 18.9M | 17.7M | 93.4% |
| redis |  28M | 1780 | 9.0M | 8.4M | 92.9% |
| bootstrap |  27M | 816 | 14.7M | 13.4M | 91.0% |
| express | 1.6M | 238 | 354.0K | 332.5K | 93.9% |
| react |  65M | 6884 | 19.8M | 17.3M | 87.4% |

> Against tar+gz, marc shines on large, Go-heavy or mixed-language repos.
> Against tar+zstd, Metarc is now (sometimes slightly) better on all repos.

### Time

#### vs tar+zstd

 `./scripts/run_bench.sh --type time`

_marc: metarc version v0.7.0-14-gff9a306-dirty (ff9a306, 2026-05-03T10:34:58Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _
_host: 26.3.1, Apple M3 Pro, 12 cores, 36G | timing: median of 3 runs, cache flushed before each run (cold)_

| Repo | Files | tar+zstd arc | marc arc | tar+zstd ext | marc ext | marc speedup (arc) |
|------|-------|------------------------|----------|-----------------------|----------|--------------------|
| kubernetes | 29838 | 0m14.320s | 0m7.881s | 0m11.819s | 0m4.935s | 1.8× faster |
| docker-compose | 702 | 0m0.611s | 0m0.157s | 0m0.494s | 0m0.147s | 3.9× faster |
| vuejs | 728 | 0m0.573s | 0m0.228s | 0m0.472s | 0m0.136s | 2.5× faster |
| numpy | 2364 | 0m1.408s | 0m0.908s | 0m0.956s | 0m0.369s | 1.6× faster |
| redis | 1780 | 0m1.098s | 0m0.609s | 0m0.770s | 0m0.279s | 1.8× faster |
| bootstrap | 816 | 0m0.639s | 0m0.331s | 0m0.503s | 0m0.159s | 1.9× faster |
| express | 238 | 0m0.352s | 0m0.087s | 0m0.325s | 0m0.068s | 4× faster |
| react | 6884 | 0m3.466s | 0m1.339s | 0m2.481s | 0m0.948s | 2.6× faster |

#### vs tar+gz

`./scripts/run_bench.sh --type time --compression gz`

_marc: metarc version v0.7.0-14-gff9a306-dirty (ff9a306, 2026-05-03T10:34:58Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _
_host: 26.3.1, Apple M3 Pro, 12 cores, 36G | timing: median of 3 runs, cache flushed before each run (cold)_

| Repo | Files | tar+gz arc | marc arc | tar+gz ext | marc ext | marc speedup (arc) |
|------|-------|------------------------|----------|-----------------------|----------|--------------------|
| kubernetes | 29838 | 0m18.052s | 0m7.481s | 0m12.192s | 0m4.824s | 2.4× faster |
| docker-compose | 702 | 0m0.594s | 0m0.164s | 0m0.415s | 0m0.130s | 3.6× faster |
| vuejs | 728 | 0m0.698s | 0m0.227s | 0m0.442s | 0m0.127s | 3.1× faster |
| numpy | 2364 | 0m1.967s | 0m0.929s | 0m1.001s | 0m0.351s | 2.1× faster |
| redis | 1780 | 0m1.399s | 0m0.592s | 0m0.723s | 0m0.260s | 2.4× faster |
| bootstrap | 816 | 0m1.004s | 0m0.316s | 0m0.498s | 0m0.155s | 3.2× faster |
| express | 238 | 0m0.328s | 0m0.076s | 0m0.285s | 0m0.067s | 4.3× faster |
| react | 6884 | 0m4.066s | 0m1.379s | 0m2.456s | 0m0.880s | 2.9× faster |


> marc is still faster that tar+zstd and tar+gz, due to parallel BLAKE3 hashing and lightweight transforms 
> BUT current version traded some speed for better compression (zstd compression level)

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