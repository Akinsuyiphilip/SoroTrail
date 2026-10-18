#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# merge_coverage.sh — merge Go coverage profiles from separate test runs into
# one profile, summing the hit counts of statement ranges that appear in more
# than one input.
#
# CI runs the unit suite and the integration-tagged suite as separate jobs
# (each against Postgres 15 and 16), so a package compiled into both test
# binaries — internal/store, internal/api, internal/ingester — appears in
# several profiles. Letting one file overwrite another hides the integration-
# only paths; summing the counts keeps the union of everything exercised.
#
# Usage:
#   scripts/merge_coverage.sh OUT IN IN...
#
# At least two inputs are required: one profile is not a merge, and a lone
# file here usually means a glob matched a single artifact after a job failed
# to upload — silently passing that through would under-report coverage.
#
# Every input must use the same -covermode: counts from `set`, `count` and
# `atomic` profiles mean different things, so a mix fails loudly instead of
# producing a plausible-looking wrong number. OUT is overwritten.
#
# Exit code: 0 on success, 2 on bad usage, 1 on malformed or mixed input.
# ---------------------------------------------------------------------------
set -euo pipefail

if [ "$#" -lt 3 ]; then
    echo "usage: $0 OUT IN..." >&2
    exit 2
fi

OUT="$1"
shift

awk '
FNR == 1 {
    # The mode line is the first line of every Go coverage profile and the
    # only line that is not a statement record, so it doubles as the format
    # check: a file that fails here is not a coverage profile at all.
    if ($0 !~ /^mode:/) {
        printf "merge_coverage: %s: first line is not a mode declaration; not a coverage profile\n", FILENAME > "/dev/stderr"
        failed = 1
        exit 1
    }
    modes[$0] = 1
    next
}
{
    # Key on file:range plus statement count so the same range from two
    # runs lands on one entry; hit counts add up.
    counts[$1 " " $2] += $3
}
END {
    if (failed) {
        exit 1
    }
    if (length(modes) != 1) {
        for (m in modes) {
            printf "merge_coverage: found mixed covermodes, starting with %s\n", m > "/dev/stderr"
        }
        exit 1
    }
    for (m in modes) {
        print m
    }
    for (k in counts) {
        print k, counts[k]
    }
}
' "$@" > "$OUT"
