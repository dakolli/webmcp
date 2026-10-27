#!/bin/sh
# install.sh downloads the release archive for this machine, verifies its
# checksum and installs the `browser` binary on PATH. POSIX sh; never root.
set -eu

repo=dakolli/webmcp
api="https://api.github.com/repos/${repo}/releases/latest"
downloads="https://github.com/${repo}/releases/download"

fail() {
	printf 'install: %s\n' "$*" >&2
	exit 1
}

if ! command -v curl >/dev/null 2>&1; then
	fail 'curl is required but was not found on PATH'
fi

os=$(uname -s)
case "$os" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*)
		printf 'install: unsupported OS %s\n' "$os" >&2
		printf 'install: Windows users must download the .zip from https://github.com/%s/releases\n' "$repo" >&2
		exit 1
		;;
esac

machine=$(uname -m)
case "$machine" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) fail "unsupported machine architecture $machine" ;;
esac

if [ -n "${VERSION:-}" ]; then
	version=${VERSION#v}
else
	release=$(curl -fsSL "$api") ||
		fail "could not read the latest release from $api — set VERSION to skip the lookup, e.g. VERSION=0.1.0"
	version=$(printf '%s\n' "$release" |
		sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' |
		head -n 1)
	version=${version#v}
	[ -n "$version" ] ||
		fail "no tag_name in the GitHub API response — set VERSION to skip the lookup, e.g. VERSION=0.1.0"
fi
[ -n "$version" ] || fail 'VERSION must not be empty'

asset="browser_${version}_${os}_${arch}.tar.gz"
base="${downloads}/v${version}"

tmp=$(mktemp -d) || fail 'mktemp -d failed'
stage=
cleanup() {
	[ -z "$stage" ] || rm -f "$stage"
	rm -rf "$tmp"
}
trap cleanup 0
trap 'exit 1' HUP INT TERM

fetch() {
	_url=$1
	_out=$2
	code=$(curl -sSL -o "$_out" -w '%{http_code}' "$_url" 2>/dev/null) ||
		fail "could not reach $_url"
	case "$code" in
		2??) ;;
		404) fail "not found (404): $_url" ;;
		*) fail "unexpected HTTP status $code from $_url" ;;
	esac
}

fetch "$base/$asset" "$tmp/$asset"
fetch "$base/checksums.txt" "$tmp/checksums.txt"

grep -E "[[:space:]][*]?${asset}\$" "$tmp/checksums.txt" >"$tmp/verify.sha256" ||
	fail "checksums.txt has no entry for $asset"
if command -v sha256sum >/dev/null 2>&1; then
	(cd "$tmp" && sha256sum -c --ignore-missing verify.sha256) ||
		fail "checksum mismatch for $asset — refusing to install"
elif command -v shasum >/dev/null 2>&1; then
	(cd "$tmp" && shasum -a 256 -c verify.sha256) ||
		fail "checksum mismatch for $asset — refusing to install"
else
	fail 'sha256sum or shasum is required to verify the download'
fi

tar -xzf "$tmp/$asset" -C "$tmp" || fail "could not extract $asset with tar"
src="$tmp/browser"
if [ ! -f "$src" ]; then
	src=$(find "$tmp" -type f -name browser | head -n 1)
	[ -n "$src" ] || fail "no browser binary inside $asset"
fi

if [ -n "${BIN_DIR:-}" ]; then
	bin_dir=$BIN_DIR
elif [ -n "${HOME:-}" ]; then
	bin_dir=$HOME/.local/bin
elif [ -w /usr/local/bin ]; then
	bin_dir=/usr/local/bin
else
	fail 'no writable install directory — set BIN_DIR to a directory you own'
fi

if [ -n "${HOME:-}" ]; then
	case "$bin_dir" in
		"$HOME"/*) mkdir -p "$bin_dir" || fail "could not create $bin_dir" ;;
	esac
fi
[ -d "$bin_dir" ] || fail "$bin_dir does not exist — create it or set BIN_DIR"
[ -w "$bin_dir" ] || fail "$bin_dir is not writable — set BIN_DIR to a directory you own"

dest="$bin_dir/browser"
stage="$bin_dir/.browser.$$"
cp "$src" "$stage" || fail "could not write to $bin_dir"
chmod 0755 "$stage" || fail "could not set mode 0755 in $bin_dir"
mv -f "$stage" "$dest" || fail "could not install to $dest"
stage=

printf 'installed %s\n' "$dest"
"$dest" --version || printf 'install: warning: %s --version failed\n' "$dest" >&2
printf 'next: browser serve mcp --port 4099\n'
