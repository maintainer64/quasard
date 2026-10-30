#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
program=$(basename -- "$0")
if [ "$program" = ffmpeg ]; then root=$(CDPATH= cd -- "$root/.." && pwd); fi
if [ "${1:-}" = "--ffmpeg" ]; then
 program=ffmpeg
 shift
fi
case "$program" in launch|quasard) program=quasard;; quasard-tools|ffmpeg) ;; *) exit 2;; esac
loader=
for candidate in "$root"/runtime/usr/lib/ld-linux-*.so.* "$root"/runtime/usr/lib/*/ld-linux-*.so.* "$root"/runtime/usr/lib64/ld-linux-*.so.*; do
 if [ -f "$candidate" ]; then loader=$candidate; break; fi
done
if [ -z "$loader" ]; then printf 'Bundled Linux loader missing\n' >&2; exit 1; fi
libraries="$root/runtime/opt/quasard/lib"
for directory in "$root"/runtime/usr/lib/*-linux-gnu "$root/runtime/usr/lib64"; do
 [ ! -d "$directory" ] || libraries="$libraries:$directory"
done
export QUASARD_ESPEAK_DATA="$root/runtime/opt/quasard/share/espeak-ng-data"
export OMP_NUM_THREADS=${OMP_NUM_THREADS:-1}
export PATH="$root/bin:$PATH"
# Do not export bundled glibc through LD_LIBRARY_PATH to the host shell.
unset LD_LIBRARY_PATH LD_PRELOAD
cd "$root"
exec "$loader" --library-path "$libraries" "$root/runtime/usr/local/bin/$program" "$@"
