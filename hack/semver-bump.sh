#!/usr/bin/env bash
#
# Reads candidate labels on stdin (one per line) and prints the single semver
# bump level among them (major | minor | patch).
#
# Exits non-zero when there is none or more than one, which is exactly the
# "every PR carries exactly one semver label" rule the release depends on.
#
# Replaces zwaldowski/match-label-action, which is archived.

set -euo pipefail

bump=""
while IFS= read -r label; do
	case "$label" in
	major | minor | patch)
		if [[ -n "$bump" ]]; then
			echo "PR carries more than one semver label ($bump, $label); exactly one is required" >&2
			exit 1
		fi
		bump="$label"
		;;
	esac
done

if [[ -z "$bump" ]]; then
	echo "no semver label found; apply exactly one of: patch, minor, major" >&2
	exit 1
fi

echo "$bump"
