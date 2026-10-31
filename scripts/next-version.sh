#!/bin/sh
# Prints the next release tag (vX.Y.Z) from Conventional Commits since the last tag, or nothing
# when those commits don't warrant a release.
#
#   type!: ... or a BREAKING CHANGE footer  -> major (minor while still 0.x)
#   feat                                     -> minor
#   fix, perf, refactor, revert, build       -> patch
#   docs, chore, ci, test, style, anything else -> no release
set -eu

last=$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || true)
if [ -n "$last" ]; then
	range="$last..HEAD"
else
	last=v0.0.0
	range=HEAD
fi

log=$(git log --format='%s%n%b%n--END--' "$range")
[ -n "$log" ] || exit 0

bump="none"
if printf '%s\n' "$log" | grep -Eq '^[a-z]+(\([^)]*\))?!:|^BREAKING[ -]CHANGE:'; then
	bump="major"
elif printf '%s\n' "$log" | grep -Eq '^feat(\([^)]*\))?:'; then
	bump="minor"
elif printf '%s\n' "$log" | grep -Eq '^(fix|perf|refactor|revert|build)(\([^)]*\))?:'; then
	bump="patch"
fi
[ "$bump" != none ] || exit 0

ver=${last#v}
major=${ver%%.*}
rest=${ver#*.}
minor=${rest%%.*}
patch=${rest#*.}

# Before 1.0.0 a breaking change bumps the minor; 1.0.0 is tagged deliberately, by hand.
if [ "$bump" = major ] && [ "$major" -eq 0 ]; then
	bump="minor"
fi

case $bump in
major) major=$((major + 1)) minor=0 patch=0 ;;
minor) minor=$((minor + 1)) patch=0 ;;
patch) patch=$((patch + 1)) ;;
esac
echo "v$major.$minor.$patch"
