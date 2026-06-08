#!/usr/bin/env bash
set -euo pipefail

# Usage: ./compare_on_repo.sh --name <name> --repo <repourl> [--mode log|test] [--compression zstd|gz] [--type size|time|legacy] [--hot] [--marc-args "..."]
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
# Corpus form (what gets measured):
#   --corpus full       the clone as checked out, .git included (default).
#                       This is the "reality" number: it is what a user gets
#                       archiving a working directory, and it is the form every
#                       historical table in docs/benchmarks.md was measured in.
#                       Its packfiles are already deflate-compressed, so they
#                       pass through both tools untouched and dilute the ratio.
#   --corpus tree       the source tree alone, exported with `git archive` at
#                       the pinned commit. Answers the content-type question:
#                       how do the tools compare on compressible text, with the
#                       already-compressed objects removed rather than averaged
#                       in? Also the only fully reproducible form: `git archive`
#                       stamps every file with the commit date, so repeated
#                       exports are byte-identical, mtimes included. A checkout
#                       stamps them with "now", which changes marc's catalog.
#
# The clone is cached in /tmp/<name> and reused when it already contains the
# pinned commit; `rm -rf /tmp/<name>` forces a refetch.
#
# Cache mode (affects --type time only):
#   (default)    COLD: flush the page cache (purge / drop_caches) before
#                each timed run. No warmup. Reflects realistic I/O-bound
#                wall-clock — what users actually experience when archiving
#                files that aren't already in RAM. Matches the methodology
#                of the pre-2026-05-03 historical tables, so before/after
#                comparisons are honest. Needs sudo (`purge` on macOS,
#                `drop_caches` on Linux); the script primes sudo once at
#                startup so the iteration loop runs unattended. If sudo is
#                unavailable, falls back to warm-cache with one warning.
#
#   --hot        WARM: prime the page cache before each timed run + 1
#                warmup. Variance < 3%. Best for tracking small regressions
#                during development — the OS state is held constant across
#                iterations so CPU-pipeline changes show up cleanly. Doesn't
#                need sudo. Use --hot when you don't care about realistic
#                wall-clock and just want low-variance numbers.
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
MARC_EXTRA_ARGS=""
CORPUS="full"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --name)        NAME="$2"; shift 2 ;;
        --repo)        REPO="$2"; shift 2 ;;
        --mode)        MODE="$2"; shift 2 ;;
        --compression) COMPRESSION="$2"; shift 2 ;;
        --type)        TYPE="$2"; shift 2 ;;
        --commit)      COMMIT="$2"; shift 2 ;;
        --corpus)      CORPUS="$2"; shift 2 ;;
        --hot)         export BENCH_HOT=1; shift ;;
        --marc-args)   MARC_EXTRA_ARGS="$2"; shift 2 ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

if [[ "$CORPUS" != "full" && "$CORPUS" != "tree" ]]; then
    echo "--corpus must be 'full' or 'tree', got '$CORPUS'" >&2
    exit 1
fi

if [[ -z "$NAME" || -z "$REPO" ]]; then
    echo "Usage: $0 --name <name> --repo <repourl> [--commit <sha>] [--mode log|test] [--compression zstd|gz] [--type size|time|legacy] [--hot] [--marc-args \"...\"]" >&2
    exit 1
fi

# Default cache mode is COLD. On macOS, prime sudo credentials upfront so
# subsequent `sudo -n purge` calls in time_median subshells inherit the
# cached credential (TouchID/password sudo doesn't work non-interactively).
# If the prime fails, set BENCH_FLUSH_DEGRADED so flush_cache silences its
# per-iteration warning and the run quietly falls back to warm cache.
# Skipped when --hot is requested.
if [[ "${BENCH_HOT:-0}" != "1" && "$(uname -s)" == "Darwin" ]]; then
    if ! sudo -v 2>/dev/null; then
        echo "[compare_on_repo] WARNING: sudo prime failed; cold mode will fall back to warm cache" >&2
        export BENCH_FLUSH_DEGRADED=1
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
    echo "_ marc: ${MARC_VERSION} | tar: ${TAR_VERSION}_"
    if [[ "$CORPUS" == "tree" ]]; then
        echo "_ corpus: source tree only, exported with \`git archive\` at the pinned commit (no .git) _"
    else
        echo "_ corpus: clone as checked out, .git included _"
    fi
    if [[ "$TYPE" == "time" || "$TYPE" == "legacy" ]]; then
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
        echo "_ host: ${OS_VER}, ${CPU}, ${CORES} cores, ${MEM} | timing: ${METHODOLOGY} _"
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
CLONE="$WORKDIR/$NAME"          # cached clone: working tree + .git
DIR2="$WORKDIR/${NAME}2"
TAR_EXTRACT_DIR="$WORKDIR/${NAME}_tar_extract"
MARC_FILE="$WORKDIR/${NAME}.marc"

# The measured directory. In full mode it is the clone itself; in tree mode it
# is a clean export beside it, so the clone survives as a cache.
if [[ "$CORPUS" == "tree" ]]; then
    MEASURED="${NAME}_tree"
else
    MEASURED="$NAME"
fi
DIR="$WORKDIR/$MEASURED"

if [[ "$COMPRESSION" == "zstd" ]]; then
    TAR_FILE="$WORKDIR/${NAME}.tar.zst"
    tar_cmd()         { tar --zstd -cf "$TAR_FILE" -C "$WORKDIR" "$MEASURED" 2>/dev/null; }
    tar_extract_cmd() { tar --zstd -xf "$TAR_FILE" -C "$TAR_EXTRACT_DIR" 2>/dev/null; }
else
    TAR_FILE="$WORKDIR/${NAME}.tgz"
    tar_cmd()         { tar czf "$TAR_FILE" -C "$WORKDIR" "$MEASURED" 2>/dev/null; }
    tar_extract_cmd() { tar xzf "$TAR_FILE" -C "$TAR_EXTRACT_DIR" 2>/dev/null; }
fi
marc_archive_cmd() { "$MARC" archive $MARC_EXTRA_ARGS "$MARC_FILE" "$DIR" 2>/dev/null; }
marc_extract_cmd() { "$MARC" extract "$MARC_FILE" -C "$DIR2" 2>/dev/null; }

# cleanup from previous run (never the cached clone)
rm -rf "$DIR2" "$TAR_EXTRACT_DIR" "$TAR_FILE" "$MARC_FILE"
[[ "$CORPUS" == "tree" ]] && rm -rf "$DIR"

# 1. Clone at pinned commit (or shallow HEAD if no commit specified).
# Reuse the clone when it already holds the pinned commit: a depth-1 fetch
# costs minutes on a large repo and does not repack identically twice.
log "=== $NAME ==="
have_commit() {
    [[ -d "$CLONE/.git" ]] || return 1
    [[ -z "$COMMIT" ]] && return 0
    git -C "$CLONE" rev-parse -q --verify "${COMMIT}^{commit}" >/dev/null 2>&1
}
if have_commit; then
    log "  using cached clone"
else
    log "  cloning..."
    rm -rf "$CLONE"
    if [[ -n "$COMMIT" ]]; then
        git init "$CLONE" >/dev/null 2>&1
        git -C "$CLONE" remote add origin "$REPO" 2>/dev/null
        git -C "$CLONE" fetch --depth 1 origin "$COMMIT" 2>/dev/null
        git -C "$CLONE" checkout FETCH_HEAD 2>/dev/null
    else
        git clone --depth 1 "$REPO" "$CLONE" 2>/dev/null
    fi
fi

# 1b. In tree mode, export the pinned tree into a clean directory. git archive
# stamps every entry with the commit date, so the export is byte-identical on
# every run, mtimes included — which a checkout is not, and which marc records
# per entry in its catalog.
if [[ "$CORPUS" == "tree" ]]; then
    log "  exporting source tree..."
    mkdir -p "$DIR"
    if ! git -C "$CLONE" archive "${COMMIT:-HEAD}" | tar -x -C "$DIR"; then
        echo "[compare_on_repo] git archive failed for $NAME" >&2
        exit 1
    fi
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

# 7. Cleanup. The clone stays: it is the cache. In tree mode the export is
# regenerated from it in seconds, so it goes.
rm -rf "$DIR2" "$TAR_EXTRACT_DIR" "$TAR_FILE" "$MARC_FILE"
[[ "$CORPUS" == "tree" ]] && rm -rf "$DIR"

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
