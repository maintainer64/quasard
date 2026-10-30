#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Run from trusted checkout; outputs must use a fresh directory.
set -eu
version=${1:?version required}
arch=${2:?amd64 or arm64 required}
out=${3:?output directory required}
case "$version" in *[!A-Za-z0-9.+-]*|'') printf 'Invalid version\n' >&2; exit 2;; esac
case "$arch" in amd64|arm64) ;; *) exit 2;; esac
mkdir -p "$out"
out=$(CDPATH= cd -- "$out" && pwd)
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
commit=$(git -C "$root" rev-parse HEAD)
date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
work=$(mktemp -d "$out/.build.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
name="quasard-$version-linux-$arch"
docker buildx build --platform "linux/$arch" --target bundle \
 --build-arg "VERSION=$version" --build-arg "COMMIT=$commit" --build-arg "BUILD_DATE=$date" \
 --output "type=local,dest=$work/$name" "$root"
tar -czf "$out/$name.tar.gz" -C "$work" "$name"
printf '%s\n' "$out/$name.tar.gz"
