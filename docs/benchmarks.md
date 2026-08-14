# Metarc Benchmarks

All benchmarks run against shallow clones of real-world open source repositories,
archived with `marc` vs `tar+zstd`, on the same machine.

## Two corpus forms, and why both are reported

Every size table below exists twice, because a single number cannot answer both
questions worth asking.

**`--corpus full`: what a user gets.** The clone as checked out, `.git`
included. This is what happens when someone archives a working directory, and
it is the form every historical table in this file was measured in.

**`--corpus tree`: what the compressor did.** The source tree alone, exported
with `git archive` at the pinned commit.

The gap between the two is not noise to be cleaned up. It is the weight of the
already-compressed objects in the corpus, and it is large. On kubernetes, `.git`
is 48 MB: only 12% of the input bytes and 25 of the 29 838 files, but roughly
**58% of the resulting archive**, because packfiles are already deflate-compressed
and pass through both tools untouched. A ratio measured over the full clone is
therefore a weighted average of two very different regimes, dominated by the one
where neither tool can do anything. It answers "how much of my disk does this
save" honestly, and answers "is this compressor good" barely at all.

The reason to report both rather than pick one is that dropping `.git` would
also drop the only incompressible content in the whole suite. All eight corpora
are source code; `.git` is, by accident, the sole adversarial case present.
Deleting it would make every corpus flattering, and would hide a real defect it
currently exposes: `.pack` and `.idx` are not in the `passthrough/v1` allowlist,
so metarc spends level-11 CPU recompressing data that cannot compress. The
confound is worth classifying, not deleting.

### Reproducibility

`--corpus tree` is the only form that reproduces exactly. Two reasons, both
verified:

- **The pin fixes the tree, not the packfile.** `git fetch --depth 1` does not
  produce byte-identical packs across runs of the same pinned commit, so the
  corpus drifts. Observed on two runs of the same commit: the tar baseline moved
  from 13.9M to 14.1M on bootstrap and from 345.6K to 329.8K on express, while
  marc's output stayed put. Roughly 22 KB of that is `.git/index` alone, which
  stores per-file inode, device and nanosecond-mtime data and is different for
  every clone.
- **`git archive` stamps mtimes from the commit,** so repeated exports are
  byte-identical down to the timestamps. A checkout stamps them with "now",
  which changes marc's catalog (it records `mtime_ns` per entry) even when the
  content is identical.

Clones are now cached in `/tmp/<name>` and reused whenever they already contain
the pinned commit, which makes `--corpus full` reproducible too and removes the
re-download from every run. `rm -rf /tmp/<name>` forces a refetch.

> Read sub-0.5 pp differences between historical `full` tables as noise: those
> predate the cache and therefore predate reproducibility.

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

#### vs tar+zstd, full clone (.git included)

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

#### vs tar+zstd, source tree only

`./scripts/run_bench.sh --type size --corpus tree`

_ marc: metarc version v0.10.0-15-g686841db-dirty (686841db, 2026-08-14T04:29:13Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _
_ corpus: source tree only, exported with `git archive` at the pinned commit (no .git) _

| Repo | Original size | Files | tar+zstd size | marc size | % size of tar | full clone | masked by .git |
|------|---------------|-------|-------------------------|-----------|---------------|------------|----------------|
| kubernetes | 327M | 29813 | 34.6M | 28.0M | **81.1%** | 89.6% | 8.5 pp |
| react |  54M | 6859 | 8.2M | 6.8M | **83.3%** | 91.1% | 7.8 pp |
| redis |  23M | 1755 | 4.2M | 3.6M | **86.1%** | 92.5% | 6.4 pp |
| numpy |  40M | 2339 | 8.9M | 8.0M | **89.7%** | 94.6% | 4.9 pp |
| express | 1.3M | 213 | 135.8K | 123.1K | **90.6%** | 92.5% | 1.9 pp |
| docker-compose | 3.7M | 677 | 421.8K | 387.8K | **91.9%** | 95.5% | 3.6 pp |
| vuejs | 7.6M | 703 | 1.5M | 1.4M | **94.3%** | 95.9% | 1.6 pp |
| bootstrap |  20M | 791 | 6.6M | 6.3M | **95.8%** | 95.5% | -0.3 pp |

> Rows sorted by advantage. The last column is the point of reporting both
> forms: on the content metarc is built for, it wins by 4 to 19 percent, and the
> full-clone table understates that by up to 8.5 points because it averages in
> packfiles no compressor can touch.
>
> **bootstrap is the exception that confirms the reading.** It is the one corpus
> whose ratio gets slightly *worse* without `.git`, because its source tree is
> itself full of incompressible content (fonts, images, minified dist bundles).
> Removing the packfiles does not remove the incompressible fraction there, so
> nothing is unmasked. Corpora rank by how much compressible text they actually
> contain, which is the honest ordering.
>
> vuejs is the weakest genuine result (94.3%): a small, homogeneous TypeScript
> tree where zstd's own window already reaches everything metarc groups.

#### vs tar+gz, full clone (.git included)

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

#### vs tar+gz, source tree only

`./scripts/run_bench.sh --type size --corpus tree --compression gz`

_ marc: metarc version v0.10.0-15-g686841db-dirty (686841db, 2026-08-14T04:29:13Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _
_ corpus: source tree only, exported with `git archive` at the pinned commit (no .git) _

| Repo | Original size | Files | tar+gz size | marc size | % size of tar | full clone |
|------|---------------|-------|-------------------------|-----------|---------------|------------|
| kubernetes | 327M | 29813 | 42.2M | 28.0M | **66.4%** | 80.6% |
| react |  54M | 6859 | 9.2M | 6.8M | **74.0%** | 84.5% |
| bootstrap |  20M | 791 | 7.7M | 6.3M | **82.1%** | 90.2% |
| redis |  23M | 1755 | 4.2M | 3.6M | **85.5%** | 91.4% |
| numpy |  40M | 2339 | 9.2M | 8.0M | **86.5%** | 92.1% |
| docker-compose | 3.7M | 677 | 439.2K | 387.8K | **88.3%** | 90.9% |
| express | 1.3M | 213 | 131.1K | 123.1K | **93.9%** | 90.3% |
| vuejs | 7.6M | 703 | 1.5M | 1.4M | **95.9%** | 95.4% |

> Against tar+gz, marc shines on large, Go-heavy or mixed-language repos.
> Against tar+zstd, Metarc is now better on all repos.
>
> The two forms disagree in direction on express and vuejs here, by under
> 4 points on archives of a few hundred KB. That is what the reproducibility
> section warns about: gz has no dictionary to lose, so the small corpora are
> dominated by framing and catalog overhead rather than by the corpus form.

### Time

#### vs tar+zstd, v0.11.1 (current default: solid level 11)

 `./scripts/run_bench.sh --type time`

_ marc: metarc version v0.11.1 (c87305c1, 2026-08-14T11:49:32Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _
_ host: 26.5.2, Apple M3 Pro, 12 cores, 36G | timing: median of 3 runs, cache flushed before each run (cold) _

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

> Measured per corpus with `compare_on_repo.sh` rather than in one
> `run_bench.sh` pass, because the full cold run exceeds a single execution
> slot. Same script, same methodology, one row per invocation.
>
> **These are second-pass numbers, and the reason matters.** The first pass of
> this table read systematically high: kubernetes measured 13.286s, then
> 10.279s, 9.175s, 9.275s, 8.818s across five repetitions of the same binary on
> the same corpus. tar stayed inside 5.26s to 5.68s over the same repetitions,
> so the drift is on marc's side, and cold mode runs no untimed warmup (unlike
> `--hot`). Every row above was taken after the machine had settled. Discarding
> the first pass moved kubernetes from "2.5× slower" to "1.7× slower" and redis
> from "1.6× slower" to parity, so this is not a rounding detail. **Treat a
> single cold row as ±15% on marc's columns.**
>
> **The cold flush is real, and was verified rather than assumed.** react
> archived with tar in 1.758s cold against 0.447s hot, a 4× penalty, while marc
> moved 2.071s to 2.258s. tar is I/O-bound and feels the flush; marc is
> CPU-bound and does not.
>
> **The level is the whole story on the archive side.** react measured in two
> alternating passes (level 3, level 11, level 3, level 11) gives 0.919s/0.978s
> at level 3 for 17.9M (99.7% of tar), against 1.547s/1.502s at the level-11
> default for 16.8M (93.5%): 1.6× the time for 6.2 points of ratio. The two
> sizes reproduced exactly. Level 7 is the documented middle
> (`internal/store/zstdcfg.go`).
>
> **The baseline moved more than marc did.** Against the v0.8.0 table below,
> marc went 12.464s to 8.818s on kubernetes and 2.268s to 1.771s on react, while
> tar went 18.748s to 5.262s and 4.636s to 1.627s. marc got about 25% faster;
> tar got 3× faster. The host also moved (26.3.1 to 26.5.2). Cold-read
> throughput on this machine roughly tripled, which removed the I/O floor that
> had favoured a parallel reader over tar. The level-3 to level-11 change ate
> marc's share of that speedup and then some.

#### vs tar+zstd, v0.8.0 (historical, solid level 3)

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


#### vs tar+gz, v0.11.1 (current default: solid level 11)

`./scripts/run_bench.sh --type time --compression gz`

_ marc: metarc version v0.11.1 (c87305c1, 2026-08-14T11:49:32Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _
_ host: 26.5.2, Apple M3 Pro, 12 cores, 36G | timing: median of 3 runs, cache flushed before each run (cold) _

| Repo | Files | tar+gz arc | marc arc | tar+gz ext | marc ext | marc speedup (arc) |
|------|-------|------------------------|----------|-----------------------|----------|--------------------|
| kubernetes | 29838 | 0m9.111s | 0m8.614s | 0m3.731s | 0m4.413s | 1.1× faster |
| docker-compose | 702 | 0m0.444s | 0m0.184s | 0m0.237s | 0m0.132s | 2.4× faster |
| vuejs | 728 | 0m0.519s | 0m0.289s | 0m0.274s | 0m0.132s | 1.8× faster |
| numpy | 2364 | 0m1.489s | 0m1.004s | 0m0.386s | 0m0.432s | 1.5× faster |
| redis | 1780 | 0m0.975s | 0m0.699s | 0m0.307s | 0m0.269s | 1.4× faster |
| bootstrap | 816 | 0m0.827s | 0m0.423s | 0m0.278s | 0m0.160s | 2× faster |
| express | 238 | 0m0.310s | 0m0.107s | 0m0.236s | 0m0.064s | 2.9× faster |
| react | 6884 | 0m2.192s | 0m1.478s | 0m0.859s | 0m0.918s | 1.5× faster |

> **Against gzip, marc wins everywhere, and that is the informative part.**
> The same binary that loses to tar+zstd on the four largest corpora beats
> tar+gz on all eight. marc's own archive times are the same in both tables
> (kubernetes 8.614s here, 8.818s there; react 1.478s here, 1.771s there): only
> the baseline changed. marc sits between the two compressors in CPU cost while
> producing smaller archives than either, which is the trade the project makes.
>
> **In gz mode the noisy side is tar, not marc.** Two passes of this table give
> tar+gz on numpy at 2.787s then 1.489s, an 87% swing, while marc moved 1.179s
> to 1.004s. In the zstd table it was the reverse. Published rows are the second
> pass in both cases. First-pass speedup labels, for the spread: kubernetes 1.1×,
> docker-compose 1.9×, vuejs 2.3×, numpy 2.4×, redis 1.3×, bootstrap 2.3×,
> express 2.3×, react 1.2×.

#### vs tar+gz, v0.8.0 (historical, solid level 3)

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

> The two v0.8.0 tables are kept as the historical baseline. They were taken
> with the solid level at 3, on a host whose cold-read throughput was about a
> third of what the same machine now delivers. Their zstd archive-speed
> advantage does not reproduce on v0.11.1; their gz advantage does, though
> smaller on the corpora where tar+gz itself got faster.

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
| `--corpus` | `full`, `tree` | `full` | `full` archives the clone with its `.git` (what a user gets); `tree` archives a `git archive` export of the pinned commit (what the compressor did, and the only reproducible form). See the section at the top of this file. |
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

2026-08-14 (v0.11.1, cold timings): **The archive-speed claim no longer holds.**
The time table was re-run cold on v0.11.1, the first re-run since v0.8.0. marc
now archives slower than tar+zstd on the four largest corpora (kubernetes 1.7×
slower), at parity on redis, and faster on the three smallest, while extraction
stays faster on six of eight. Two independent causes, both measured rather than
inferred: the solid level moved from 3 to 11 (react in alternating passes:
0.919s/0.978s and 99.7% of tar at level 3, against 1.547s/1.502s and 93.5% at
level 11, so 1.6× the time for 6.2 points of ratio), and the host's cold-read
throughput roughly tripled (tar on kubernetes went 18.748s to 5.262s while marc
went 12.464s to 8.818s). The cold flush was verified, not assumed: react
cold-vs-hot is 1.758s against 0.447s for tar, and 2.071s against 2.258s for
marc. The v0.8.0 tables are kept as the historical baseline.

The gz table was re-run too, and it inverts the conclusion: against tar+gz the
same binary is faster on all eight corpora (1.1× to 2.9×). marc's archive times
are the same in both tables; only the baseline differs. marc costs more CPU than
zstd and less than gzip, while producing smaller archives than either. In gz
mode the unstable side is tar (numpy 2.787s then 1.489s across two passes)
rather than marc (1.179s then 1.004s).

**Methodology finding from that run: cold mode has no warmup, and it shows.**
The first pass of the table read high on every row. Repeating kubernetes five
times gave 13.286s, 10.279s, 9.175s, 9.275s, 8.818s for the same binary and
corpus, while tar stayed inside 5.26s to 5.68s: a 45% overstatement on the first
measurement, entirely on marc's side. Published rows are therefore second-pass.
`--hot` runs one untimed warmup; cold deliberately does not, on the grounds that
a warmup would repopulate the page cache. That reasoning covers the cache but
not CPU frequency, thermal state, or scheduler warmup, which is what these
numbers expose. A warmup run whose page cache is flushed afterwards would fix
the bias without warming the cache, and is the obvious next change to the
harness.

`scripts/run_bench.sh` and `scripts/compare_on_repo.sh` also had their sudo
probe fixed as part of this run: they primed with `sudo -v`, which a sudoers
entry scoped to `purge` alone does not satisfy, so a machine configured exactly
for this benchmark would have been declared degraded and silently measured warm.
The probe now tries `sudo -n purge` first.

2026-08-14 (v0.11.1): **Dependency bumps verified against both size tables.**
`klauspost/compress` 1.18.5 to 1.19.2 and `modernc.org/sqlite` 1.48.2 to 1.56.0
were merged, then both size tables re-run on the resulting binary. The
`--corpus tree` table reproduced **identically, every cell**: the compressor
library bump moves no output bytes. marc's sizes in the `--corpus full` table
are unchanged too, but its ratios move by 1 to 5 points because the tar baseline
drifted (kubernetes 81.0M to 79.2M, express 345.6K to 329.8K, react 18.4M to
17.9M) on cached clones whose packs differ from the ones the table above was
measured on. docker-compose reads 100.8% in that run, entirely from the baseline
side. The table above is kept as the run it was; the README carries the v0.11.1
figures. This is the second independent confirmation that `tree` is the only
form worth quoting a number from.

2026-08-14 (later still): **Two corpus forms, cached clones, `git archive` export.**
`--corpus tree` measures a `git archive` export of the pinned commit instead of
the clone. Clones are now cached in `/tmp/<name>` and reused when they already
hold the pinned commit, so re-runs cost no network and both forms reproduce
byte-for-byte. Rationale and the full-vs-tree gap are documented at the top of
this file. The `.git`-inclusive tables are kept, not replaced: they are the
user-facing number, and `.git` is the only incompressible content in the suite.

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
