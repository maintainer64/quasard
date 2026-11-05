# Licensing and provenance

New Go code in this migration is licensed GPL-3.0-only; see LICENSE and SPDX headers.
This statement does not grant rights that upstream authors or third parties have not granted.

## Reference implementation

Python reference: amirilivice/quasard, commit a142b19c997f3a8a4bed8f6b0aef4acd70e2436e,
https://github.com/amirilivice/quasard . No top-level license was present at that commit.
The Go migration uses its observable protocol and behavior as a reference. Permission
for redistribution of retained source or derivative portions must be resolved before publishing.
Do not interpret the new root LICENSE as a relicensing of the reference implementation.

## Assets and native dependencies

- alice_server/assets/silero_vad.onnx: retain accompanying SILERO_VAD_LICENSE (MIT).
- alice_server/aliceapi.desc: recovered firmware descriptors; rights/provenance must be checked before redistribution.
- Vosk API: Apache-2.0; each downloaded model has its own license, to be retained.
- Piper libpiper: GPL-3.0; espeak-ng, ONNX Runtime and bundled dependencies retain their own notices.
- Piper voice models: verify the full artifact license; dataset license alone is insufficient.
- FFmpeg: build/license depends on enabled codecs and configuration; preserve the runtime package copyright notices.
- Go modules: pinned in go.mod/go.sum, retain their upstream licenses when redistributing.
- Built-in DNS uses github.com/miekg/dns (BSD-3-Clause); linked dependency licenses are
  collected by scripts/go-notices.sh into the image and Linux/Windows bundles.

Include required corresponding source and notices when distributing built images or archives.
PR images and tag releases publish automatically after their build/test checks; there is no
separate distribution-approval variable.

- whisper.cpp and its vendored ggml: MIT, pinned source d1be6fde11ac6e0407606b4e42fe72d34add8037 (v1.9.5).
  The upstream license is retained in native runtime/bundle notices. Whisper model weights are downloaded separately.
  Local patch native/whisper-abort.patch forwards the configured cancellation callback to CPU graph execution.

- GigaAM v3_e2e_rnnt: MIT, source pinned to salute-developers/GigaAM
  7447938d791c4f3e643386ee22c33777004293a5. Exported ONNX weights and the complete
  upstream LICENSE are distributed separately from the runtime image. The
  conversion manifest records model/tokenizer hashes and tool versions.
