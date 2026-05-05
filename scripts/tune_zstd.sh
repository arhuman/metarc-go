#!/usr/bin/env bash
set -euo pipefail

# Usage: ./tune_zstd.sh --name <name> --repo <repourl> [--commit <sha>] [--keep-clone] [--hot]
#
# Sweeps marc's --zstd-level-{blob,solid,catalog} levels against a single
# corpus, printing per-chunk gain vs the level-3 baseline. Stops the inner
# sweep for a given chunk as soon as marc archive time exceeds tar+zstd
# archive time, then moves on to the next chunk.
#
# Output: a markdown report on stdout. Progress on stderr.
#
# Cache mode:
#   (default)  COLD: cache flushed before each timed run. Realistic
#              wall-clock; matches scripts/run_bench.sh default. Needs
#              sudo on macOS / Linux; falls back to warm with a warning
#              if sudo isn't available.
#   --hot      WARM: cache primed + 1 warmup. Lower variance — preferable
#              when you specifically want to compare compression *levels*
#              of the same tool (the level deltas can be smaller than the
#              cold-cache noise floor).
#
# On macOS, the script self-wraps in `caffeinate -di` to prevent display
# sleep / idle throttling during the run. Set NO_CAFFEINATE=1 to opt out.

if [[ -z "${TUNE_CAFFEINATED:-}" && -z "${NO_CAFFEINATE:-}" ]] \
    && command -v caffeinate >/dev/null 2>&1; then
    export TUNE_CAFFEINATED=1
    exec caffeinate -di -- bash "$0" "$@"
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
# shellcheck source=lib/bench-helpers.sh
source "$SCRIPT_DIR/lib/bench-helpers.sh"
MARC="$SCRIPT_DIR/../bin/marc"

# --- arg parsing ---

NAME=""
REPO=""
COMMIT=""
KEEP_CLONE=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --name)        NAME="$2"; shift 2 ;;
        --repo)        REPO="$2"; shift 2 ;;
        --commit)      COMMIT="$2"; shift 2 ;;
        --keep-clone)  KEEP_CLONE=1; shift ;;
        --hot)         export BENCH_HOT=1; shift ;;
        *) die "unknown option: $1" ;;
    esac
done

[[ -z "$NAME" || -z "$REPO" ]] && die "usage: $0 --name <name> --repo <repourl> [--commit <sha>] [--keep-clone] [--hot]"
[[ -x "$MARC" ]] || die "marc binary not found at $MARC; run 'make build' first"

log() { echo "[tune_zstd] $*" >&2; }

# Default cache mode is COLD. Prime sudo upfront on macOS so the
# per-iteration `sudo -n purge` calls in time_median subshells succeed
# silently. If the prime fails, set BENCH_FLUSH_DEGRADED so flush_cache
# silences its per-iteration warning and the run quietly falls back to
# warm cache. Skipped when --hot is requested.
if [[ "${BENCH_HOT:-0}" != "1" && "$(uname -s)" == "Darwin" ]]; then
    log "cold mode (default): priming sudo credential for purge"
    if ! sudo -v 2>/dev/null; then
        log "WARNING: sudo prime failed; cold mode will fall back to warm cache"
        export BENCH_FLUSH_DEGRADED=1
    fi
fi

# --- workspace ---

WORKDIR="/tmp"
DIR="$WORKDIR/$NAME"
TAR_FILE="$WORKDIR/${NAME}.tar.zst"
MARC_FILE="$WORKDIR/${NAME}.marc"

# --- clone (or reuse) ---

if [[ "$KEEP_CLONE" -eq 1 && -d "$DIR" && -d "$DIR/.git" ]]; then
    log "reusing existing clone at $DIR"
else
    log "cloning $NAME ..."
    rm -rf "$DIR" "$TAR_FILE" "$MARC_FILE"
    if [[ -n "$COMMIT" ]]; then
        git init "$DIR" >/dev/null 2>&1
        git -C "$DIR" remote add origin "$REPO" 2>/dev/null
        git -C "$DIR" fetch --depth 1 origin "$COMMIT" 2>/dev/null
        git -C "$DIR" checkout FETCH_HEAD 2>/dev/null
    else
        git clone --depth 1 "$REPO" "$DIR" 2>/dev/null
    fi
fi

FILE_COUNT=$(find "$DIR" -type f | wc -l | tr -d ' ')

# --- bench commands ---

tar_cmd()             { tar --zstd -cf "$TAR_FILE" -C "$WORKDIR" "$NAME" 2>/dev/null; }
marc_baseline_cmd()   { "$MARC" archive "$MARC_FILE" "$DIR" 2>/dev/null; }

# --- header ---

MARC_VERSION=$("$MARC" --version 2>&1 | head -1 || echo "unknown")
TAR_VERSION=$(tar --version 2>/dev/null | head -1 || echo "unknown")
OS_VER=$(sw_vers -productVersion 2>/dev/null || uname -sr)
CPU=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || uname -m)
CORES=$(sysctl -n hw.ncpu 2>/dev/null || nproc 2>/dev/null || echo "?")
MEM_BYTES=$(sysctl -n hw.memsize 2>/dev/null || echo 0)
MEM=$(awk -v b="$MEM_BYTES" 'BEGIN{ if (b > 0) printf "%dG", b/1073741824; else print "?" }')

if [[ "${BENCH_HOT:-0}" == "1" ]]; then
    METHODOLOGY="median of 3 runs after 1 warmup, page cache primed (warm)"
elif [[ "${BENCH_FLUSH_DEGRADED:-0}" == "1" ]]; then
    METHODOLOGY="median of 3 runs (cold requested but sudo unavailable; degraded to warm)"
else
    METHODOLOGY="median of 3 runs, cache flushed before each run (cold)"
fi

echo "# zstd level tuning — $NAME"
echo
echo "_ marc: ${MARC_VERSION} | tar: ${TAR_VERSION}_"
echo "_ host: ${OS_VER}, ${CPU}, ${CORES} cores, ${MEM} | timing: ${METHODOLOGY}_"
echo "_ corpus: ${FILE_COUNT} files"$([[ -n "$COMMIT" ]] && echo " @ ${COMMIT:0:8}" || echo "")"_"
echo

# --- baselines ---

log "measuring tar+zstd baseline (ceiling)..."
T_TAR=$(time_median "$DIR" 'rm -f "$TAR_FILE"' 'tar_cmd')
S_TAR=$(file_bytes "$TAR_FILE")

log "measuring marc baseline (all levels = 3)..."
T_BASE=$(time_median "$DIR" 'rm -f "$MARC_FILE"' 'marc_baseline_cmd')
S_BASE=$(file_bytes "$MARC_FILE")
verify_roundtrip "$MARC_FILE" "$DIR" || die "baseline round-trip failed"

echo "| metric             | value |"
echo "|--------------------|-------|"
echo "| tar+zstd time      | ${T_TAR} (ceiling) |"
echo "| tar+zstd size      | $(fmt_bytes "$S_TAR") |"
echo "| marc baseline time | ${T_BASE} |"
echo "| marc baseline size | $(fmt_bytes "$S_BASE") |"
echo

# --- sweep ---

declare -A BEST_LEVEL  # chunk -> picked level (3 if both levels go OVER)

for chunk in blob solid catalog; do
    BEST_LEVEL[$chunk]=3
    echo "## ${chunk}"
    echo
    echo "| level | size | gain | time | penalty | budget vs tar | status |"
    echo "|-------|------|------|------|---------|---------------|--------|"

    for level in 7 11; do
        log "sweeping ${chunk} level=${level} ..."
        # Closure: bash dynamic scope means $chunk and $level are visible
        # when time_median's eval invokes this function.
        marc_at_level_cmd() {
            "$MARC" archive "$MARC_FILE" "$DIR" \
                "--zstd-level-${chunk}" "$level" 2>/dev/null
        }
        T=$(time_median "$DIR" 'rm -f "$MARC_FILE"' 'marc_at_level_cmd')
        S=$(file_bytes "$MARC_FILE")
        verify_roundtrip "$MARC_FILE" "$DIR" \
            || die "round-trip failed: chunk=${chunk} level=${level}"

        gain=$(pct_diff "$S_BASE" "$S")
        penalty=$(pct_diff_seconds "$T_BASE" "$T")
        budget=$(pct_diff_seconds "$T_TAR" "$T")
        if seconds_lt "$T" "$T_TAR"; then
            status="OK"
            BEST_LEVEL[$chunk]="$level"  # latest in-budget level wins
        else
            status="OVER"
        fi

        echo "| ${level} | $(fmt_bytes "$S") | ${gain} | ${T} | ${penalty} | ${budget} | ${status} |"

        # PER-CHUNK STOP: end this chunk's sweep on first OVER.
        [[ "$status" == "OVER" ]] && break
    done
    echo
done

# --- recommendation ---

echo "## Recommendation (per-chunk best within tar+zstd budget)"
echo
any_above_default=0
for chunk in blob solid catalog; do
    lvl="${BEST_LEVEL[$chunk]}"
    if [[ "$lvl" -gt 3 ]]; then
        any_above_default=1
        echo "- \`--zstd-level-${chunk} ${lvl}\`"
    else
        echo "- \`${chunk}\`: keep default (level 3) — no in-budget improvement found"
    fi
done
echo

# --- combined verification ---

if [[ "$any_above_default" -eq 1 ]]; then
    args=()
    for chunk in blob solid catalog; do
        lvl="${BEST_LEVEL[$chunk]}"
        if [[ "$lvl" -gt 3 ]]; then
            args+=("--zstd-level-${chunk}" "$lvl")
        fi
    done
    log "verifying combined config: ${args[*]} ..."
    marc_combined_cmd() {
        "$MARC" archive "$MARC_FILE" "$DIR" "${args[@]}" 2>/dev/null
    }
    T_COMB=$(time_median "$DIR" 'rm -f "$MARC_FILE"' 'marc_combined_cmd')
    S_COMB=$(file_bytes "$MARC_FILE")
    verify_roundtrip "$MARC_FILE" "$DIR" || die "combined round-trip failed"

    gain_comb=$(pct_diff "$S_BASE" "$S_COMB")
    penalty_comb=$(pct_diff_seconds "$T_BASE" "$T_COMB")
    budget_comb=$(pct_diff_seconds "$T_TAR" "$T_COMB")
    if seconds_lt "$T_COMB" "$T_TAR"; then
        comb_status="OK (still under tar+zstd ceiling)"
    else
        comb_status="OVER (exceeds tar+zstd ceiling)"
    fi

    echo "## Combined verification"
    echo
    echo "    marc archive ... ${args[*]}"
    echo
    echo "| metric          | value |"
    echo "|-----------------|-------|"
    echo "| size            | $(fmt_bytes "$S_COMB") |"
    echo "| gain vs base    | ${gain_comb} |"
    echo "| time            | ${T_COMB} |"
    echo "| penalty vs base | ${penalty_comb} |"
    echo "| budget vs tar   | ${budget_comb} |"
    echo "| status          | ${comb_status} |"
    echo
fi

# --- cleanup ---

if [[ "$KEEP_CLONE" -eq 0 ]]; then
    rm -rf "$DIR" "$TAR_FILE" "$MARC_FILE"
fi

log "done"
