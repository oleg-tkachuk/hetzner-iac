# One awk program, selected with -v op=<name>, for the line-oriented parsing
# this repository needs.
#
# It replaces pipelines. `df --output=avail -BM / | tail -1 | tr -dc '0-9'` is
# three processes and three chances to return the wrong thing quietly; the
# `avail-mb` rule below is one. The same argument applies to every `grep | grep
# -c` pair: awk counts while it matches.
#
# What it deliberately does NOT do is read YAML. The four places that used to
# grep a topology now call `tools/topology get`, which asks the parser the
# cluster is built from. An awk program with an indentation state machine would
# fix the breakage those greps had and still be a hand-rolled YAML reader —
# wrong on a quoted value, an anchor, or a nested key of the same name.
#
# Every op is covered by lib_test.go beside it. An awk program nobody tests is
# a shell pipeline with extra steps.

# Fail loudly on a missing or unknown op rather than silently printing nothing,
# which is the failure mode the pipelines had.
BEGIN {
    if (op == "") {
        print "awk/lib.awk: -v op=<name> is required" > "/dev/stderr"
        exit 2
    }

    known["avail-mb"] = 1
    known["count-rows"] = 1
    known["any-row"] = 1
    known["cache-writers"] = 1
    known["reverse-words"] = 1
    known["staged-binaries"] = 1

    if (!(op in known)) {
        print "awk/lib.awk: unknown op " op > "/dev/stderr"
        exit 2
    }
}

# staged-binaries: the paths git considers binary in `git diff --cached
# --numstat`, and a non-zero exit if there are any.
#
# git's own detection rather than `file -b | grep -qE 'ELF|Mach-O'`, which is
# what this replaced, for three measured reasons:
#
#   - That pattern knew two formats. It let a PNG, a gzip, an ar archive and a
#     wasm module through — checked, all four.
#   - If `file` produced no output, grep saw an empty input, answered "no
#     match", and the binary passed silently. A guard whose failure mode is
#     "allow" is not a guard.
#   - numstat prints `-` for added and removed lines on anything git calls
#     binary, whatever the format, with no `file` on the machine at all.
#
# Two Go binaries reached the working tree twice in this repository, and a
# 52 MB one was committed and pushed before any of this existed.
op == "staged-binaries" && $1 == "-" && $2 == "-" {
    # `allow` is a colon-separated list of paths that are binary on purpose.
    # Empty today, because this repository tracks none.
    if (allowed($3)) next

    binaries[++found] = $3
    next
}

# allowed reports whether a path is in the -v allow=<a:b:c> list.
function allowed(path,    n, i, parts) {
    if (allow == "") return 0

    n = split(allow, parts, ":")

    for (i = 1; i <= n; i++)
        if (parts[i] == path) return 1

    return 0
}

# reverse-words: the whitespace-separated tokens of the input, last first, one
# per line.
#
# Tokens rather than lines, so a list that arrives on one line and a list that
# arrives on several both work — the layer list is one space-separated string
# in the Taskfile, and piping it through `tr` first would be the pipeline this
# op exists to remove. A token containing a space is therefore not expressible,
# which is true of every layer name by construction.
#
# It replaces a `tac` / `tail -r` branch: `tac` is GNU, `tail -r` is BSD, a
# machine has one or the other, and the shell that picked between them was
# twelve lines including the error for a machine with neither. awk has neither
# problem — it is the same program on both.
#
# Used for destroying layers, where the order has to be the apply order
# backwards: ingress cannot go before the CNI it depends on.
op == "reverse-words" {
    for (i = 1; i <= NF; i++) words[++count] = $i

    next
}

# avail-mb: the available megabytes from `df --output=avail -BM <path>`.
#
# Reads the first data row and strips the unit suffix df prints. Exits on the
# first row so a multi-filesystem df cannot silently contribute a second
# number.
op == "avail-mb" && NR > 1 {
    gsub(/[^0-9]/, "", $1)
    print $1 + 0
    found = 1
    exit
}

# count-rows: how many non-blank lines arrived.
#
# Replaces `grep -c .`, which exits 1 when the answer is zero and so needs a
# `|| true` under `set -e`. Zero is an answer here, not a failure.
op == "count-rows" && NF > 0 { rows++ }

# any-row: exit 0 if at least one non-blank line arrived, 1 if none.
#
# Replaces `grep -q .` with the same contract and without the exit-code
# surprise: grep's 1 means "no match", which is indistinguishable from an
# error when a pipeline swallows stderr.
op == "any-row" && NF > 0 { rows++ }

# cache-writers: the workflow lines that can make a job write the shared Go
# cache, with the file and line number.
#
# One pass instead of `grep -rnE ... || true` piped into `grep -c . || true`.
# The pattern is anchored so this rule cannot match the string inside its own
# source, which the shell version did on its first attempt.
op == "cache-writers" && /^[[:space:]]+cache-mode:.*save/ {
    printf "%s:%d:%s\n", FILENAME, FNR, $0
    rows++
}

END {
    if (op == "avail-mb") {
        if (!found) {
            print "awk/lib.awk: df produced no data row" > "/dev/stderr"
            exit 1
        }

        exit 0
    }

    if (op == "count-rows" || op == "cache-writers") {
        if (op == "count-rows") print rows + 0
        else print "count=" rows + 0

        exit 0
    }

    if (op == "any-row") {
        exit rows > 0 ? 0 : 1
    }

    if (op == "staged-binaries") {
        if (found == 0) exit 0

        print "ERROR: git considers these staged files binary:" > "/dev/stderr"

        for (i = 1; i <= found; i++) print "  " binaries[i] > "/dev/stderr"

        print "" > "/dev/stderr"
        print "A compiled binary is almost always `go build ./...` leaving one in the tree." > "/dev/stderr"
        print "Remove it and check .gitignore lists every tools/ and layers/ name." > "/dev/stderr"
        print "If the file genuinely belongs in the repository, allow it in lefthook.yml." > "/dev/stderr"

        exit 1
    }

    if (op == "reverse-words") {
        for (i = count; i >= 1; i--) print words[i]

        exit 0
    }
}
