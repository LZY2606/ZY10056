# Verifying go-org

`tools/verify.sh` is the single offline verification entry point used both
locally and in CI. It checks the parser, the HTML and Org writers, the blorg
example and the fixtures in one fixed-order pipeline.

```sh
sh tools/verify.sh          # full mode (default)
sh tools/verify.sh full     # full mode
sh tools/verify.sh quick    # quick mode
```

The script can be run from any directory; it operates on the repository root.
It requires `go`, `gofmt`, `git` (optional, for the workspace snapshot) and
`sha256sum` or `shasum`. It needs no network access, no pandoc, no browser,
no external services and no manually set environment variables. A warm Go
module cache is enough (run `go build ./...` once, or `go mod download`).

## Modes

Both modes run every stage — `quick` never skips coverage areas, it only
lowers budgets:

| stage        | quick                          | full                            |
|--------------|--------------------------------|---------------------------------|
| parser       | `go test -count=1 ./org/`      | `go test -race -count=1 ./org/` |
| blorg (unit) | `go test -count=1 ./blorg/`    | `go test -race -count=1 ./blorg/` |
| fuzz         | 300 mutations                  | 3000 mutations                  |

`quick` covers the parser, both writers, the byte manifest, one blorg build
scenario and the fixed-corpus fuzz smoke, and finishes in well under 90 s.

## Stages (fixed order)

1. **env** — Go version check against `go.mod`, builds of the verification
   helpers into a temporary directory, workspace snapshot.
2. **fixtures** — raw-byte SHA-256 check of `tools/fixtures/manifest.sha256`
   before *any* parsing, so no tool can normalize a fixture first and produce
   a false pass. Also asserts `crlf.org` still contains CR bytes.
3. **format** — `gofmt -l` must be empty.
4. **vet** — `go vet ./...`.
5. **parser** — `go test ./org/` (parser + writer unit tests).
6. **html-writer** — read-only golden comparison: rendered HTML is compared
   against `tools/fixtures/golden/*.html` as normalized semantic nodes
   (elements with sorted attributes, whitespace-collapsed text outside
   `<pre>`), not as raw bytes.
7. **org-writer** — read-only golden comparison of Org writer output against
   `tools/fixtures/golden/*.org`, byte for byte.
8. **struct** — parse → Org write → re-parse structural check comparing
   headline levels, link targets, footnote references, code block languages
   and list structure for every fixture.
9. **blorg** — blorg unit test, then a full `blorg build` of a temporary
   content site (copied from `blorg/testdata`, never the repo). The output
   file set and file contents are compared against `blorg/testdata/public`
   and a relative link (`href="about.html"`) must be present in `index.html`.
10. **fuzz** — deterministic fuzz smoke (`tools/fuzzsmoke`) over the fixed
    corpus (`org/testdata/*.org` + `tools/fixtures/*.org`) with fixed seed
    `1060`. Unmodified corpus entries must round-trip exactly
    (parse → Org write → re-parse → identical HTML). Mutated inputs only
    assert robustness (no panics, no writer errors) — exact round-trip
    equality on arbitrary mutated input is a fuzz finding (see `org/fuzz.go`),
    not a smoke invariant.
11. **cleanliness** — `git status --porcelain` must equal the snapshot taken
    in `env`, and the fixture manifest is re-checked. Consecutive runs, and
    runs under different locales, timezones or `TMPDIR`s, must leave both
    unchanged.

Every stage prints `==> [stage] start` and `==> [stage] ok` or
`==> [stage] FAILED (exit N): ...`.

## Offline boundary

The script exports `GOPROXY=off`, `GOFLAGS=-mod=readonly` and
`GOTOOLCHAIN=local`: no module, toolchain or tool downloads are possible. It
also pins `LC_ALL=C` and `TZ=UTC` so results do not depend on locale or
timezone. It never starts servers or opens browsers.

## Exit codes

- `0` — all stages passed.
- `N != 0` — the failing stage's original exit code is preserved (usually
  `1`; `2` for usage errors; `127` for missing tools; `128+signal` when
  interrupted by a signal).

## Artifacts and temporary files

All temporary files live in one `mktemp -d` directory per run. It is removed
on success and on signal/failure exits — unless a stage failed, in which case
the directory is kept and its path is printed (`failure artifacts kept in
...`) so the stage logs and fuzz artifacts can be inspected. Cleanup only
ever removes the temporary directory created by that run; the repository is
never written to.

Fuzz failures write the offending input to `<tmp>/fuzz-artifacts/` and print
a replay command:

```sh
go run ./tools/fuzzsmoke -replay <artifact-path>
```

Because the seed is fixed, a failure is also reproduced by simply re-running
the same `tools/verify.sh` mode.

## Golden drift

Golden comparison is strictly read-only: drift prints a minimal diff (first
differing node/line with its location) and never accepts or overwrites
repository files. To update goldens intentionally, a human runs:

```sh
go run ./tools/orgcheck update tools/fixtures   # regenerate goldens
git diff tools/fixtures                          # human review of the drift
```

## Updating the fixture manifest (manual flow)

`tools/fixtures/manifest.sha256` covers every file under `tools/fixtures/`
(fixtures and goldens) as raw bytes. After intentionally adding or changing a
fixture or golden:

```sh
# 1. review the change: git diff tools/fixtures
# 2. regenerate the manifest
find tools/fixtures -type f ! -name manifest.sha256 | sort | xargs shasum -a 256 > tools/fixtures/manifest.sha256
# 3. verify
sh tools/verify.sh quick
```

The manifest is verified twice per run (before any parsing and again in the
cleanliness stage), so a tool that rewrites a fixture mid-run is always
caught.

## CI

`.github/workflows/ci.yml` downloads modules (`go mod download`, the only
networked step) and then calls the same entry point as local runs:

```sh
sh tools/verify.sh full
```
