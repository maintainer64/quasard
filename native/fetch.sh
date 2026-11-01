#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
set -eu
# Every downloaded archive gets a fresh directory; unpack outside scripts directory.
fetch() (
 url=$1 hash=$2 destination=$3
 mkdir -p "$(dirname "$destination")"
 if [ -f "$destination" ] && printf '%s  %s\n' "$hash" "$destination" | sha256sum -c - >/dev/null 2>&1; then return; fi
 work=$(mktemp -d)
 curl --fail --location --retry 3 --connect-timeout 30 --max-time 600 "$url" -o "$work/download"
 printf '%s  %s\n' "$hash" "$work/download" | sha256sum -c -
 mv "$work/download" "$destination"
 rmdir "$work"
)
