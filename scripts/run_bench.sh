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

REPOS=(
    "https://github.com/kubernetes/kubernetes.git kubernetes"
    "https://github.com/docker/compose docker-compose"
    "https://github.com/vuejs/core vuejs"
    "https://github.com/numpy/numpy numpy"
    "https://github.com/redis/redis redis"
    "https://github.com/twbs/bootstrap bootstrap"
    "https://github.com/expressjs/express express"
    "https://github.com/facebook/react react"
)

EXTRA_ARGS="--compression $COMPRESSION --type $TYPE"

# Print table header
"$COMPARE" --name header --repo header $EXTRA_ARGS

for entry in "${REPOS[@]}"; do
    URL=$(echo "$entry" | awk '{print $1}')
    NAME=$(echo "$entry" | awk '{print $2}')
    "$COMPARE" --name "$NAME" --repo "$URL" $EXTRA_ARGS
done
