#!/bin/sh
# Rewrite module paths in go.mod and tracked Go files after importing upstream.
set -eu

cd "$(git rev-parse --show-toplevel)"

from='go\.starlark\.net'
to='github.com/tiatoncom/starlark-go'

if sed --version >/dev/null 2>&1; then
    replace() { sed -i "s|$from|$to|g" "$1"; }
else
    replace() { sed -i '' "s|$from|$to|g" "$1"; }
fi

# Leave upstream's website and protobuf package names intact. All Go module
# references, including imports, linkname directives, and test fixtures in Go
# files, are rewritten. A second run makes no changes.
if grep -q 'go\.starlark\.net' go.mod; then
    replace go.mod
fi

git grep -l 'go\.starlark\.net' -- '*.go' | while IFS= read -r file; do
    replace "$file"
done
