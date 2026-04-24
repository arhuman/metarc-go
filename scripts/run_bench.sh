#!/usr/bin/env bash
set -euo pipefail

# Usage: ./run_bench.sh [--compression zstd|gz] [--type size|time|legacy]
#
# All progress/debug output goes to stderr so that stdout contains
# only the markdown table, enabling:  ./run_bench.sh >> RESULTS.md

PLAYGROUND="$(cd "$(dirname "$0")" && pwd -P)"
COMPARE="$PLAYGROUND/compare_on_repo.sh"

COMPRESSION="zstd"
TYPE="legacy"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --compression) COMPRESSION="$2"; shift 2 ;;
        --type)        TYPE="$2"; shift 2 ;;
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

EXTRA_ARGS="--compression $COMPRESSION --type $TYPE"

# Print table header
"$COMPARE" --name header --repo header $EXTRA_ARGS

for entry in "${REPOS[@]}"; do
    URL=$(echo "$entry" | awk '{print $1}')
    NAME=$(echo "$entry" | awk '{print $2}')
    COMMIT=$(echo "$entry" | awk '{print $3}')
    "$COMPARE" --name "$NAME" --repo "$URL" --commit "$COMMIT" $EXTRA_ARGS
done
