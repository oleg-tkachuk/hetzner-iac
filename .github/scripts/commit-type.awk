# Which commit type a set of paths allows.
#
# Reads one path per line and prints the type the commit should have used, or
# nothing when the type it has is fine. `type` comes in with -v.
#
#   prose only            → docs, which releases nothing
#   contributor-only only → not feat/fix/perf; ci, test or chore
#
# feat, fix and perf make semantic-release cut a version and write notes, and
# a change no consumer of this repository receives should not.
#
# "Contributor-only" is deliberately narrow: .github, Go tests and the dotfile
# tool configs. The taskfiles and tools/ are NOT in it — somebody who checks
# out a tag gets those, so a new task is a real feat.
#
# Two callers, one file: the Commit types step in .github/workflows/ci.yaml
# runs it per commit of a pull request, and the commit-msg hook in
# lefthook.yml runs it over what is staged. It was inline in the workflow
# first, which meant the hook could only have been a second copy of it —
# and a rule with two copies is a rule that disagrees with itself.
#
# awk rather than grep: one pass, and no exit-code-as-answer, which is how the
# first version of this reported every commit as prose.

NF {
    n++

    if ($0 ~ /\.md$/ || $0 ~ /^docs\//) {
        prose++
    }

    if ($0 ~ /^\.github\// || $0 ~ /_test\.go$/ ||
        $0 ~ /^\.[a-z]+[a-z.]*\.(yaml|yml|json|toml)$/ || $0 ~ /^lefthook\.yml$/) {
        internal++
    }
}

END {
    if (n == 0) {
        exit
    }

    if (prose == n && type != "docs") {
        print "docs"
        exit
    }

    if (prose + internal == n && type ~ /^(feat|fix|perf)$/) {
        print "ci, test or chore"
        exit
    }
}
