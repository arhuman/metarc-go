#!/usr/bin/env bash
set -euo pipefail

# All progress/debug output goes to stderr so that stdout contains
# only the markdown table, enabling:  ./run_bench.sh >> RESULTS.md

PLAYGROUND="$(cd "$(dirname "$0")" && pwd -P)"
COMPARE="$PLAYGROUND/compare_on_repo.sh"

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

# Print table header
"$COMPARE" --name header --repo header

for entry in "${REPOS[@]}"; do
    URL=$(echo "$entry" | awk '{print $1}')
    NAME=$(echo "$entry" | awk '{print $2}')
    "$COMPARE" --name "$NAME" --repo "$URL"
done
