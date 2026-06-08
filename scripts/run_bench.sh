#!/usr/bin/env bash
set -euo pipefail

# Usage: ./run_bench.sh [--compression zstd|gz] [--type size|time|legacy]
#                       [--corpus full|tree] [--hot]
#
# All progress/debug output goes to stderr so that stdout contains
# only the markdown table, enabling:  ./run_bench.sh >> RESULTS.md
#
# Corpus form:
#
#   full (default)  measure the clone as checked out, .git included. What a
#                   user archiving a working directory actually gets, and the
#                   form every historical table was measured in. The packfiles
#                   are already compressed, so they pass through both tools
#                   untouched and pull every ratio toward parity.
#
#   tree            measure the source tree alone, exported with `git archive`
#                   at the pinned commit. Isolates the content class the tool
#                   targets, and is the only reproducible form: `git archive`
#                   stamps mtimes from the commit, a checkout stamps them
#                   with "now".
#
# Report both. The pair is the measurement: `full` states what users see,
# `tree` states what the compressor did, and the gap between them is the
# weight of the already-compressed objects.
#
# Cache mode (affects --type time only; size is deterministic):
#
#   default   COLD: flush the OS page cache before each timed iteration
#             (`purge` on macOS, `sync && drop_caches` on Linux). No
#             warmup, since warming would defeat the cold purpose.
#             Reflects realistic I/O-bound wall-clock — what users
#             actually experience when archiving a fresh checkout.
#             Matches the methodology of the pre-2026-05-03 historical
#             tables, so before/after comparisons are honest.
#
#             Needs sudo on both macOS (`purge`) and Linux (`drop_caches`).
#             The script primes sudo once at startup (one TouchID /
#             password prompt) so the 3-iteration loops run unattended.
#             If sudo isn't available, falls back to warm cache and
#             prints one warning rather than spamming stderr.
#
#   --hot     WARM: prime the OS page cache before every timed iteration,
#             then run one untimed warmup, then 3 timed runs. Median
#             reported. Variance < 3%. Reflects CPU-bound encoding speed
#             and is the right mode for tracking small regressions during
#             development. Doesn't need sudo.
#
# On macOS, the script self-wraps in `caffeinate -di` to prevent display
# sleep / idle throttling during the benchmark. Set NO_CAFFEINATE=1 to opt
# out (e.g. when running under another wrapper or in CI).

if [[ -z "${BENCH_CAFFEINATED:-}" && -z "${NO_CAFFEINATE:-}" ]] \
    && command -v caffeinate >/dev/null 2>&1; then
    export BENCH_CAFFEINATED=1
    exec caffeinate -di -- bash "$0" "$@"
fi

PLAYGROUND="$(cd "$(dirname "$0")" && pwd -P)"
COMPARE="$PLAYGROUND/compare_on_repo.sh"

COMPRESSION="zstd"
TYPE="legacy"
HOT_FLAG=""
CORPUS="full"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --compression) COMPRESSION="$2"; shift 2 ;;
        --type)        TYPE="$2"; shift 2 ;;
        --corpus)      CORPUS="$2"; shift 2 ;;
        --hot)         HOT_FLAG="--hot"; shift ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

# Pinned commits (2026-04-24) for reproducible benchmarks.
REPOS=(
    "https://github.com/kubernetes/kubernetes.git kubernetes 301f9afd23b8fcedc3a68ef1bd5b1177605e5497"
    "https://github.com/docker/compose docker-compose baaaaa3ff5633dbe49f33f34f2d7b2cb29429a5d"
    "https://github.com/vuejs/core vuejs 3310eea4ececff0379ea657e633e3c18b0f647eb"
    "https://github.com/numpy/numpy numpy 5dd04960e67241949513d174124d0e3d6578ba97"
    "https://github.com/redis/redis redis 47c51369eeffd55e1baf20df7955a3dfbe842fc4"
    "https://github.com/twbs/bootstrap bootstrap 41ceb03f5ea2032e09387ed68aef4b66ef901fec"
    "https://github.com/expressjs/express express 6340c1eaaedc0ddcae8be8df2cdb1d2e961cbf2f"
    "https://github.com/facebook/react react 561ed529b3a6a16e5b2b76fa5ee86c09f959686c"
)

EXTRA_ARGS="--compression $COMPRESSION --type $TYPE --corpus $CORPUS $HOT_FLAG"

# Default cache mode is COLD. Prompt for sudo upfront so subsequent
# flush_cache calls inherit a cached credential instead of failing
# silently per-iteration. If the prime fails, set BENCH_FLUSH_DEGRADED so
# flush_cache silences its per-iteration warning and the run quietly
# falls back to warm cache. Skipped when --hot is requested.
if [[ -z "$HOT_FLAG" && "$(uname -s)" == "Darwin" ]]; then
    echo "[run_bench] cold mode (default): priming sudo credential for purge" >&2
    if ! sudo -v 2>/dev/null; then
        echo "[run_bench] WARNING: sudo prime failed; cold mode will fall back to warm cache" >&2
        export BENCH_FLUSH_DEGRADED=1
    fi
fi

# Print table header
"$COMPARE" --name header --repo header $EXTRA_ARGS

for entry in "${REPOS[@]}"; do
    URL=$(echo "$entry" | awk '{print $1}')
    NAME=$(echo "$entry" | awk '{print $2}')
    COMMIT=$(echo "$entry" | awk '{print $3}')
    "$COMPARE" --name "$NAME" --repo "$URL" --commit "$COMMIT" $EXTRA_ARGS
done
