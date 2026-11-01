#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Minimal server codec: no video, network protocols, device backends or CLI tools.
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$here/fetch.sh"
prefix=${1:-/opt/quasard}
work=${2:?fresh build directory required}
mkdir -p "$work" "$prefix/bin" "$prefix/share/licenses"
fetch https://codeload.github.com/FFmpeg/FFmpeg/tar.gz/894da5ca7d742e4429ffb2af534fcda0103ef593 9dad3557314f1b7923b61aff15e30079e964c3b41276bbd43dd8bb15b8028b5f "$work/ffmpeg.tar.gz"
mkdir "$work/source" "$work/build"
tar -xzf "$work/ffmpeg.tar.gz" -C "$work/source" --strip-components=1
cd "$work/build"
"$work/source/configure" --prefix="$prefix" --disable-everything --disable-autodetect \
 --disable-doc --disable-debug --disable-network --disable-avdevice \
 --enable-avformat --disable-swscale --disable-ffplay --disable-ffprobe \
 --disable-shared --enable-static --enable-small --enable-ffmpeg --enable-swresample \
 --enable-libopus --enable-decoder=opus,pcm_s16le --enable-encoder=libopus,pcm_s16le \
 --enable-parser=opus --enable-demuxer=ogg,pcm_s16le --enable-muxer=ogg,pcm_s16le \
 --enable-protocol=pipe --enable-filter=aresample,aformat,anull --disable-x86asm
make -j "${BUILD_JOBS:-2}"
install -m 755 ffmpeg "$prefix/bin/ffmpeg"
strip "$prefix/bin/ffmpeg"
cp "$work/source/COPYING.LGPLv2.1" "$prefix/share/licenses/FFMPEG_LGPL2.1"
# Only the binary is installed: headers/static archives stay in the builder.
