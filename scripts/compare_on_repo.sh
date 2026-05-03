#!/usr/bin/env bash
set -euo pipefail

# Usage: ./compare_on_repo.sh --name <name> --repo <repourl> [--mode log|test] [--compression zstd|gz] [--type size|time|legacy] [--cold]
#
# Clones <repourl> into /tmp/<name>, archives with tar and marc,
# extracts marc into /tmp/<name>2, compares, and prints one markdown
# table row with columns determined by --type.
#
# Modes:
#   (default)    print the markdown table row only
#   --mode log   print only the log/progress output
#   --mode test  print "true" or "false" (round-trip success)
#
# Compression:
#   --compression zstd  use tar+zstd (default)
#   --compression gz    use tar+gz
#
# Types:
#   --type legacy       original columns: all info (default)
#   --type size         size/ratio columns only
#   --type time         timing columns only
#
# Cache mode (affects --type time only):
#   (default)    WARM: prime the page cache before each timed run + 1 warmup.
#                Variance < 3%. Best for tracking small regressions because
#                the OS state is held constant across iterations. Numbers
#                reflect the tool's CPU-bound speed when I/O is not the
#                bottleneck.
#
#   --cold       COLD: flush the page cache (purge / drop_caches) before
#                each timed run. No warmup. Reflects realistic I/O-bound
#                wall-clock — what users actually experience when archiving
#                files that aren't already in RAM. Use this to:
#                  - compare against pre-2026-05-03 historical numbers;
#                  - run unattended in CI for honest wall-clock results;
#                  - rule out an I/O regression that warm-cache would hide.
#                Needs sudo (`purge` on macOS, `drop_caches` on Linux). The
#                script primes sudo once at startup so the iteration loop
#                runs unattended; if sudo is unavailable, falls back to
#                warm-cache with one warning.
#
# Special case: --name header --repo header  → prints the table header.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
MARC="$SCRIPT_DIR/../bin/marc"

source "$SCRIPT_DIR/lib/bench-helpers.sh"

log() { :; }  # redefined after arg parsing

# --- parse args ---

NAME=""
REPO=""
MODE=""
COMPRESSION="zstd"
TYPE="legacy"
COMMIT=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --name)        NAME="$2"; shift 2 ;;
        --repo)        REPO="$2"; shift 2 ;;
        --mode)        MODE="$2"; shift 2 ;;
        --compression) COMPRESSION="$2"; shift 2 ;;
        --type)        TYPE="$2"; shift 2 ;;
        --commit)      COMMIT="$2"; shift 2 ;;
        --cold)        export BENCH_COLD=1; shift ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

if [[ -z "$NAME" || -z "$REPO" ]]; then
    echo "Usage: $0 --name <name> --repo <repourl> [--commit <sha>] [--mode log|test] [--compression zstd|gz] [--type size|time|legacy] [--cold]" >&2
    exit 1
fi

# In cold mode on macOS, prime sudo credentials upfront so subsequent
# `sudo -n purge` calls in time_median subshells inherit the cached
# credential. Without this, each subshell tries `sudo -n` and falls
# through (TouchID/password sudo doesn't work non-interactively).
# If the prime fails, set BENCH_COLD_DEGRADED so flush_cache silences
# its per-iteration warning.
if [[ "${BENCH_COLD:-0}" == "1" && "$(uname -s)" == "Darwin" ]]; then
    if ! sudo -v 2>/dev/null; then
        echo "[compare_on_repo] WARNING: sudo prime failed; --cold will run with warm cache" >&2
        export BENCH_COLD_DEGRADED=1
    fi
fi

# Define log() based on mode
if [[ "$MODE" == "log" ]]; then
    log() { echo "$@"; }
else
    log() { :; }
fi

# --- special case: header ---

if [[ "$NAME" == "header" && "$REPO" == "header" ]]; then
    MARC_VERSION=$("$MARC" --version 2>&1 || echo "unknown")
    TAR_VERSION=$(tar --version 2>/dev/null | head -1 || echo "unknown")
    echo "_marc: ${MARC_VERSION} | tar: ${TAR_VERSION}_"
    if [[ "$TYPE" == "time" || "$TYPE" == "legacy" ]]; then
        OS_VER=$(sw_vers -productVersion 2>/dev/null || uname -sr)
        CPU=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || uname -m)
        CORES=$(sysctl -n hw.ncpu 2>/dev/null || nproc 2>/dev/null || echo "?")
        MEM_BYTES=$(sysctl -n hw.memsize 2>/dev/null || echo 0)
        MEM=$(awk -v b="$MEM_BYTES" 'BEGIN{ if (b > 0) printf "%dG", b/1073741824; else print "?" }')
        if [[ "${BENCH_COLD:-0}" == "1" ]]; then
            METHODOLOGY="median of 3 runs, cache flushed before each run (cold)"
        else
            METHODOLOGY="median of 3 runs after 1 warmup, page cache primed (warm)"
        fi
        echo "_host: ${OS_VER}, ${CPU}, ${CORES} cores, ${MEM} | timing: ${METHODOLOGY}_"
    fi
    echo ""
    case "$TYPE" in
        size)
            echo "| Repo | Original size | Files | tar+${COMPRESSION} size | marc size | % size of tar |"
            echo "|------|---------------|-------|-------------------------|-----------|---------------|"
            ;;
        time)
            echo "| Repo | Files | tar+${COMPRESSION} arc | marc arc | tar+${COMPRESSION} ext | marc ext | marc speedup (arc) |"
            echo "|------|-------|------------------------|----------|-----------------------|----------|--------------------|"
            ;;
        *)
            echo "| Repo | Original size | Files | tar+${COMPRESSION} | tar size | marc | marc size | % size of tar |"
            echo "|------|---------------|-------|---------------------|----------|------|-----------|---------------|"
            ;;
    esac
    exit 0
fi

# --- work in /tmp ---

WORKDIR="/tmp"
DIR="$WORKDIR/$NAME"
DIR2="$WORKDIR/${NAME}2"
TAR_EXTRACT_DIR="$WORKDIR/${NAME}_tar_extract"
MARC_FILE="$WORKDIR/${NAME}.marc"

if [[ "$COMPRESSION" == "zstd" ]]; then
    TAR_FILE="$WORKDIR/${NAME}.tar.zst"
    tar_cmd()         { tar --zstd -cf "$TAR_FILE" -C "$WORKDIR" "$NAME" 2>/dev/null; }
    tar_extract_cmd() { tar --zstd -xf "$TAR_FILE" -C "$TAR_EXTRACT_DIR" 2>/dev/null; }
else
    TAR_FILE="$WORKDIR/${NAME}.tgz"
    tar_cmd()         { tar czf "$TAR_FILE" -C "$WORKDIR" "$NAME" 2>/dev/null; }
    tar_extract_cmd() { tar xzf "$TAR_FILE" -C "$TAR_EXTRACT_DIR" 2>/dev/null; }
fi
marc_archive_cmd() { "$MARC" archive "$MARC_FILE" "$DIR" 2>/dev/null; }
marc_extract_cmd() { "$MARC" extract "$MARC_FILE" -C "$DIR2" 2>/dev/null; }

# cleanup from previous run
rm -rf "$DIR" "$DIR2" "$TAR_EXTRACT_DIR" "$TAR_FILE" "$MARC_FILE"

# 1. Clone at pinned commit (or shallow HEAD if no commit specified)
log "=== $NAME ==="
log "  cloning..."
if [[ -n "$COMMIT" ]]; then
    git init "$DIR" >/dev/null 2>&1
    git -C "$DIR" remote add origin "$REPO" 2>/dev/null
    git -C "$DIR" fetch --depth 1 origin "$COMMIT" 2>/dev/null
    git -C "$DIR" checkout FETCH_HEAD 2>/dev/null
else
    git clone --depth 1 "$REPO" "$DIR" 2>/dev/null
fi

# 2. Size + file count
ORIG_SIZE=$(human "$DIR")
FILE_COUNT=$(find "$DIR" -type f | wc -l | tr -d ' ')

# 3. tar archive + extract (median of 3 with cache priming + warmup)
log "  tar+${COMPRESSION}..."
TAR_TIME=$(time_median "$DIR" 'rm -f "$TAR_FILE"' 'tar_cmd')
TAR_BYTES=$(file_bytes "$TAR_FILE")
TAR_SIZE=$(fmt_bytes "$TAR_BYTES")
TAR_EXTRACT_TIME=$(time_median "$TAR_FILE" \
    'rm -rf "$TAR_EXTRACT_DIR" && mkdir "$TAR_EXTRACT_DIR"' \
    'tar_extract_cmd')
rm -rf "$TAR_EXTRACT_DIR"

# 4. marc archive (median of 3 with cache priming + warmup)
log "  marc archive..."
METARC_TIME=$(time_median "$DIR" 'rm -f "$MARC_FILE"' 'marc_archive_cmd')
MARC_BYTES=$(file_bytes "$MARC_FILE")
MARC_SIZE=$(fmt_bytes "$MARC_BYTES")

# 5. Round-trip verification (marc extract timed, median of 3)
log "  verifying round-trip..."
MARC_EXTRACT_TIME=$(time_median "$MARC_FILE" \
    'rm -rf "$DIR2" && mkdir "$DIR2"' \
    'marc_extract_cmd')
if diff -rq "$DIR" "$DIR2" > /dev/null 2>&1; then
    ROUNDTRIP="OK"
else
    ROUNDTRIP="FAIL"
    log "  ROUND-TRIP FAILED for $NAME"
fi

# 6. Ratio
RATIO=$(python3 -c "print(f'{$MARC_BYTES/$TAR_BYTES*100:.1f}%')")

# 7. Cleanup
rm -rf "$DIR" "$DIR2" "$TAR_EXTRACT_DIR" "$TAR_FILE" "$MARC_FILE"

# 8. Output based on mode and type
case "$MODE" in
    test)
        [[ "$ROUNDTRIP" == "OK" ]]
        ;;
    log)
        log "  done: tar=${TAR_SIZE}(arc=${TAR_TIME},ext=${TAR_EXTRACT_TIME}) marc=${MARC_SIZE}(arc=${METARC_TIME},ext=${MARC_EXTRACT_TIME}) ratio=${RATIO} round-trip=${ROUNDTRIP}"
        ;;
    *)
        case "$TYPE" in
            size)
                echo "| $NAME | ${ORIG_SIZE} | ${FILE_COUNT} | ${TAR_SIZE} | ${MARC_SIZE} | ${RATIO} |"
                ;;
            time)
                SPEEDUP=$(speedup_label "$TAR_TIME" "$METARC_TIME")
                echo "| $NAME | ${FILE_COUNT} | ${TAR_TIME} | ${METARC_TIME} | ${TAR_EXTRACT_TIME} | ${MARC_EXTRACT_TIME} | ${SPEEDUP} |"
                ;;
            *)
                echo "| $NAME | ${ORIG_SIZE} | ${FILE_COUNT} | ${TAR_TIME} | ${TAR_SIZE} | ${METARC_TIME} | ${MARC_SIZE} | ${RATIO} |"
                ;;
        esac
        ;;
esac
