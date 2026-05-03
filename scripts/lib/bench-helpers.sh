# Shared helpers for the bench scripts.
#
# Sourced by scripts/compare_on_repo.sh and scripts/tune_zstd.sh. Defines:
#   - byte/size formatting:      file_bytes, fmt_bytes, human
#   - timing primitives:         fmt_seconds, parse_seconds, prime_cache, time_median
#   - comparison helpers:        seconds_lt, pct_diff, pct_diff_seconds
#   - roundtrip + util:          verify_roundtrip, die
#
# All functions are pure (no globals written) except prime_cache and
# verify_roundtrip which touch the filesystem deliberately.

# ---------------------------------------------------------------------------
# Size formatting
# ---------------------------------------------------------------------------

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

# ---------------------------------------------------------------------------
# Timing primitives
# ---------------------------------------------------------------------------

# fmt_seconds <float-seconds> -> "0mSS.SSSs" (matches `time` builtin output)
fmt_seconds() {
    awk -v s="$1" 'BEGIN {
        if (s < 0) s = 0
        m = int(s / 60)
        rem = s - 60 * m
        printf "%dm%.3fs\n", m, rem
    }'
}

# parse_seconds "<0mSS.SSSs>" -> echoes float seconds (inverse of fmt_seconds).
# Avoids gawk's match(re,arr) (not in BSD awk on macOS) by using bash
# parameter expansion to split on the "m" / "s" markers.
parse_seconds() {
    local t="$1" mins secs
    t="${t%s}"          # strip trailing "s"   -> "0m0.042"
    mins="${t%%m*}"     # everything before m  -> "0"
    secs="${t#*m}"      # everything after m   -> "0.042"
    [[ -z "$mins" || -z "$secs" ]] && { echo "0"; return; }
    awk -v m="$mins" -v s="$secs" 'BEGIN{ printf "%.6f\n", m * 60 + s }'
}

# prime_cache <dir|file>: read every file under the path so the OS page cache
# is warm before we measure. Errors are ignored (best-effort warmup).
prime_cache() {
    local target="$1"
    if [[ -d "$target" ]]; then
        find "$target" -type f -print0 2>/dev/null \
            | xargs -0 cat > /dev/null 2>&1 || true
    elif [[ -f "$target" ]]; then
        cat "$target" > /dev/null 2>&1 || true
    fi
}

# time_median <prime_target> <prep_cmd> <timed_cmd> [n]
#
# Runs <timed_cmd> n=3 times (default) after one untimed warmup. Before each
# run, calls <prep_cmd> (typically rm/mkdir of the output) and primes the
# page cache for <prime_target>. Echoes the median wall-clock as "0mSS.SSSs".
#
# <prep_cmd> and <timed_cmd> are eval'd, so they can be function names or
# full command strings. Use single-quoted args so $VARS expand at eval time.
time_median() {
    local prime_target="$1" prep_cmd="$2" timed_cmd="$3"
    local n="${4:-3}"
    local i start end dur

    # Warmup: prep + run untimed. Eats dyld/JIT/code-sign cost.
    prime_cache "$prime_target"
    eval "$prep_cmd" >/dev/null 2>&1 || true
    eval "$timed_cmd" >/dev/null 2>&1 || true

    local times=()
    for ((i = 0; i < n; i++)); do
        prime_cache "$prime_target"
        eval "$prep_cmd" >/dev/null 2>&1 || true
        start=$EPOCHREALTIME
        eval "$timed_cmd" >/dev/null 2>&1
        end=$EPOCHREALTIME
        dur=$(awk -v s="$start" -v e="$end" 'BEGIN{printf "%.3f", e - s}')
        times+=("$dur")
    done

    # Median = middle element after numeric sort.
    local sorted=()
    while IFS= read -r line; do sorted+=("$line"); done \
        < <(printf '%s\n' "${times[@]}" | sort -n)
    local mid=$(( ${#sorted[@]} / 2 ))
    fmt_seconds "${sorted[$mid]}"
}

# ---------------------------------------------------------------------------
# Comparison helpers
# ---------------------------------------------------------------------------

# seconds_lt <a> <b>: returns 0 (true) if a < b, both in "0mSS.SSSs" form.
seconds_lt() {
    local a b
    a=$(parse_seconds "$1")
    b=$(parse_seconds "$2")
    awk -v a="$a" -v b="$b" 'BEGIN{ exit (a < b) ? 0 : 1 }'
}

# pct_diff <base_bytes> <new_bytes> -> "+/-N.NN%"
# Negative means new is smaller than base.
pct_diff() {
    awk -v b="$1" -v n="$2" 'BEGIN{
        if (b == 0) { print "n/a"; exit }
        printf "%+.2f%%\n", (n - b) / b * 100
    }'
}

# pct_diff_seconds "<base time>" "<new time>" -> "+/-N.NN%"
# Positive means new is slower than base.
pct_diff_seconds() {
    local b n
    b=$(parse_seconds "$1")
    n=$(parse_seconds "$2")
    awk -v b="$b" -v n="$n" 'BEGIN{
        if (b == 0) { print "n/a"; exit }
        printf "%+.1f%%\n", (n - b) / b * 100
    }'
}

# ---------------------------------------------------------------------------
# Round-trip + util
# ---------------------------------------------------------------------------

# verify_roundtrip <archive_path> <src_dir>
# Extracts the archive to a temp dir and `diff -rq`s against src_dir.
# Returns 0 on byte-identical match, 1 otherwise. Caller supplies $MARC.
verify_roundtrip() {
    local archive="$1" src="$2"
    local extract_dir
    extract_dir=$(mktemp -d -t marc-verify.XXXXXX)
    if ! "$MARC" extract "$archive" -C "$extract_dir" 2>/dev/null; then
        rm -rf "$extract_dir"
        return 1
    fi
    # marc extract puts the source dir's basename inside extract_dir.
    local extracted
    extracted="$extract_dir/$(basename "$src")"
    if [[ ! -d "$extracted" ]]; then
        # Fall back to scanning the extract dir.
        extracted="$extract_dir"
    fi
    local rc=0
    diff -rq "$src" "$extracted" >/dev/null 2>&1 || rc=1
    rm -rf "$extract_dir"
    return "$rc"
}

# die <msg>: log to stderr and exit 1.
die() {
    echo "ERROR: $*" >&2
    exit 1
}
