# tiatoncom fork of starlark-go

This fork publishes [google/starlark-go](https://github.com/google/starlark-go)
under the Go module path `github.com/tiatoncom/starlark-go`. Its only behavior
change bounds the memory allocated by a single `+`, `+=`, `extend`, `str`, or `%`
operation. Repeated concatenation or stringification of shared subgraphs can
otherwise grow exponentially while consuming only one execution step per
operation. The patch applies the existing `maxAlloc` limit used by repetition
(`*`) and includes tests for both the limits and ordinary operations.

The `tiaton/` directory keeps the maintenance scripts and fork documentation
together; it is not part of the Go packages.

## Branches and releases

- `master` mirrors upstream `master` without fork changes.
- `tiaton-main` contains an upstream base plus exactly two fork commits: the
  allocation-limit patch and the module-path/maintenance commit.
- Release tags `vX.Y.Z` point to commits on `tiaton-main` and are immutable.
  Publish a new tag rather than moving a published one.

## Updating from upstream

Start with a clean `tiaton-main` checkout and run `./tiaton/update.sh`. The
script fetches upstream, creates a new update branch from `upstream/master`,
cherry-picks the allocation patch, rewrites module references with
`./tiaton/rename.sh`, runs `GOWORK=off go vet ./...` and
`GOWORK=off go test ./...`, commits the module-path and maintenance files, and
suggests the next release tag. It stops at a cherry-pick conflict with an
explanation; resolve the conflict and rerun the remaining steps manually.
Review the new branch before moving `tiaton-main` or publishing a tag.
At the initial upstream base, unmodified `starlark/int_posix64.go` triggers
`go vet`'s `unsafeptr` diagnostic. The script stops on that failure instead of
silently skipping the check; resolve it before publishing an update.

`rename.sh` rewrites module references in `go.mod` and tracked Go files across
the tree. It is idempotent. Upstream website files and protobuf package names
retain their upstream identity; changing those strings would break their
published URLs or wire formats.
