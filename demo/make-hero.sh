#!/bin/sh
# make-hero.sh — regenerate the README hero (demo/hero.svg) from REAL output.
#
# The hero is a proof receipt: it renders the actual result of
# `whittle compress demo/build.log` (the surviving ERROR lines, the omission
# marker, the token verdict) plus the repo's published bench aggregates. This
# script is the guarantee that the hero can never drift from the truth: it runs
# the real command and injects the real numbers. Run via `make hero`.
set -eu
cd "$(dirname "$0")/.."

out=$(mktemp) && err=$(mktemp)
trap 'rm -f "$out" "$err"' EXIT
go run ./cmd/whittle compress -stats demo/build.log >"$out" 2>"$err"

# The verdict, verbatim from -stats (e.g. tokens=4728->163).
stats=$(grep -o 'action=[^ ]* detected=[^ ]* strategy=[^ ]* tokens=[0-9]*->[0-9]*' "$err")
tin=$(printf '%s' "$stats" | sed 's/.*tokens=\([0-9]*\)->.*/\1/')
tout=$(printf '%s' "$stats" | sed 's/.*->\([0-9]*\).*/\1/')
pct=$(awk "BEGIN{printf \"%d\", (1 - $tout/$tin) * 100}") # floor: understate, never overstate

# The surviving lines, verbatim: first ERROR, the LARGEST omission marker, last ERROR.
err1=$(grep 'ERROR' "$out" | head -1)
err2=$(grep 'ERROR' "$out" | tail -1)
marker=$(grep 'lines omitted' "$out" | sort -t'[' -k2 -rn | head -1 | sed 's/^ *//')
[ -n "$err1" ] && [ -n "$err2" ] && [ -n "$marker" ] || { echo "make-hero: carve shape changed; update template" >&2; exit 1; }

# The corpus claims are fixed copy in the template — assert they still match the
# published bench report so the hero cannot silently outlive its evidence.
grep -q '22% tool-output reduction' bench/README.md || { echo "make-hero: 22% claim no longer in bench/README.md" >&2; exit 1; }
grep -q '5,000 verified multi-turn sessions' bench/README.md || { echo "make-hero: 5,000-session claim no longer in bench/README.md" >&2; exit 1; }

# esc XML-escapes; sedesc additionally escapes sed-replacement metacharacters
# (&, |, \) so injected text can never mangle the template substitution.
esc() { printf '%s' "$1" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g'; }
sedesc() { esc "$1" | sed 's/[\&|]/\\&/g'; }
ts1=$(sedesc "${err1%% *}"); msg1=$(sedesc "${err1#* }")
ts2=$(sedesc "${err2%% *}"); msg2=$(sedesc "${err2#* }")
tin_fmt=$(awk "BEGIN{n=$tin; s=\"\"; while (n>=1000){s=sprintf(\",%03d%s\",n%1000,s); n=int(n/1000)}; printf \"%d%s\",n,s}")

sed -e "s|{{TS1}}|$ts1|" -e "s|{{MSG1}}|$msg1|" \
    -e "s|{{TS2}}|$ts2|" -e "s|{{MSG2}}|$msg2|" \
    -e "s|{{MARKER}}|$(sedesc "$marker")|" \
    -e "s|{{STATSLINE}}|$(sedesc "whittle: $stats")|" \
    -e "s|{{TIN}}|$tin_fmt|" -e "s|{{TOUT}}|$tout|" -e "s|{{PCT}}|$pct|" \
    demo/hero.template.svg > demo/hero.svg
echo "hero: $tin -> $tout tokens (-$pct%) -> demo/hero.svg"
