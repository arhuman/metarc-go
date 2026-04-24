#!/usr/bin/env bash
set -euo pipefail

# Usage: ./compare_on_repo.sh --name <name> --repo <repourl> [--mode log|test] [--compression zstd|gz] [--type size|time|legacy]
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
# Special case: --name header --repo header  → prints the table header.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
MARC="$SCRIPT_DIR/../bin/marc"

# --- helpers (same as run_bench.sh) ---

log() { :; }  # redefined after arg parsing

file_bytes() { wc -c < "$1" | tr -d ' '; }

fmt_bytes() {
    python3 -c "
b = $1
if   b >= 1073741824: print(f'{b/1073741824:.1f}G')
elif b >= 1048576:    print(f'{b/1048576:.1f}M')
elif b >= 1024:       print(f'{b/1024:.1f}K')
else:                 print(f'{b}B')
"
}

human() { du -sh "$1" 2>/dev/null | cut -f1; }

# --- parse args ---

NAME=""
REPO=""
MODE=""
COMPRESSION="zstd"
TYPE="legacy"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --name)        NAME="$2"; shift 2 ;;
        --repo)        REPO="$2"; shift 2 ;;
        --mode)        MODE="$2"; shift 2 ;;
        --compression) COMPRESSION="$2"; shift 2 ;;
        --type)        TYPE="$2"; shift 2 ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

if [[ -z "$NAME" || -z "$REPO" ]]; then
    echo "Usage: $0 --name <name> --repo <repourl> [--mode log|test] [--compression zstd|gz] [--type size|time|legacy]" >&2
    exit 1
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
    echo ""
    case "$TYPE" in
        size)
            echo "| Repo | Original size | Files | tar+${COMPRESSION} size | marc size | % size of tar |"
            echo "|------|---------------|-------|-------------------------|-----------|---------------|"
            ;;
        time)
            echo "| Repo | Files | tar+${COMPRESSION} arc | marc arc | tar+${COMPRESSION} ext | marc ext |"
            echo "|------|-------|------------------------|----------|-----------------------|----------|"
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

# cleanup from previous run
rm -rf "$DIR" "$DIR2" "$TAR_EXTRACT_DIR" "$TAR_FILE" "$MARC_FILE"

# 1. Shallow clone
log "=== $NAME ==="
log "  cloning..."
git clone --depth 1 "$REPO" "$DIR" 2>/dev/null

# 2. Size + file count
ORIG_SIZE=$(human "$DIR")
FILE_COUNT=$(find "$DIR" -type f | wc -l | tr -d ' ')

# 3. tar archive + extract
log "  tar+${COMPRESSION}..."
TAR_TIME=$( { time tar_cmd; } 2>&1 | grep real | awk '{print $2}' )
TAR_BYTES=$(file_bytes "$TAR_FILE")
TAR_SIZE=$(fmt_bytes "$TAR_BYTES")
mkdir "$TAR_EXTRACT_DIR"
TAR_EXTRACT_TIME=$( { time tar_extract_cmd; } 2>&1 | grep real | awk '{print $2}' )
rm -rf "$TAR_EXTRACT_DIR"

# 4. marc archive
log "  marc archive..."
METARC_TIME=$( { time "$MARC" archive "$MARC_FILE" "$DIR" 2>/dev/null; } 2>&1 | grep real | awk '{print $2}' )
MARC_BYTES=$(file_bytes "$MARC_FILE")
MARC_SIZE=$(fmt_bytes "$MARC_BYTES")

# 5. Round-trip verification (marc extract timed)
log "  verifying round-trip..."
mkdir "$DIR2"
MARC_EXTRACT_TIME=$( { time "$MARC" extract "$MARC_FILE" -C "$DIR2" 2>/dev/null; } 2>&1 | grep real | awk '{print $2}' )
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
                echo "| $NAME | ${FILE_COUNT} | ${TAR_TIME} | ${METARC_TIME} | ${TAR_EXTRACT_TIME} | ${MARC_EXTRACT_TIME} |"
                ;;
            *)
                echo "| $NAME | ${ORIG_SIZE} | ${FILE_COUNT} | ${TAR_TIME} | ${TAR_SIZE} | ${METARC_TIME} | ${MARC_SIZE} | ${RATIO} |"
                ;;
        esac
        ;;
esac
