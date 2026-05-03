# zstd level tuning — kubernetes

_marc: metarc version v0.7.0-13-gd5647c1 (d5647c1, 2026-05-03T05:49:01Z) | tar: bsdtar 3.5.3 - libarchive 3.7.4 zlib/1.2.12 liblzma/5.4.3 bz2lib/1.0.8 _
_host: 26.3.1, Apple M3 Pro, 12 cores, 36G | timing: median of 3 after 1 warmup, page cache primed_
_corpus: 29838 files @ 301f9afd_

| metric             | value |
|--------------------|-------|
| tar+zstd time      | 0m16.362s (ceiling) |
| tar+zstd size      | 81.1M |
| marc baseline time | 0m4.876s |
| marc baseline size | 81.0M |

## blob

| level | size | gain | time | penalty | budget vs tar | status |
|-------|------|------|------|---------|---------------|--------|
| 7 | 81.0M | +0.00% | 0m4.904s | +0.6% | -70.0% | OK |
| 11 | 81.0M | +0.00% | 0m4.862s | -0.3% | -70.3% | OK |

## solid

| level | size | gain | time | penalty | budget vs tar | status |
|-------|------|------|------|---------|---------------|--------|
| 7 | 77.9M | -3.83% | 0m5.202s | +6.7% | -68.2% | OK |
| 11 | 75.5M | -6.81% | 0m10.970s | +125.0% | -33.0% | OK |

## catalog

| level | size | gain | time | penalty | budget vs tar | status |
|-------|------|------|------|---------|---------------|--------|
| 7 | 80.5M | -0.59% | 0m4.905s | +0.6% | -70.0% | OK |
| 11 | 80.3M | -0.86% | 0m5.105s | +4.7% | -68.8% | OK |

## Recommendation (per-chunk best within tar+zstd budget)

- `--zstd-level-blob 11`
- `--zstd-level-solid 11`
- `--zstd-level-catalog 11`

## Combined verification

    marc archive ... --zstd-level-blob 11 --zstd-level-solid 11 --zstd-level-catalog 11

| metric          | value |
|-----------------|-------|
| size            | 74.8M |
| gain vs base    | -7.68% |
| time            | 0m11.381s |
| penalty vs base | +133.4% |
| budget vs tar   | -30.4% |
| status          | OK (still under tar+zstd ceiling) |

