#!/bin/sh
# build-release.sh builds the binaries published for one tagged release.
#
# Usage: scripts/build-release.sh <version> [output-dir]
#
# The version is the release tag, for example v0.1.0. Each target is built with
# CGO disabled, so every artifact is one self-contained binary that runs on a
# machine without a Go toolchain and without any other file from this
# repository. The script writes the binaries and sha256sums.txt into the output
# directory, which defaults to dist/ at the repository root. A relative output
# directory is resolved against the repository root. Any pilharmonic_* file
# already in that directory is removed first, so the checksums describe this
# release only and a later upload cannot publish an older binary.
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
	echo "usage: $0 <version> [output-dir]" >&2
	exit 2
fi

version=$1
out=${2:-dist}

case $version in
	'' | [!0-9A-Za-z]* | *[!0-9A-Za-z._+-]*)
		echo "build-release: version must start with a letter or digit and use only letters, digits, '.', '_', '+' and '-': $version" >&2
		exit 2
		;;
esac

if command -v sha256sum >/dev/null 2>&1; then
	sha256=sha256sum
elif command -v shasum >/dev/null 2>&1; then
	sha256='shasum -a 256'
else
	echo "build-release: need sha256sum or shasum to write checksums" >&2
	exit 1
fi

# Resolve the repository root so the script works from any directory and when
# it is found through PATH.
self=$(command -v "$0" 2>/dev/null || printf '%s' "$0")
repo=$(CDPATH= cd -- "$(dirname -- "$self")/.." && pwd)
cd "$repo"

mkdir -p "$out"
out=$(CDPATH= cd -- "$out" && pwd)
rm -f "$out"/pilharmonic_*

commit=$(git rev-parse HEAD)
ldflags="-s -w -X main.version=$version -X main.commit=$commit"

# The release ships one binary for each supported operating system and
# architecture. Add a line here to publish another target.
targets="
linux/amd64
linux/arm64
darwin/amd64
darwin/arm64
windows/amd64
"

set -f # keep the target list from expanding into file names
for target in $targets; do
	goos=${target%/*}
	goarch=${target#*/}
	name="pilharmonic_${version}_${goos}_${goarch}"
	if [ "$goos" = windows ]; then
		name="$name.exe"
	fi
	echo "building $name"
	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
		go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$out/$name" .
done
set +f

(
	cd "$out"
	# shellcheck disable=SC2086 # sha256 is either "sha256sum" or "shasum -a 256".
	$sha256 pilharmonic_"$version"_* >sha256sums.txt
)

echo "release $version written to $out"
