#!/bin/sh
# tools/verify.sh - offline verification entry point for go-org (local + CI).
#
# Usage: sh tools/verify.sh [quick|full]   (default: full)
#
# Stages run in a fixed order; each prints start/ok/FAILED. On failure the
# original exit code is preserved and the first difference is shown. Only
# temporary directories created by this run are ever cleaned up. Nothing is
# written to the repository and goldens are never auto-accepted.
# See VERIFYING.md for details.

set -u

MODE="${1:-full}"
case "$MODE" in
quick | full) ;;
-h | --help)
	echo "usage: sh tools/verify.sh [quick|full]"
	exit 0
	;;
*)
	echo "usage: sh tools/verify.sh [quick|full]" >&2
	exit 2
	;;
esac

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$REPO_ROOT" || exit 1

# Deterministic, offline environment. No network, no toolchain downloads,
# no locale/timezone dependent output.
export LC_ALL=C
export TZ=UTC
export GOTOOLCHAIN=local
export GOPROXY=off
export GOFLAGS=-mod=readonly

VERIFY_TMP=$(mktemp -d "${TMPDIR:-/tmp}/go-org-verify.XXXXXX") || exit 1
KEEP_TMP=0
cleanup() {
	# Only ever removes the temporary directory created by this run.
	if [ "$KEEP_TMP" -eq 0 ]; then
		rm -rf "$VERIFY_TMP"
	else
		echo "verify: failure artifacts kept in $VERIFY_TMP" >&2
	fi
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 131' QUIT
trap 'exit 143' TERM

STAGE=init

stage() {
	STAGE="$1"
	printf '==> [%s] start\n' "$STAGE"
}

ok() {
	printf '==> [%s] ok\n' "$STAGE"
}

fail() {
	rc="$1"
	shift
	printf '==> [%s] FAILED (exit %s): %s\n' "$STAGE" "$rc" "$*" >&2
	KEEP_TMP=1
	exit "$rc"
}

# run CMD... - run a stage command, capture output, dump it on failure.
run() {
	log="$VERIFY_TMP/$STAGE.log"
	"$@" >"$log" 2>&1
	rc=$?
	if [ "$rc" -ne 0 ]; then
		cat "$log" >&2
		fail "$rc" "command failed: $*"
	fi
}

sha256_check() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum -c "$1"
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 -c "$1"
	else
		return 127
	fi
}

# --- stage: env -------------------------------------------------------------
stage env
command -v go >/dev/null 2>&1 || fail 127 "go not found in PATH"
NEED_GO=$(sed -n 's/^go //p' go.mod | head -1)
HAVE_GO=$(go version | sed -n 's/.*go\([0-9][0-9.]*\).*/\1/p')
awk -v need="$NEED_GO" -v have="$HAVE_GO" 'BEGIN {
	split(need, a, "."); split(have, b, ".")
	for (i = 1; i <= 3; i++) {
		n = (a[i] == "" ? 0 : a[i]) + 0; h = (b[i] == "" ? 0 : b[i]) + 0
		if (h > n) exit 0; if (h < n) exit 1
	}
	exit 0
}' || fail 1 "go >= $NEED_GO required, found $HAVE_GO"
echo "go $HAVE_GO (need >= $NEED_GO), mode $MODE"
mkdir -p "$VERIFY_TMP/bin"
run go build -o "$VERIFY_TMP/bin/orgcheck" ./tools/orgcheck
run go build -o "$VERIFY_TMP/bin/fuzzsmoke" ./tools/fuzzsmoke
run go build -o "$VERIFY_TMP/bin/go-org" .
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
	git status --porcelain >"$VERIFY_TMP/workspace.before"
fi
ok

# --- stage: fixtures --------------------------------------------------------
# Raw-byte hash check of the fixture manifest before ANY parsing happens, so
# no tool can normalize a fixture first and produce a false pass.
stage fixtures
run sha256_check tools/fixtures/manifest.sha256
grep -q "$(printf '\r')" tools/fixtures/crlf.org ||
	fail 1 "tools/fixtures/crlf.org lost its CRLF line endings"
echo "manifest ok: $(grep -c ' ' tools/fixtures/manifest.sha256) entries"
ok

# --- stage: format ----------------------------------------------------------
stage format
UNFORMATTED=$(gofmt -l main.go org blorg tools etc 2>&1) || fail 1 "gofmt failed: $UNFORMATTED"
if [ -n "$UNFORMATTED" ]; then
	echo "$UNFORMATTED" >&2
	fail 1 "gofmt needed (first file: $(echo "$UNFORMATTED" | head -1))"
fi
ok

# --- stage: vet -------------------------------------------------------------
stage vet
run go vet ./...
ok

# --- stage: parser ----------------------------------------------------------
stage parser
if [ "$MODE" = full ]; then
	run go test -race -count=1 ./org/
else
	run go test -count=1 ./org/
fi
ok

# --- stage: html-writer -----------------------------------------------------
# Read-only golden comparison: rendered HTML is compared against committed
# goldens as normalized semantic nodes. Drift prints a minimal diff; goldens
# are never overwritten.
stage html-writer
run "$VERIFY_TMP/bin/orgcheck" golden-html tools/fixtures
ok

# --- stage: org-writer ------------------------------------------------------
# Read-only golden comparison of Org writer output, byte for byte.
stage org-writer
run "$VERIFY_TMP/bin/orgcheck" golden-org tools/fixtures
ok

# --- stage: struct ----------------------------------------------------------
# parse -> Org write -> re-parse structural comparison: headline levels, link
# targets, footnote references, code block languages, list structure.
stage struct
run "$VERIFY_TMP/bin/orgcheck" struct tools/fixtures
ok

# --- stage: blorg -----------------------------------------------------------
# Build a temporary content site (never the repo) and verify the output file
# set and relative links.
stage blorg
if [ "$MODE" = full ]; then
	run go test -race -count=1 ./blorg/
else
	run go test -count=1 ./blorg/
fi
SITE="$VERIFY_TMP/blorg-site"
mkdir -p "$SITE"
cp blorg/testdata/blorg.org "$SITE/blorg.org"
cp -R blorg/testdata/content "$SITE/content"
run sh -c "cd '$SITE' && '$VERIFY_TMP/bin/go-org' blorg build"
(
	cd blorg/testdata/public && find . -type f | sort
) >"$VERIFY_TMP/blorg.expected"
(
	cd "$SITE/public" && find . -type f | sort
) >"$VERIFY_TMP/blorg.actual"
if ! diff -u "$VERIFY_TMP/blorg.expected" "$VERIFY_TMP/blorg.actual" >&2; then
	fail 1 "blorg output file set drift (see diff above)"
fi
if ! diff -r blorg/testdata/public "$SITE/public" >"$VERIFY_TMP/blorg.diff" 2>&1; then
	head -40 "$VERIFY_TMP/blorg.diff" >&2
	fail 1 "blorg output content drift (first difference above)"
fi
grep -q 'href="about.html"' "$SITE/public/index.html" ||
	fail 1 "relative link href=\"about.html\" missing from blorg index.html"
echo "blorg build ok: $(wc -l <"$VERIFY_TMP/blorg.actual" | tr -d ' ') files, relative links ok"
ok

# --- stage: fuzz ------------------------------------------------------------
# Deterministic fuzz smoke over a fixed corpus with a fixed seed. Failure
# inputs are written to a temporary artifact with a printed replay command.
stage fuzz
if [ "$MODE" = full ]; then
	FUZZ_N=3000
else
	FUZZ_N=300
fi
run "$VERIFY_TMP/bin/fuzzsmoke" -seed 1060 -n "$FUZZ_N" \
	-artifacts "$VERIFY_TMP/fuzz-artifacts" org/testdata tools/fixtures
cat "$VERIFY_TMP/fuzz.log"
ok

# --- stage: cleanliness -----------------------------------------------------
# Workspace state and fixture hashes must be identical to before the run.
stage cleanliness
if [ -f "$VERIFY_TMP/workspace.before" ]; then
	git status --porcelain >"$VERIFY_TMP/workspace.after"
	if ! diff -u "$VERIFY_TMP/workspace.before" "$VERIFY_TMP/workspace.after" >&2; then
		fail 1 "workspace changed during verification (see diff above)"
	fi
fi
run sha256_check tools/fixtures/manifest.sha256
echo "workspace clean, fixture hashes unchanged"
ok

printf 'verify %s: all stages ok\n' "$MODE"
