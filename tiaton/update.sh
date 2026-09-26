#!/bin/sh
# Prepare a new two-commit fork branch on top of upstream/master.
set -eu

cd "$(git rev-parse --show-toplevel)"
if [ -n "$(git status --porcelain)" ]; then
    printf '%s\n' 'Start from a clean checkout of tiaton-main.' >&2
    exit 1
fi
if [ "$(git branch --show-current)" != tiaton-main ]; then
    printf '%s\n' 'Switch to tiaton-main before updating.' >&2
    exit 1
fi
if [ "$#" -gt 1 ]; then
    printf '%s\n' 'Usage: tiaton/update.sh [new-branch-name]' >&2
    exit 1
fi

branch=${1:-"tiaton-update-$(date +%Y%m%d-%H%M%S)"}
files=$(mktemp -d)
trap 'rm -rf "$files"' EXIT HUP INT TERM
cp tiaton/rename.sh tiaton/update.sh tiaton/README.md "$files/"

if ! git remote get-url upstream >/dev/null 2>&1; then
    git remote add upstream https://github.com/google/starlark-go.git
fi
git fetch upstream
if ! git merge-base --is-ancestor master upstream/master; then
    printf '%s\n' 'Local master diverged from upstream/master; update the mirror manually.' >&2
    exit 1
fi
git branch -f master upstream/master

base=$(git merge-base upstream/master tiaton-main)
set -- $(git rev-list --first-parent --reverse "$base"..tiaton-main)
if [ "$#" -ne 2 ]; then
    printf '%s\n' 'Expected exactly two fork commits after the upstream base.' >&2
    exit 1
fi
patch_commit=$1

git switch -c "$branch" upstream/master
if ! git cherry-pick "$patch_commit"; then
    printf '%s\n' 'Cherry-pick stopped on a conflict. Resolve it and run git cherry-pick --continue, then restore tiaton/ from tiaton-main and run rename.sh and the checks manually.' >&2
    exit 1
fi

mkdir -p tiaton
cp "$files/rename.sh" "$files/update.sh" "$files/README.md" tiaton/
./tiaton/rename.sh

if ! grep -q '^# Starlark in Go$' README.md; then
    printf '%s\n' 'Upstream README changed its title; update the fork banner manually.' >&2
    exit 1
fi
{
    printf '%s\n' '# Starlark in Go' '' \
        '> **tiatoncom fork** of [google/starlark-go](https://github.com/google/starlark-go),' \
        '> published as `github.com/tiatoncom/starlark-go`. The fork bounds allocations' \
        '> from concatenation, list extension, and string formatting. See' \
        '> [fork maintenance](tiaton/README.md) for details and update instructions.' ''
    sed -n '/^# Starlark in Go$/,$p' README.md | sed '1d; s|go\.starlark\.net|github.com/tiatoncom/starlark-go|g; s|https://github.com/google/starlark-go/actions/workflows/tests.yml|https://github.com/tiatoncom/starlark-go/actions/workflows/tests.yml|g'
} > "$files/README.md"
cp "$files/README.md" README.md

GOWORK=off go vet ./...
GOWORK=off go test ./...

git add -A
git -c commit.gpgsign=false -c commit.template=/dev/null commit \
    -m 'tiatoncom fork: module path github.com/tiatoncom/starlark-go' \
    -m 'Rewrite upstream module references with tiaton/rename.sh and include fork maintenance scripts and documentation.'

latest=$(git tag --merged tiaton-main --list 'v*' --sort=-version:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sed -n '1p')
if [ -n "$latest" ]; then
    version=${latest#v}
    major=${version%%.*}
    version=${version#*.}
    minor=${version%%.*}
    patch=${version#*.}
    next="v$major.$minor.$((patch + 1))"
else
    next=v0.0.1
fi
printf 'Review %s, then move tiaton-main and publish the immutable tag %s.\n' "$branch" "$next"
