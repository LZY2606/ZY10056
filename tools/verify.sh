#!/bin/sh
# verify.sh - offline verification entry point for go-org (local + CI).
#
# Usage: sh tools/verify.sh [quick|full]   (default: full)
#
# Runs a fixed sequence of stages: go version + fixture manifest pre-checks,
# format, vet, tests, HTML/Org golden read-only comparison, parse-write-parse
# structural check, blorg build in a temporary site, bounded fuzz smoke and a
# final workspace cleanliness check. See VERIFYING.md for details.
set -u

# --- deterministic, offline environment -------------------------------------
export LC_ALL=C
export TZ=UTC
export GOPROXY=off
export GOFLAGS=-mod=readonly
export GO_ORG_FUZZ_SEED=20240517

REPO_ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$REPO_ROOT" || exit 1

MODE="${1:-full}"
case "$MODE" in
quick)
	FUZZ_BUDGET=60
	TEST_PKGS="./org/..."
	;;
full)
	FUZZ_BUDGET=400
	TEST_PKGS="./..."
	;;
*)
	echo "usage: sh tools/verify.sh [quick|full]" >&2
	exit 2
	;;
esac

# --- temp dir lifecycle: only dirs created by this run are removed ----------
VERIFY_TMP=$(mktemp -d "${TMPDIR:-/tmp}/go-org-verify.XXXXXX") || exit 1
KEEP_TMP=0
cleanup() {
	rc=$?
	if [ "$KEEP_TMP" -eq 0 ]; then
		rm -rf "$VERIFY_TMP"
	else
		echo "[verify] kept temp dir for inspection: $VERIFY_TMP" >&2
	fi
	exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

export GO_ORG_FUZZ_ARTIFACT_DIR="$VERIFY_TMP/fuzz-artifacts"

# Snapshot workspace state so the final stage can prove verification did not
# modify the workspace (the checkout itself may legitimately be dirty).
git status --porcelain >"$VERIFY_TMP/git-status-before" 2>/dev/null ||
	echo "UNAVAILABLE" >"$VERIFY_TMP/git-status-before"

# --- stage runner ------------------------------------------------------------
STAGE_LOG=""
run_stage() {
	name=$1
	shift
	STAGE_LOG="$VERIFY_TMP/stage-$name.log"
	printf '[verify] ==> %s\n' "$name"
	start=$(date +%s)
	if "$@" >"$STAGE_LOG" 2>&1; then
		end=$(date +%s)
		printf '[verify] ok: %s (%ss)\n' "$name" $((end - start))
		return 0
	else
		rc=$?
	fi
	printf '[verify] FAIL: %s (exit %d)\n' "$name" "$rc" >&2
	first=$(grep -n -m1 -E '^(---|\+\+\+|@@|diff |FAIL)|\.go:[0-9]+:[0-9]+|\.org:[0-9]+' "$STAGE_LOG" 2>/dev/null)
	if [ -n "$first" ]; then
		printf '[verify] first difference: %s\n' "$first" >&2
	fi
	sed -n '1,40p' "$STAGE_LOG" >&2
	lines=$(wc -l <"$STAGE_LOG" | tr -d ' ')
	if [ "$lines" -gt 40 ]; then
		printf '[verify] ... (%s more lines in %s)\n' $((lines - 40)) "$STAGE_LOG" >&2
	fi
	KEEP_TMP=1
	exit "$rc"
}

sha256_tool() {
	if command -v sha256sum >/dev/null 2>&1; then
		echo "sha256sum -c"
	elif command -v shasum >/dev/null 2>&1; then
		echo "shasum -a 256 -c"
	else
		echo ""
	fi
}

# --- stages ------------------------------------------------------------------

stage_go_version() {
	command -v go >/dev/null 2>&1 || {
		echo "go toolchain not found in PATH"
		return 1
	}
	required=$(awk '$1 == "go" { print $2; exit }' go.mod)
	[ -n "$required" ] || {
		echo "could not parse go directive from go.mod"
		return 1
	}
	have=$(go version | awk '{ print $3 }' | sed 's/^go//')
	echo "go.mod requires go >= $required, found $have"
	awk -v have="$have" -v req="$required" 'BEGIN {
		n = split(have, h, "."); m = split(req, r, ".");
		for (i = 1; i <= 3; i++) {
			hv = (i <= n ? h[i] : 0) + 0; rv = (i <= m ? r[i] : 0) + 0;
			if (hv > rv) exit 0; if (hv < rv) exit 1;
		}
		exit 0
	}' || {
		echo "go $have is older than required go $required"
		return 1
	}
}

check_fixture_manifest() {
	tool=$(sha256_tool)
	[ -n "$tool" ] || {
		echo "neither sha256sum nor shasum found"
		return 1
	}
	# Verify raw bytes before any parsing happens.
	$tool tools/fixtures.manifest
}

stage_fixture_manifest() {
	check_fixture_manifest
}

stage_format() {
	unformatted=$(find . -name '*.go' -not -path './docs/*' -not -path './.git/*' -print0 | xargs -0 gofmt -l)
	if [ -n "$unformatted" ]; then
		echo "gofmt reports unformatted files:"
		echo "$unformatted"
		return 1
	fi
	echo "all go files are gofmt clean"
}

stage_vet() {
	go vet ./...
}

stage_test() {
	# shellcheck disable=SC2086
	go test $TEST_PKGS -count=1
}

stage_html_writer_golden() {
	# Read-only golden comparison: tests only diff against committed .html
	# files and never rewrite them.
	go test ./org -run 'TestHTMLWriter|TestExtendedHTMLWriter|TestPrettyRelativeLinks|TestTopLevelHLevel' -count=1 -v
}

stage_org_writer_golden() {
	go test ./org -run 'TestOrgWriter|TestExtendedOrgWriter' -count=1 -v
}

stage_parser_structure() {
	fixtures=$(awk '{ print $2 }' tools/fixtures.manifest |
		grep '\.org$' | sed 's|org/testdata/||; s|\.org$||' | tr '\n' ' ')
	echo "representative fixtures: $fixtures"
	GO_ORG_VERIFY_FIXTURES="$fixtures" go test ./org -run TestParseWriteParseStructure -count=1 -v
}

stage_blorg_build() {
	site="$VERIFY_TMP/blorg-site"
	mkdir -p "$site"
	cp blorg/testdata/blorg.org "$site/blorg.org"
	cp -R blorg/testdata/content "$site/content"
	go build -o "$VERIFY_TMP/go-org" . || return 1
	(
		cd "$site" || exit 1
		"$VERIFY_TMP/go-org" blorg build || exit 1
	)
	# Compare the rendered output file set and content against the committed
	# golden tree - rendered into the temp site, never into the repo.
	diff -r blorg/testdata/public "$site/public" || {
		echo "blorg output differs from committed golden tree"
		return 1
	}
	# Verify relative and base-url links in the rendered site.
	grep -q 'href="/go-org/blorg/style.css"' "$site/public/index.html" || {
		echo "missing base-url stylesheet link in rendered index.html"
		return 1
	}
	grep -q 'href="/go-org/blorg/yet-another-post/index.html"' "$site/public/index.html" || {
		echo "missing pretty permalink in rendered index.html"
		return 1
	}
	grep -q 'href="about.html"' "$site/public/index.html" || {
		echo "missing relative link in rendered index.html"
		return 1
	}
	echo "blorg build ok: $(find "$site/public" -type f | wc -l | tr -d ' ') files, links verified"
}

stage_fuzz_smoke() {
	echo "fuzz budget: $FUZZ_BUDGET iterations, seed: $GO_ORG_FUZZ_SEED"
	GO_ORG_FUZZ_BUDGET="$FUZZ_BUDGET" go test ./org -run TestFuzzSmoke -count=1 -v
}

stage_workspace_clean() {
	check_fixture_manifest || {
		echo "fixture hashes changed during verification"
		return 1
	}
	if [ "$(cat "$VERIFY_TMP/git-status-before")" = "UNAVAILABLE" ]; then
		echo "git status unavailable at start; cannot prove workspace cleanliness"
		return 1
	fi
	git status --porcelain >"$VERIFY_TMP/git-status-after"
	if ! diff -u "$VERIFY_TMP/git-status-before" "$VERIFY_TMP/git-status-after"; then
		echo "workspace state changed during verification"
		return 1
	fi
	echo "workspace clean, fixture hashes unchanged"
}

main() {
	echo "[verify] mode: $MODE (repo: $REPO_ROOT)"
	run_stage go-version stage_go_version
	run_stage fixture-manifest stage_fixture_manifest
	run_stage format stage_format
	run_stage vet stage_vet
	run_stage test stage_test
	run_stage html-writer-golden stage_html_writer_golden
	run_stage org-writer-golden stage_org_writer_golden
	run_stage parser-structure stage_parser_structure
	run_stage blorg-build stage_blorg_build
	run_stage fuzz-smoke stage_fuzz_smoke
	run_stage workspace-clean stage_workspace_clean
	echo "[verify] all stages passed (mode: $MODE)"
}

main
