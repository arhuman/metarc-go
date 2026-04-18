#!/usr/bin/env bash
set -euo pipefail

# Usage: ./compare_on_repo.sh --name <name> --repo <repourl> [--mode log|test]
#
# Clones <repourl> into /tmp/<name>, archives with tar+gz and marc,
# extracts marc into /tmp/<name>2, compares, and prints one markdown
# table row with the same columns as run_bench.sh.
#
# Modes:
#   (default)    print the markdown table row only
#   --mode log   print only the log/progress output
#   --mode test  print "true" or "false" (round-trip success)
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
elif b >= 1048576:    print(f'{b/1048576:.0f}M')
elif b >= 1024:       print(f'{b/1024:.0f}K')
else:                 print(f'{b}B')
"
}

human() { du -sh "$1" 2>/dev/null | cut -f1; }

# --- parse args ---

NAME=""
REPO=""
MODE=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --name) NAME="$2"; shift 2 ;;
        --repo) REPO="$2"; shift 2 ;;
        --mode) MODE="$2"; shift 2 ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

if [[ -z "$NAME" || -z "$REPO" ]]; then
    echo "Usage: $0 --name <name> --repo <repourl> [--mode log|test]" >&2
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
    echo "| Repo | Original size | Files | tgz compression | tgz size | marc compression | marc size | % size of tgz |"
    echo "|------|---------------|-------|-------------|-------------|------------|-----------|------------------|"
    exit 0
fi

# --- work in /tmp ---

WORKDIR="/tmp"
DIR="$WORKDIR/$NAME"
DIR2="$WORKDIR/${NAME}2"
TGZ="$WORKDIR/${NAME}.tgz"
MARC_FILE="$WORKDIR/${NAME}.marc"

# cleanup from previous run
rm -rf "$DIR" "$DIR2" "$TGZ" "$MARC_FILE"

# 1. Shallow clone
log "=== $NAME ==="
log "  cloning..."
git clone --depth 1 "$REPO" "$DIR" 2>/dev/null

# 2. Size + file count
ORIG_SIZE=$(human "$DIR")
FILE_COUNT=$(find "$DIR" -type f | wc -l | tr -d ' ')

# 3. tar+gz
log "  tar+gz..."
TAR_TIME=$( { time tar czf "$TGZ" -C "$WORKDIR" "$NAME" 2>/dev/null; } 2>&1 | grep real | awk '{print $2}' )
TGZ_BYTES=$(file_bytes "$TGZ")
TGZ_SIZE=$(fmt_bytes "$TGZ_BYTES")

# 4. marc archive
log "  marc archive..."
METARC_TIME=$( { time "$MARC" archive "$MARC_FILE" "$DIR" 2>/dev/null; } 2>&1 | grep real | awk '{print $2}' )
MARC_BYTES=$(file_bytes "$MARC_FILE")
MARC_SIZE=$(fmt_bytes "$MARC_BYTES")

# 5. Round-trip verification
log "  verifying round-trip..."
mkdir "$DIR2"
"$MARC" extract "$MARC_FILE" -C "$DIR2" 2>/dev/null
if diff -rq "$DIR" "$DIR2" > /dev/null 2>&1; then
    ROUNDTRIP="OK"
else
    ROUNDTRIP="FAIL"
    log "  ROUND-TRIP FAILED for $NAME"
fi

# 6. Ratio
RATIO=$(python3 -c "print(f'{$MARC_BYTES/$TGZ_BYTES*100:.1f}%')")

# 7. Cleanup
rm -rf "$DIR" "$DIR2" "$TGZ" "$MARC_FILE"

# 8. Output based on mode
case "$MODE" in
    test)
        [[ "$ROUNDTRIP" == "OK" ]]
        ;;
    log)
        log "  done: tar=${TGZ_SIZE} marc=${MARC_SIZE} ratio=${RATIO} round-trip=${ROUNDTRIP}"
        ;;
    *)
        echo "| $NAME | ${ORIG_SIZE} | ${FILE_COUNT} | ${TAR_TIME} | ${TGZ_SIZE} | ${METARC_TIME} | ${MARC_SIZE} | ${RATIO} |"
        ;;
esac
