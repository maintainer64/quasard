# Go backend development

Work in progress, 2026-10-30. Go is active development/runtime path; migration is not a completed replacement. Do not deploy to a station based on fixture tests alone. Historical Python implementation is available from Git, not required at runtime; see [parity/recovery](parity.md).

## Implemented / locally tested

- TOML config and `ALICE_*` overrides, explicit config selection, migration diagnostics; private atomic device store; activation/Glagol HTTP mocks.
- JSON and descriptor-based AAPI WebSocket adapters; deterministic Russian commands.
- OpenAI-compatible streaming LLM with history, sentence splitting and cancellation.
- Go-native Yandex Music HTTP client, direct MP3 signing, queues and device-code login.
- Go tools: certificates, HAR import, activation/station simulators, fake LLM, audio cutting, Music login.
- Previous macOS ARM64 `go test -race ./...` and `go vet ./...` passed with fixtures/fake speech, **not native models**. Config cleanup adds path-priority and legacy-engine rejection tests; exact latest commands/results belong in change report.

## Runtime verification, 2026-10-30

- Native AMD64/ARM64 images, dependency loading, restricted codecs and Linux bundle launchers passed GitHub CI. Actual tag release publication remains untested.
- Isolated Docker Linux ARM64 smoke passed real Piper → Ogg → Vosk and concurrent Silero VAD, plus packaged JSON/AAPI text and voice round-trips, HTTP activation, UDP/TCP DNS forwarding and SIGHUP rollback. Fixed dropped final Piper chunk and wrong bundled Silero output name. Local smoke setup and downloaded models are not published.
- CI checks native builds/codecs/bundles, not real-model inference. Local ARM64 success is not AMD64 inference proof.

## Not verified / remaining

- Real station audio, external LLM, Music OAuth/account/CDN playback; CPU/RSS/startup/latency benchmarks. No complete-stack Pi 1 GB claim.
- Full migration parity, external F5/remote speech adapters, traffic logger and full legacy voice preparation are not implemented. Current backend is CPU-only local Vosk/Piper; config rejects unsupported speech settings.
- Historical Music source imports absent `music_meta` / `audio_play` helpers. Reconstructed payload has mock tests, not hardware parity.
- Original `app.py` has no standalone push WebSocket. `/push_subscribe` is not evidence of one.

## Local unit development

From trusted repository checkout, Go 1.24+:

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/quasard ./cmd/quasard-tools
go run ./cmd/quasard-tools --help
```

Without `native,cgo,linux`, speech initialization returns `ErrUnavailable`; macOS/Windows Go build is not a native inference test. Audio fixtures may require host FFmpeg; skipped fixtures must not be counted as passed codec/inference checks.

Canonical template is `config.example.toml` (former `config.go.example.toml` renamed, legacy Python example replaced after inspection). Copy only if `config.toml` does not already exist; preserve and migrate user config manually. File priority is explicit `--config` → `ALICE_CONFIG` → cwd `config.toml`. Relative resource paths resolve from cwd. A missing explicitly selected file is an error; missing implicit file uses defaults. Example uses loopback-friendly ports 8080/8443 and `llm.model="echo"`; built-in defaults differ.

## Linux native development / verification

Native speech build requires Linux, CGO, `-tags native`, headers/libraries in `/opt/quasard/include` and `/opt/quasard/lib`, espeak data and valid models. Sources/build inputs live in `native/`; final image build is in `Dockerfile`. No native inference runs on macOS through the no-native backend.

Model directory for `TestNativeSmoke` contains:

```text
vosk-model-small-ru-0.22/
ru_RU-dmitri-medium.onnx
ru_RU-dmitri-medium.onnx.json
silero_vad.onnx
```

In an already prepared Linux development environment, explicitly allowed model smoke command:

```sh
QUASARD_SMOKE_MODELS=/absolute/path/models go test -tags native ./internal/speech -run TestNativeSmoke -count=1 -v
```

Unset `QUASARD_SMOKE_MODELS` **skips** test; it is not success evidence. Test covers Piper non-silence, Ogg/Opus encode/decode, Vosk recognition and per-request Silero VAD. It does not prove real station compatibility. Model downloads, image builds and containers are separate opt-in work, not required to edit config/docs.

Docker Compose mounts user `config.toml` read-only at `/app/config.toml`, models/certs read-only and data writable; exposes localhost 8080/8443 only. Prepare config/models/TLS first. No automatic DNS/router/station/trust-store changes. See [README](../README.md), [networking](NETWORKING.md), [Keenetic](KEENETIC.md).

## Optional DNS

`internal/dnsproxy` serves bounded UDP/TCP forwarding and exact rewrites, disabled by default. `internal/config.DNSLoader` captures config pathname and DNS environment at startup; SIGHUP reloads DNS only. Invalid edits/bind failures retain previous listeners and policy. No settings HTTP API, cache, DHCP or general proxy. Tests use loopback ephemeral ports and fake upstreams, never public resolvers.

`compose.dns.example.yaml` is opt-in: LAN-IP-bound host53 → container5353, with a read-only config **directory** bind for atomic file replacement. Base Compose retains single-file mount/loopback HTTP ports. DNS source ACL must account for actual router/NAT source IP. Never expose DNS to WAN; readiness remains speech/HTTP-only. See [deployment and reload](../README.md#встроенный-dns).

## Distribution and useful Go tools

CI and publication jobs live in [`.github/workflows`](../.github/workflows). Linux bundles have root `./quasard` and `./quasard-tools` launchers, runtime tree and limited FFmpeg. Windows has tools-only archives, no native server artifact; `voice-prep` there needs separate FFmpeg. Release archives include `SHA256SUMS`.

- `quasard-tools certs --help`: own CA/server cert; writes files, does not install trust.
- `quasard-tools import-har --help`: sanitize local captures into device data.
- `quasard-tools activation-sim --help`, `station-sim --help`: local protocol probes, including JSON/protobuf paths.
- `quasard-tools fake-llm --help`: local SSE mock.
- `quasard-tools music-login --help`: device OAuth; real account/network access is separate opt-in.
- `quasard-tools voice-prep --help`: audio cutting only, **not** F5 cloning, ASR transcription or VAD-based best-window selection. Bundled limited FFmpeg may not decode arbitrary input formats.

Preserved `alice_server/aliceapi.desc` and `alice_server/assets/**` are required resource/reference material, not a surviving Python backend. Python code/tool removal does not close any acceptance gap listed in [parity.md](parity.md).
