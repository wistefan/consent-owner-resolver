#!/usr/bin/env bash
#
# Prints the next semantic version for a bump level, derived from the highest
# existing release tag. With no release tag yet, the base is 0.0.0 -- so the
# first `patch` release is 0.0.1.
#
# Usage: next-version.sh <major|minor|patch>
#
# Requires the full tag history (`actions/checkout` with `fetch-depth: 0`).
# Replaces zwaldowski/semver-release-action, which is archived.

set -euo pipefail

bump="${1:-}"
if [[ -z "$bump" ]]; then
	echo "usage: $(basename "$0") <major|minor|patch>" >&2
	exit 1
fi

# Tags are minted without a `v` prefix, but tolerate one so a repository that
# switches convention does not silently restart at 0.0.1.
# `|| true`: no release tag yet is the normal state of a new repository, and
# grep exits 1 on no match - which `set -e` would otherwise treat as fatal.
latest="$(git tag --list |
	grep -E '^v?[0-9]+\.[0-9]+\.[0-9]+$' |
	sed 's/^v//' |
	sort -t. -k1,1n -k2,2n -k3,3n |
	tail -n 1 || true)"
latest="${latest:-0.0.0}"

IFS=. read -r major minor patch <<<"$latest"

case "$bump" in
major)
	major=$((major + 1))
	minor=0
	patch=0
	;;
minor)
	minor=$((minor + 1))
	patch=0
	;;
patch)
	patch=$((patch + 1))
	;;
*)
	echo "unknown bump level: $bump (expected major, minor or patch)" >&2
	exit 1
	;;
esac

echo "${major}.${minor}.${patch}"
