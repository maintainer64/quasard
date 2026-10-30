#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Collect shared-library closure; no compiler, shell or package manager in runtime.
set -eu
root=${1:?runtime staging directory}
mkdir -p "$root/usr/local/bin" "$root/opt/quasard/lib" "$root/opt/quasard/share" "$root/usr/share/licenses/quasard"
cp /opt/quasard/bin/ffmpeg "$root/usr/local/bin/"
cp -a /opt/quasard/share/espeak-ng-data "$root/opt/quasard/share/"
cp -a /opt/quasard/share/licenses/. "$root/usr/share/licenses/quasard/"
for file in /opt/quasard/lib/libvosk.so /opt/quasard/lib/libpiper.so* /opt/quasard/lib/libonnxruntime.so*; do
 [ -f "$file" ] || continue
 cp -a "$file" "$root/opt/quasard/lib/$(basename "$file")"
done
for file in /out/quasard /out/quasard-tools /opt/quasard/bin/ffmpeg "$root"/opt/quasard/lib/*.so*; do
 # Trusted build outputs only. ldd discovers linked SONAME paths.
 ldd "$file" | while IFS= read -r line; do
  case "$line" in *'not found'*) printf 'Unresolved dependency: %s\n' "$line" >&2; exit 1;; esac
  set -- $line
  case "${2:-}" in '=>') library=${3:-};; *) library=${1:-};; esac
  case "$library" in /lib/*|/lib64/*|/usr/lib/*|/usr/lib64/*)
   # Distroless uses merged-/usr: /lib and /lib64 are symlinks.
   destination=$library
   case "$destination" in /lib/*|/lib64/*) destination=/usr$destination;; esac
   mkdir -p "$root$(dirname "$destination")"
   cp -L "$library" "$root$destination"
   ;;
  esac
 done
done
# Native Vosk may load OpenMP dynamically; retain package runtime and notices.
find /usr/lib -name 'libgomp.so.1' -exec cp -L '{}' "$root/opt/quasard/lib/" \;
for package in libopus0 libgomp1 libc6 libstdc++6; do
 if [ -d "/usr/share/doc/$package" ]; then mkdir -p "$root/usr/share/licenses/quasard/$package"; cp -L "/usr/share/doc/$package/copyright" "$root/usr/share/licenses/quasard/$package/"; fi
done
