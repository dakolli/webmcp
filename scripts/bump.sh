#!/bin/sh
# bump.sh creates the next annotated release tag locally and prints the push
# command. It never pushes, never touches the index and never publishes.
set -eu

part=${1:-patch}
case "$part" in
	patch | minor | major) ;;
	*)
		printf 'bump: unknown part %s (use patch, minor or major)\n' "$part" >&2
		exit 1
		;;
esac

if [ -n "$(git status --porcelain)" ]; then
	printf 'bump: dirty working tree — commit or stash first\n' >&2
	git status --short >&2
	exit 1
fi

tag=$(git describe --tags --abbrev=0 2>/dev/null || printf 'v0.0.0')
version=${tag#v}
major=${version%%.*}
rest=${version#*.}
minor=${rest%%.*}
patch=${rest#*.}

for n in "$major" "$minor" "$patch"; do
	case "$n" in
		'' | *[!0-9]*)
			printf 'bump: cannot parse tag %s into major.minor.patch\n' "$tag" >&2
			exit 1
			;;
	esac
done

case "$part" in
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
esac

next="v${major}.${minor}.${patch}"
if git rev-parse -q --verify "refs/tags/${next}" >/dev/null; then
	printf 'bump: tag %s already exists\n' "$next" >&2
	exit 1
fi

git tag -a "$next" -m "Release $next"
printf 'tagged %s locally\n' "$next"
printf 'push it with: git push origin %s\n' "$next"
