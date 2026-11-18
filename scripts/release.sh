#!/bin/sh
# Build the release archives and create the GitHub release.
#
#   scripts/release.sh v0.2.0 [notes-file]
#   scripts/release.sh v0.2.0 --dry-run
#
# The tag must exist and be pushed before this runs: the archives are built from
# the working tree, and the script refuses a dirty one so a release never
# contains half a change. The version reaches the binary through -ldflags, so
# `afford version` and the tag cannot disagree.
set -eu

usage() {
	echo "usage: scripts/release.sh vX.Y.Z [notes-file] [--dry-run]" >&2
	exit 2
}

version=""
notes=""
dry_run=0
for arg in "$@"; do
	case "$arg" in
	--dry-run) dry_run=1 ;;
	v[0-9]*) version="$arg" ;;
	*) notes="$arg" ;;
	esac
done
[ -n "$version" ] || usage

[ -z "$(git status --porcelain)" ] || {
	echo "release: working tree is dirty; commit first so the archives match the tag" >&2
	exit 2
}
git rev-parse -q --verify "refs/tags/$version" >/dev/null || {
	echo "release: tag $version does not exist locally" >&2
	exit 2
}
git ls-remote --exit-code --tags origin "refs/tags/$version" >/dev/null 2>&1 || {
	echo "release: tag $version is not pushed" >&2
	exit 2
}

num="${version#v}"
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT INT TERM

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
	os="${target%/*}"
	arch="${target#*/}"
	dir="$out/afford_${num}_${os}_${arch}"
	mkdir -p "$dir"
	bin=afford
	[ "$os" = windows ] && bin=afford.exe
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
		-ldflags "-s -w -X main.version=$num" -o "$dir/$bin" ./cmd/afford
	cp README.md LICENSE "$dir/"
	if [ "$os" = windows ]; then
		(cd "$out" && zip -qr "afford_${num}_${os}_${arch}.zip" "afford_${num}_${os}_${arch}")
	else
		(cd "$out" && tar czf "afford_${num}_${os}_${arch}.tar.gz" "afford_${num}_${os}_${arch}")
	fi
	echo "built $dir"
done

if command -v sha256sum >/dev/null 2>&1; then
	hash=sha256sum
else
	hash="shasum -a 256"
fi
(cd "$out" && $hash *"${num}"*.tar.gz *"${num}"*.zip > checksums.txt)
echo "checksums written"

assets=""
for f in "$out"/afford_"${num}"_*.tar.gz "$out"/afford_"${num}"_*.zip "$out"/checksums.txt; do
	assets="$assets $f"
done

if [ "$dry_run" -eq 1 ]; then
	echo "dry run; assets in $out:"
	ls -1 "$out"
	exit 0
fi

if gh release view "$version" >/dev/null 2>&1; then
	# shellcheck disable=SC2086
	gh release upload "$version" $assets --clobber
	echo "assets uploaded to the existing release $version"
else
	if [ -n "$notes" ]; then
		# shellcheck disable=SC2086
		gh release create "$version" --title "afford ${num}" --notes-file "$notes" $assets
	else
		# shellcheck disable=SC2086
		gh release create "$version" --title "afford ${num}" --generate-notes $assets
	fi
fi
gh release view "$version" --json url --jq .url
