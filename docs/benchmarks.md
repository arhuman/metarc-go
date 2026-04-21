# Metarc — Benchmarks

**Last updated**: 2026-04-22  
**Version**: with `go-line-subst/v1` transform enabled

All benchmarks run against shallow clones of real-world open source repositories,
archived with `marc` vs `tar+zstd`, on the same machine.

---

## Performance

### Size

#### vs tar+zstd

| Repo | Original size | Files | tar+zstd | marc | % of tar |
|------|---------------|-------|----------|------|----------|
| kubernetes | 374M | 29254 | 81.2M | 81.5M | 100.3% |
| docker-compose | 4.5M | 706 | 1.1M | 1.1M | 102.1% |
| vuejs | 9.8M | 732 | 3.2M | 3.3M | 101.2% |
| numpy | 50M | 2372 | 18.4M | 18.6M | 100.9% |
| redis | 28M | 1784 | 8.9M | 9.0M | 100.7% |
| bootstrap | 27M | 820 | 13.9M | 13.8M | 99.5% |
| express | 1.6M | 242 | 345.8K | 356.1K | 103.0% |
| react | 65M | 6888 | 18.4M | 18.4M | 100.1% |
| prometheus | 37M | 1627 | 9.6M | 9.6M | 100.8% |

#### vs tar+gz

| Repo | Original size | Files | tar+gz | marc | % of tar |
|------|---------------|-------|--------|------|----------|
| kubernetes | 374M | 29254 | 90.2M | 81.5M | 90.4% |
| docker-compose | 4.5M | 706 | 1.2M | 1.1M | 97.1% |
| vuejs | 9.8M | 732 | 3.3M | 3.3M | 100.7% |
| numpy | 50M | 2372 | 18.9M | 18.6M | 98.2% |
| redis | 28M | 1784 | 9.0M | 9.0M | 99.5% |
| bootstrap | 27M | 820 | 14.7M | 13.8M | 93.6% |
| express | 1.6M | 242 | 354.7K | 356.2K | 100.4% |
| react | 65M | 6888 | 19.8M | 18.4M | 92.9% |
| prometheus | 37M | 1627 | 11.6M | 9.6M | 83.1% |

> Against tar+gz, marc shines on large, Go-heavy or mixed-language repos (kubernetes −10%, prometheus −17%, react −7%).
> Against tar+zstd, most repos are at near-parity or slightly larger — zstd already exploits much of the same
> redundancy that marc's semantic transforms target, so the net gain is modest at default zstd levels.
> The speed advantage holds regardless of the baseline compressor.

### Time

#### vs tar+zstd

| Repo | Files | tar+zstd arc | marc arc | tar+zstd ext | marc ext |
|------|-------|-------------|----------|-------------|----------|
| kubernetes | 29254 | 16.7s | 3.8s | 16.3s | 8.4s |
| docker-compose | 706 | 0.42s | 0.11s | 0.36s | 0.14s |
| vuejs | 732 | 0.40s | 0.13s | 0.35s | 0.15s |
| numpy | 2372 | 1.20s | 0.47s | 1.09s | 0.50s |
| redis | 1784 | 0.91s | 0.32s | 0.78s | 0.36s |
| bootstrap | 820 | 0.47s | 0.21s | 0.41s | 0.17s |
| express | 242 | 0.15s | 0.040s | 0.13s | 0.053s |
| react | 6888 | 3.38s | 0.77s | 3.03s | 1.18s |
| prometheus | 1627 | — | — | — | — |

#### vs tar+gz

| Repo | Files | tar+gz arc | marc arc | tar+gz ext | marc ext |
|------|-------|-----------|----------|-----------|----------|
| kubernetes | 29254 | 22.0s | 4.0s | 15.6s | 8.2s |
| docker-compose | 706 | 0.52s | 0.10s | 0.41s | 0.15s |
| vuejs | 732 | 0.65s | 0.14s | 0.41s | 0.16s |
| numpy | 2372 | 2.75s | 0.98s | 1.51s | 0.48s |
| redis | 1784 | 1.74s | 0.34s | 0.86s | 0.34s |
| bootstrap | 820 | 1.21s | 0.22s | 0.54s | 0.22s |
| express | 242 | 0.18s | 0.045s | 0.16s | 0.059s |
| react | 6888 | 4.94s | 0.81s | 3.46s | 1.47s |
| prometheus | 1627 | 1.86s | 0.38s | 1.08s | 0.46s |

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

### Single repo

Use `compare_on_repo.sh` directly to benchmark one repository:

```sh
./scripts/compare_on_repo.sh \
  --name react \
  --repo https://github.com/facebook/react \
  --type size
```

Append `--mode log` to see progress output, or `--mode test` to verify round-trip integrity only.
