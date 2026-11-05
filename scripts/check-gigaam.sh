#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# CI only: public synthetic speech and the model already embedded in the image.
set -eu
image=${1:?speech-test image required}
work=${2:?fixture directory required}
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$here/../native/fetch.sh"
mkdir -p "$work/models"
while read -r name hash size url; do
 case "$name" in ''|'#'*) continue;; esac
 fetch "$url" "$hash" "$work/models/$name"
 [ "$(wc -c < "$work/models/$name" | tr -d ' ')" = "$size" ]
done < "$here/../native/asr-smoke-models.lock"
voices=$(CDPATH= cd -- "$work/models" && pwd)
docker run --rm --network none --memory 1536m --cpus 2 \
 -e QUASARD_SMOKE_MODELS=/models -e QUASARD_GIGAAM_MODEL=/opt/quasard/models/gigaam \
 -e QUASARD_SMOKE_VAD=/silero_vad.onnx \
 -v "$voices:/models:ro" "$image" \
 -test.v -test.run '^TestNative(GigaAM|Smoke|BilingualSmoke)$' -test.timeout 3m
