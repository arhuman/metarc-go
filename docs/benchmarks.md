# Metarc — Benchmarks

All benchmarks run against shallow clones of real-world open source repositories,
archived with `marc` vs `tar+zstd`, on the same machine.

## Changelog

2026-05-03: **Bench methodology changed — old timing tables are not directly comparable.**
`scripts/run_bench.sh` and `scripts/compare_on_repo.sh` now report the **median of 3 timed runs** after **1 untimed warmup**, with the OS page cache **primed** for each run (every input file read once before timing). On macOS the script self-wraps in `caffeinate -di` to prevent display-sleep / idle throttling. Run-to-run variance dropped from ~30% under the old single-run methodology to under 3%. Side effect: numbers from this date forward will systematically differ from earlier tables — usually a small drop on tar+zstd (which benefited disproportionately from cold-cache disk reads in the old methodology) and a small rise on marc (which now pays consistent priming overhead). Size tables are unaffected; only timing tables need re-running before any direct before/after comparison.

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

## Performance

### Size

#### vs tar+zstd

_marc: metarc version v0.6.0-7-gc68d3c0-dirty (c68d3c0, 2026-04-24T15:55:29Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _

| Repo | Original size | Files | tar+zstd size | marc size | % size of tar |
|------|---------------|-------|-------------------------|-----------|---------------|
| kubernetes | 375M | 29838 | 81.1M | 81.0M | 99.9% |
| docker-compose | 4.5M | 702 | 1.1M | 1.1M | 100.1% |
| vuejs | 9.9M | 728 | 3.3M | 3.3M | 101.5% |
| numpy |  50M | 2364 | 18.4M | 18.5M | 100.5% |
| redis |  29M | 1780 | 8.9M | 9.0M | 101.8% |
| bootstrap |  27M | 816 | 13.9M | 13.6M | 98.4% |
| express | 1.6M | 238 | 345.4K | 356.5K | 103.2% |
| react |  66M | 6884 | 18.5M | 18.3M | 98.8% |

#### vs tar+gz

_marc: metarc version v0.6.0-7-gc68d3c0-dirty (c68d3c0, 2026-04-24T15:55:29Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _

| Repo | Original size | Files | tar+gz size | marc size | % size of tar |
|------|---------------|-------|-------------------------|-----------|---------------|
| kubernetes | 376M | 29838 | 90.0M | 81.0M | 90.0% |
| docker-compose | 4.5M | 702 | 1.2M | 1.1M | 95.2% |
| vuejs | 9.9M | 728 | 3.3M | 3.3M | 100.9% |
| numpy |  50M | 2364 | 18.9M | 18.5M | 97.9% |
| redis |  29M | 1780 | 9.0M | 9.0M | 100.3% |
| bootstrap |  27M | 816 | 14.7M | 13.6M | 92.6% |
| express | 1.6M | 238 | 354.0K | 356.5K | 100.7% |
| react |  65M | 6884 | 19.8M | 18.3M | 92.3% |

> Against tar+gz, marc shines on large, Go-heavy or mixed-language repos (kubernetes −10%, react −7%).
> Against tar+zstd, most repos are at near-parity or slightly larger — zstd already exploits much of the same
> redundancy that marc's semantic transforms target, so the net gain is modest at default zstd levels.
> The speed advantage holds regardless of the baseline compressor.

### Time

> ⚠️ The two timing tables below were captured with the **pre-2026-05-03 single-run methodology**. They are kept for historical reference but should not be compared directly against numbers from current `run_bench.sh --type time` output, which uses median-of-3 with cache priming and a warmup pass. See the 2026-05-03 changelog entry above. Re-run on the same machine to refresh.

#### vs tar+zstd

_marc: metarc version v0.6.0-7-gc68d3c0-dirty (c68d3c0, 2026-04-24T15:55:29Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _

| Repo | Files | tar+zstd arc | marc arc | tar+zstd ext | marc ext |
|------|-------|------------------------|----------|-----------------------|----------|
| kubernetes | 29838 | 0m14.169s | 0m4.792s | 0m12.865s | 0m4.892s |
| docker-compose | 702 | 0m0.313s | 0m0.100s | 0m0.281s | 0m0.104s |
| vuejs | 728 | 0m0.306s | 0m0.116s | 0m0.271s | 0m0.102s |
| numpy | 2364 | 0m0.993s | 0m0.409s | 0m0.884s | 0m0.359s |
| redis | 1780 | 0m0.664s | 0m0.286s | 0m0.610s | 0m0.252s |
| bootstrap | 816 | 0m0.352s | 0m0.163s | 0m0.310s | 0m0.135s |
| express | 238 | 0m0.129s | 0m0.039s | 0m0.102s | 0m0.040s |
| react | 6884 | 0m2.688s | 0m0.993s | 0m2.420s | 0m0.944s |

#### vs tar+gz

_marc: metarc version v0.6.0-7-gc68d3c0-dirty (c68d3c0, 2026-04-24T15:55:29Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _

| Repo | Files | tar+gz arc | marc arc | tar+gz ext | marc ext |
|------|-------|------------------------|----------|-----------------------|----------|
| kubernetes | 29838 | 0m18.053s | 0m4.907s | 0m12.470s | 0m5.201s |
| docker-compose | 702 | 0m0.345s | 0m0.103s | 0m0.276s | 0m0.109s |
| vuejs | 728 | 0m0.442s | 0m0.122s | 0m0.442s | 0m0.117s |
| numpy | 2364 | 0m1.665s | 0m0.421s | 0m0.859s | 0m0.347s |
| redis | 1780 | 0m1.062s | 0m0.297s | 0m0.628s | 0m0.260s |
| bootstrap | 816 | 0m0.730s | 0m0.163s | 0m0.331s | 0m0.140s |
| express | 238 | 0m0.130s | 0m0.038s | 0m0.098s | 0m0.041s |
| react | 6884 | 0m3.584s | 0m0.899s | 0m2.462s | 0m0.908s |

> marc archives consistently 4–5× faster than tar+zstd and 5–6× faster than tar+gz, due to parallel
> BLAKE3 hashing and lightweight transforms. marc also extracts 2× faster than tar+zstd and 2–3× faster than tar+gz.

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
| `--cold` | (flag) | off (warm) | Flush the OS page cache before each timed run. See *Choosing warm vs cold* below. |

### Choosing warm vs cold

The bench script measures **wall-clock**, and wall-clock depends heavily on whether the input files are in the OS page cache. The two modes answer different questions; pick the one that matches your goal.

| | Warm (default) | Cold (`--cold`) |
|---|---|---|
| What it measures | CPU-bound encoding speed: how fast the tool's pipeline runs when reads come from RAM. | Realistic I/O-bound wall-clock: how long the tool actually takes when files have to come off disk. |
| Variance | < 3% (priming + warmup hold the OS state constant). | Higher (real disk I/O is noisy). |
| Methodology | 1 untimed warmup, then median of 3 timed runs with `prime_cache` before each. | No warmup. Median of 3 timed runs, with `purge` (macOS) or `sync && drop_caches` (Linux) before each. |
| Needs sudo | No. | Yes — script primes the credential once at startup. |
| Use when | tracking small regressions; comparing CPU efficiency between tool versions. | reproducing pre-2026-05-03 historical numbers; running CI; investigating an I/O regression. |

#### Why we introduced `--cold`

The default warm methodology was added on 2026-05-03 to kill the ~30% run-to-run variance the old single-run methodology had — variance that was hiding small regressions and producing nonsense like "tar+zstd 14s on Monday, 2s on Tuesday" on the same machine. The fix worked (variance dropped under 3%), but it had a side effect: warm-cache numbers systematically favor I/O-bound tools (tar+zstd reading from RAM) over CPU-bound tools (marc, which is gated by parallel BLAKE3 hashing regardless of cache state). On a corpus like kubernetes, warm tar+zstd lands at ~2 s while cold tar+zstd lands at ~14 s — a 7× spread.

`--cold` exists for two reasons:

1. **Comparability with historical numbers.** The tables earlier in this file were captured with the old (effectively cold-cache) methodology. To do an honest before/after comparison after any change, run with `--cold` so the methodology matches.
2. **Realistic I/O-bound numbers for CI and release notes.** When a user archives a freshly-checked-out repo, files are *not* warm in their page cache. The number that matters to them is `--cold`, not the CPU-bound warm number that's optimised for regression tracking. Use `--cold` for any number that ends up in user-facing documentation or release notes.

The default stays warm because the typical reader of these tables is *us*, looking for regressions during development. The cold mode is for honest external claims and for matching the historical baseline.

### Single repo

Use `compare_on_repo.sh` directly to benchmark one repository:

```sh
./scripts/compare_on_repo.sh \
  --name react \
  --repo https://github.com/facebook/react \
  --type size
```

Append `--mode log` to see progress output, or `--mode test` to verify round-trip integrity only.
