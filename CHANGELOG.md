# Changelog

All notable changes to this project are documented here.
Format: [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [Unreleased]

## [0.11.1] - 2026-08-14

## [0.11.0] - 2026-08-14

Archives written by this version remain readable by earlier binaries, and this
version still extracts archives written by earlier ones.

### Added

- `--zstd-window` sets the zstd match window (power of two). Defaults to the solid
  block size. The resolved value is recorded in the archive and shown by `marc inspect`.
- `make cover-html` opens the coverage report in a browser. `make cover` now enforces
  a coverage floor instead, and `make audit` depends on it.
- `scripts/run_bench.sh --corpus tree` benchmarks the source tree alone, exported with
  `git archive`, alongside the existing whole-clone measurement.

### Changed

- Default solid block size is 32 MiB, up from 16 MiB, and the zstd match window now
  follows the block size instead of staying at the library default of 8 MiB.
- Solid blocks no longer flush on an extension change while the block holds less than
  1 MiB, so corpora with many rare extensions stop producing undersized frames.
- The archive catalog is substantially smaller: indexes only the writer needs are
  dropped before serialization, `blobs.sha` stores a 128-bit content-address prefix,
  and the catalog is compressed at zstd level 11.
- `--disable-transform dedup/v1` now fails with an explicit error. Deduplication is
  structural and could not be disabled; the flag was silently ignored.

Measured against v0.10.0 on source trees without `.git`: express -10.7%,
docker-compose -9.9%, kubernetes -5.2%, react -4.9%.

### Fixed

- `log-date-subst/v2` no longer tokenizes syslog, log4j and python timestamps. On
  those formats it produced archives 5% to 22% larger per file, and syslog day
  padding did not survive a round trip. All nine formats still decode, so existing
  archives extract unchanged.
- `--final-compressor none` is honored in solid mode. It was silently ignored unless
  combined with `--no-solid`.

## [0.10.0] and earlier

See the [GitHub releases](https://github.com/arhuman/metarc/releases) for tagged versions.
