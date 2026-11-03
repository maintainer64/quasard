#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Pinned CPU-only whisper.cpp. Run only during image/bundle build.
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$here/fetch.sh"
prefix=${1:-/opt/quasard}
work=${2:-$(mktemp -d)}
mkdir -p "$work/source" "$prefix/share/licenses"
fetch 'https://codeload.github.com/ggml-org/whisper.cpp/tar.gz/d1be6fde11ac6e0407606b4e42fe72d34add8037' 645445da884df6dbd52b30f82d7be7c01d1767c938714e477e786b88d529e9f4 "$work/source.tgz"
tar -xzf "$work/source.tgz" -C "$work/source" --strip-components=1
cmake -S "$work/source" -B "$work/build" -G Ninja \
 -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$prefix" -DCMAKE_INSTALL_LIBDIR=lib \
 -DCMAKE_INSTALL_RPATH="$prefix/lib" -DBUILD_SHARED_LIBS=ON \
 -DGGML_NATIVE=OFF -DGGML_BACKEND_DL=ON -DGGML_BACKEND_DIR="$prefix/lib" \
 -DGGML_CPU_ALL_VARIANTS=ON -DGGML_CUDA=OFF -DGGML_VULKAN=OFF -DGGML_METAL=OFF \
 -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_EXAMPLES=OFF -DWHISPER_BUILD_SERVER=OFF \
 -DWHISPER_CURL=OFF -DWHISPER_COREML=OFF -DWHISPER_OPENVINO=OFF
cmake --build "$work/build" --parallel "${BUILD_JOBS:-2}"
cmake --install "$work/build"
cp "$work/source/LICENSE" "$prefix/share/licenses/WHISPER_CPP"
# Vendored ggml is covered by the source archive root MIT license.
cp "$work/source/LICENSE" "$prefix/share/licenses/GGML"
