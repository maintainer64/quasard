#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Explicit CI runtime checks, never called by Dockerfile.
set -eu
image=${1:?local image required}
work=${2:?fresh output directory required}
mkdir "$work"
docker run --rm --network none "$image" --version
docker run --rm --network none --entrypoint /usr/local/bin/quasard-tools "$image" --version
# One second silence: validate exact minimal encode/decode paths without models.
dd if=/dev/zero of="$work/input.pcm" bs=32000 count=1 2>/dev/null
docker run --rm -i --network none --entrypoint /usr/local/bin/ffmpeg "$image" -v error -f s16le -ar 16000 -ac 1 -i pipe:0 -ar 48000 -c:a libopus -f ogg pipe:1 < "$work/input.pcm" > "$work/output.ogg"
docker run --rm -i --network none --entrypoint /usr/local/bin/ffmpeg "$image" -v error -f ogg -i pipe:0 -ar 16000 -ac 1 -f s16le pipe:1 < "$work/output.ogg" > "$work/output.pcm"
size=$(wc -c < "$work/output.pcm")
[ "$size" -ge 31000 ] && [ "$size" -le 33000 ]
printf 'Codec runtime passed; real Vosk/Piper/Silero inference NOT tested.\n'
