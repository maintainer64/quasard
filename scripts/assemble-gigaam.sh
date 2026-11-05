#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Build-time assembly only: GitHub limits individual regular Git files to 100 MiB.
set -eu
export LC_ALL=C
source_dir=$(CDPATH= cd -- "${1:?source model directory required}" && pwd)
output=${2:?output model directory required}
mkdir -p "$output"
output=$(CDPATH= cd -- "$output" && pwd)
[ "$source_dir" != "$output" ] || { echo 'Use a separate output directory' >&2; exit 1; }
trap 'rm -f "$output/encoder.onnx.tmp"' EXIT HUP INT TERM
cat "$source_dir"/encoder.onnx.part-* > "$output/encoder.onnx.tmp"
for name in decoder.onnx joiner.onnx manifest.json LICENSE.GigaAM SHA256SUMS; do
    cp "$source_dir/$name" "$output/$name"
done
mv "$output/encoder.onnx.tmp" "$output/encoder.onnx"
(cd "$output" && sha256sum -c SHA256SUMS)
