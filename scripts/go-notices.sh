#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
set -eu
out=${1:?notice directory required}
mkdir -p "$out/go"
go list -m all > "$out/GO_MODULES.txt"
go list -deps -f '{{if .Module}}{{if not .Module.Main}}{{.Module.Path}} {{.Module.Dir}}{{end}}{{end}}' ./cmd/... | sort -u | while read -r module directory; do
 [ -n "$module" ] || continue
 directory=$(printf '%s' "$directory" | tr '\\' '/')
 destination="$out/go/$module"
 mkdir -p "$destination"
 found=false
 for name in LICENSE LICENSE.txt LICENSE.md COPYING NOTICE NOTICE.txt; do
  if [ -f "$directory/$name" ]; then cp "$directory/$name" "$destination/"; found=true; fi
 done
 if [ "$found" = false ]; then printf 'Missing license: %s\n' "$module" >&2; exit 1; fi
done
