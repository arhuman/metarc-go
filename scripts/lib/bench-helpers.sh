# Shared helpers for the bench scripts.
#
# Sourced by scripts/compare_on_repo.sh and scripts/tune_zstd.sh. Defines:
#   - byte/size formatting:      file_bytes, fmt_bytes, human
#   - timing primitives:         fmt_seconds, parse_seconds, prime_cache, flush_cache, time_median
#   - comparison helpers:        seconds_lt, pct_diff, pct_diff_seconds, pct_faster, speedup_label
#   - roundtrip + util:          verify_roundtrip, die
#
# All functions are pure (no globals written) except prime_cache, flush_cache,
# and verify_roundtrip, which touch the filesystem deliberately. flush_cache
# also requires sudo on macOS / Linux; see its docstring.

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
# In cold mode (BENCH_COLD=1), this is a no-op — flush_cache handles the
# inverse goal of evicting cache before each measurement.
prime_cache() {
    [[ "${BENCH_COLD:-0}" == "1" ]] && return 0
    local target="$1"
    if [[ -d "$target" ]]; then
        find "$target" -type f -print0 2>/dev/null \
            | xargs -0 cat > /dev/null 2>&1 || true
    elif [[ -f "$target" ]]; then
        cat "$target" > /dev/null 2>&1 || true
    fi
}

# flush_cache: evict the OS page cache so the next read goes to disk.
# Portable across macOS (`purge`) and Linux (`drop_caches`). Both require
# root, so the script tries unprivileged first and falls back to `sudo -n`
# (non-interactive). If that also fails, prints one warning and continues
# in degraded mode (warmer-than-fully-cold).
_BENCH_FLUSH_WARNED=0
_bench_flush_warn() {
    [[ "$_BENCH_FLUSH_WARNED" == "1" ]] && return
    _BENCH_FLUSH_WARNED=1
    echo "[bench] WARNING: cold-cache flush failed ($1); --cold results may be partially warm" >&2
}

flush_cache() {
    # Parent script may set BENCH_COLD_DEGRADED=1 after detecting sudo is
    # unavailable, to suppress the redundant per-iteration warning that
    # would otherwise fire from each time_median subshell.
    [[ "${BENCH_COLD_DEGRADED:-0}" == "1" ]] && return 0

    case "$(uname -s)" in
        Darwin)
            if ! command -v purge >/dev/null 2>&1; then
                _bench_flush_warn "purge not found"
                return 0
            fi
            # purge typically needs root on modern macOS; try unprivileged
            # first in case the user's setup permits it, then sudo -n.
            purge 2>/dev/null && return 0
            sudo -n purge 2>/dev/null && return 0
            _bench_flush_warn "sudo -n purge denied (run interactively first or configure passwordless sudo)"
            ;;
        Linux)
            sync 2>/dev/null
            sudo -n sh -c 'echo 3 > /proc/sys/vm/drop_caches' 2>/dev/null && return 0
            _bench_flush_warn "sudo -n drop_caches denied"
            ;;
        *)
            _bench_flush_warn "unsupported OS $(uname -s)"
            ;;
    esac
    return 0
}

# time_median <prime_target> <prep_cmd> <timed_cmd> [n]
#
# Runs <timed_cmd> n=3 times (default) and echoes the median wall-clock as
# "0mSS.SSSs". Before each run, calls <prep_cmd> (typically rm/mkdir of
# the output).
#
# Default mode (BENCH_COLD unset / 0): warm cache.
#   - 1 untimed warmup run (eats dyld/JIT/code-sign cost).
#   - prime_cache primes <prime_target> before warmup AND each timed run.
#   - Useful for low-variance regression tracking; the OS-cache state is
#     held constant across iterations.
#
# Cold mode (BENCH_COLD=1): evict cache before each timed iteration.
#   - No warmup (would defeat the cold-state purpose).
#   - flush_cache instead of prime_cache.
#   - Reflects realistic I/O-bound wall-clock for tools that do disk reads;
#     CPU-bound tools see little difference vs warm.
#
# <prep_cmd> and <timed_cmd> are eval'd, so they can be function names or
# full command strings. Use single-quoted args so $VARS expand at eval time.
time_median() {
    local prime_target="$1" prep_cmd="$2" timed_cmd="$3"
    local n="${4:-3}"
    local i start end dur cold="${BENCH_COLD:-0}"

    if [[ "$cold" != "1" ]]; then
        # Warm-cache mode: 1 untimed warmup, then n timed runs after priming.
        prime_cache "$prime_target"
        eval "$prep_cmd" >/dev/null 2>&1 || true
        eval "$timed_cmd" >/dev/null 2>&1 || true
    fi

    local times=()
    for ((i = 0; i < n; i++)); do
        if [[ "$cold" == "1" ]]; then
            flush_cache
        else
            prime_cache "$prime_target"
        fi
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

# pct_faster "<reference time>" "<candidate time>" -> "+/-N.N%"
# Positive when candidate is FASTER than reference (took less time);
# negative when slower. Computed as (reference - candidate) / reference.
# Both args in "0mSS.SSSs" form.
#
# Examples (reference is tar+zstd, candidate is marc):
#   tar 16s, marc  4s  →  +75.0%   (marc 75% faster — took 25% of the time)
#   tar  4s, marc  4s  →    0.0%
#   tar  4s, marc  8s  →  -100.0%  (marc 100% slower — took 2× the time)
pct_faster() {
    local r c
    r=$(parse_seconds "$1")
    c=$(parse_seconds "$2")
    awk -v r="$r" -v c="$c" 'BEGIN{
        if (r == 0) { print "n/a"; exit }
        printf "%+.1f%%\n", (r - c) / r * 100
    }'
}

# speedup_label "<reference time>" "<candidate time>" -> "1.6× faster" / "1.6× slower" / "1×"
#
# Multiplicative form of the candidate-vs-reference comparison. Always
# emits a positive multiplier with an explicit "faster" or "slower"
# suffix, which reads better in tables than a percentage that can be
# negative. Both args in "0mSS.SSSs" form.
#
# Examples (reference is tar+zstd, candidate is marc):
#   tar 16s, marc  4s  →  "4× faster"
#   tar  4s, marc  4s  →  "1×"
#   tar  4s, marc  8s  →  "2× slower"
speedup_label() {
    local r c
    r=$(parse_seconds "$1")
    c=$(parse_seconds "$2")
    awk -v r="$r" -v c="$c" 'BEGIN{
        if (r == 0 || c == 0) { print "n/a"; exit }
        if (r > c)      { printf "%.2g× faster\n", r / c }
        else if (c > r) { printf "%.2g× slower\n", c / r }
        else            { print "1×" }
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
