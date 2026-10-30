#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Debian glibc, CPU only. Run from trusted repository, not download directories.
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$here/fetch.sh"
prefix=${1:-/opt/quasard}
work=${2:-$(mktemp -d)}
mkdir -p "$work" "$prefix/lib" "$prefix/include" "$prefix/share/licenses"
case "$(uname -m)" in
 aarch64) vosk_arch=aarch64;ort_arch=aarch64;vosk_hash=45e95d37755deb07568e79497d7feba8c03aee5a9e071df29961aa023fd94541;ort_hash=bb76395092d150b52c7092dc6b8f2fe4d80f0f3bf0416d2f269193e347e24702;;
 x86_64) vosk_arch=x86_64;ort_arch=x64;vosk_hash=bbdc8ed85c43979f6443142889770ea95cbfbc56cffb5c5dcd73afa875c5fbb2;ort_hash=8344d55f93d5bc5021ce342db50f62079daf39aaafb5d311a451846228be49b3;;
 *) printf 'Unsupported architecture\n' >&2;exit 1;;
esac
fetch "https://github.com/alphacep/vosk-api/releases/download/v0.3.45/vosk-linux-$vosk_arch-0.3.45.zip" "$vosk_hash" "$work/vosk.zip"
fetch "https://github.com/microsoft/onnxruntime/releases/download/v1.22.0/onnxruntime-linux-$ort_arch-1.22.0.tgz" "$ort_hash" "$work/ort.tgz"
fetch 'https://codeload.github.com/OHF-Voice/piper1-gpl/tar.gz/74ac24e7e8e2e57a5e1fada5505c74e3d9c1843a' b826e11544ec21e4e9c11ddc564718de989500ea101df2fe6142ba42146770a2 "$work/piper.tgz"
fetch 'https://codeload.github.com/espeak-ng/espeak-ng/tar.gz/212928b394a96e8fd2096616bfd54e17845c48f6' 1f201cabc73e569a7cb434d40d3b30980f923010f8ecd4d1c4ae94691ac2888a "$work/espeak.tgz"
fetch 'https://codeload.github.com/tsukumijima/open_jtalk/tar.gz/4a1ef70897689d2ee6b8f9be35454a3a4b15b289' aede59240df519019d344b0501d50230b566bdb97f795ee4f51b8a0930ed3754 "$work/openjtalk.tgz"
mkdir "$work/vosk" "$work/ort" "$work/piper" "$work/espeak" "$work/openjtalk"
unzip -q "$work/vosk.zip" -d "$work/vosk"
tar -xzf "$work/ort.tgz" -C "$work/ort" --strip-components=1
tar -xzf "$work/piper.tgz" -C "$work/piper" --strip-components=1
tar -xzf "$work/espeak.tgz" -C "$work/espeak" --strip-components=1
tar -xzf "$work/openjtalk.tgz" -C "$work/openjtalk" --strip-components=1
cp "$work/vosk/vosk-linux-$vosk_arch-0.3.45/libvosk.so" "$prefix/lib/"
cp "$work/vosk/vosk-linux-$vosk_arch-0.3.45/vosk_api.h" "$prefix/include/"
cp -a "$work/ort/lib/"* "$prefix/lib/"
# Upstream CMake export uses lib64 on ARM64 even though archive uses lib.
ln -s lib "$prefix/lib64"
cp "$work/ort/include/"*.h "$prefix/include/"
cp "$work/ort/LICENSE" "$prefix/share/licenses/ONNXRUNTIME"
cp "$work/piper/COPYING" "$prefix/share/licenses/PIPER"
# Replace only dependency fetch stanzas: retain exact pinned source, prevent
# CMake from fetching unverified dependency code, force ORT 1.22.0 for Piper.
perl -0777 -i -pe 's@GIT_REPOSITORY https://github.com/espeak-ng/espeak-ng.git\s+GIT_TAG [^\n]+@SOURCE_DIR "\$ENV{QS_ESPEAK_SOURCE}"\n    DOWNLOAD_COMMAND ""@; s@GIT_REPOSITORY https://github.com/tsukumijima/open_jtalk.git\s+GIT_TAG [^\n]+@SOURCE_DIR "\$ENV{QS_OPENJTALK_SOURCE}"\n    DOWNLOAD_COMMAND ""@' "$work/piper/libpiper/CMakeLists.txt"
export QS_ESPEAK_SOURCE="$work/espeak" QS_OPENJTALK_SOURCE="$work/openjtalk"
cmake -S "$work/piper/libpiper" -B "$work/build" -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$prefix" -DONNXRUNTIME_DIR="$work/ort" -DCMAKE_DISABLE_FIND_PACKAGE_onnxruntime=TRUE -DCMAKE_INSTALL_RPATH="$prefix/lib"
cmake --build "$work/build" --parallel "${BUILD_JOBS:-2}"
cmake --install "$work/build"
# espeak static library is linked into libpiper; runtime needs its data only.
cp -a "$work/build/espeak_ng-install/share/espeak-ng-data" "$prefix/share/"
