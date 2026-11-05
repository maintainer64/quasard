#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# CI only: public synthetic speech. Model export is a separate build-time job.
set -eu
image=${1:?speech-test image required}
voices=$(CDPATH= cd -- "${2:?fixture voices directory required}" && pwd)
model=$(CDPATH= cd -- "${3:?exported GigaAM directory required}" && pwd)
docker run --rm --network none --memory 1536m --cpus 2 \
 -e QUASARD_SMOKE_MODELS=/models -e QUASARD_GIGAAM_MODEL=/gigaam \
 -v "$voices:/models:ro" -v "$model:/gigaam:ro" "$image" \
 -test.v -test.run '^TestNativeGigaAM$' -test.timeout 3m
