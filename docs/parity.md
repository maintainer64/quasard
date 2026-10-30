# Go migration contract

Baseline: https://github.com/maintainer64/quasard/tree/a142b19c997f3a8a4bed8f6b0aef4acd70e2436e

Status (2026-10-30): Go server and CLI sources compile; `go test -race ./...` and `go vet ./...` passed on macOS ARM64, including shutdown cancellation/join regression coverage. Native Linux ARM64 bridge **builds and links** in the development image; real-model inference test was skipped, not passed. Final distroless ARM64 image/minimal FFmpeg build successfully (167,543,384 bytes without Vosk/Piper models), and archive audit confirms no shell/Python/toolchain; container execution was denied, so dependency loading and inference remain unverified. Full Vosk model download timed out. Historical Python is the Git reference, not an active runtime; migration is not complete. Config/README cleanup does not constitute an inference or parity pass.

## Scope

First target: Docker Linux ARM64 on Mac, CPU-only, no Python runtime. New project code: GPL-3.0-only. Native dependencies and models retain their own licenses. Tracked Python backend and tools, Python requirements and launcher behavior were removed by explicitly approved bounded cleanup before parity gates passed. This removes an active path, not acceptance obligations. Reference stays recoverable in Git; `alice_server/aliceapi.desc` and `alice_server/assets/**` remain in current tree. Ignored user config, data, models, voice samples and certificates must remain untouched.

| Source | Go destination | Required acceptance |
| --- | --- | --- |
| `alice_server/__main__.py`, `app.py` | `cmd/quasard`, `internal/httpapi` | Startup, TLS, shutdown, warmup readiness, debug and push-subscribe HTTP routes; no historical standalone push WebSocket |
| `config.py` | `internal/config` | TOML defaults and `ALICE_<SECTION>_<KEY>` overrides; migration diagnostics |
| `store.py` | `internal/store` | Device JSON compatibility, stable Glagol security, captured responses, atomic writes |
| `http_api.py` | `internal/httpapi` | Method/path/status/body golden fixtures, host-prefix routing and catchall |
| `protocol.py` | `internal/protocol` | Exact envelopes/actions and missing-field behavior |
| `aliceapi.py`, `aliceapi.desc` | `internal/aliceapi` | Descriptor-based JSON/AAPI mapping; binary round trips |
| `uniproxy.py` | `internal/session` | Voice/text/TTS/server actions; stream lifecycle, cancel, disconnect and reconnect |
| `audio.py` | `internal/audio` | BE32 framing; 16 kHz mono decode; continuous 48 kHz Ogg/Opus output |
| `asr.py`, Silero asset | `internal/speech`, `internal/session` | Resident Vosk; Silero state per request; no-speech, silence and maximum-duration deadlines |
| `llm.py` | `internal/llm` | Streaming Chat Completions, history TTL/cancellation, splitting and speech cleanup |
| `intents.py` | `internal/intents` | Table fixtures for all local/music grammar and routing precedence |
| `music.py` | `internal/music` | Go-native API/auth/download signing, per-device queue, rotor feedback and callback replay |
| `tts.py` | `internal/speech`, `internal/audio` | Resident native Piper; cancellation and resampling; external F5 adapter remains unimplemented |
| `tools/activation_sim.py`, `station_sim.py` | `cmd/quasard-tools` | Activation and JSON/protobuf station simulators |
| `tools/fake_llm.py` | `cmd/quasard-tools` | Local streaming mock; chunk/error fixtures |
| `tools/gen_certs.py`, `import_har.py` | `cmd/quasard-tools` | Own CA/cert generation and sanitized captured-response import |
| `tools/music_login.py` | `cmd/quasard-tools` | Real Music device OAuth and private local token storage |
| `tools/voice_prep.py` | `cmd/quasard-tools` | Audio preparation with documented native codecs |
| `tools/f5_server.py` | External legacy compatibility | F5 inference/cloning is not replaced by Piper cloning; excluded from CPU Go image |
| `start.ps1`, README/config/docs | Go/Docker launch docs | No implicit Python dependency or automatic station/network changes |

## Reference inspection and recovery

All Python source names in this document refer to commit `a142b19c997f3a8a4bed8f6b0aef4acd70e2436e`, not current files. Inspect without checking out or overwriting working files:

```sh
git show a142b19c997f3a8a4bed8f6b0aef4acd70e2436e:alice_server/config.py
git show a142b19c997f3a8a4bed8f6b0aef4acd70e2436e:alice_server/tts.py
git show a142b19c997f3a8a4bed8f6b0aef4acd70e2436e:tools/voice_prep.py
git show a142b19c997f3a8a4bed8f6b0aef4acd70e2436e:config.example.toml
```

For a separate offline reference copy, create a **new empty directory outside the working tree** and extract there. Check target is empty; do not extract over this repository or user data:

```sh
mkdir ../quasard-python-reference-a142b19
git archive a142b19c997f3a8a4bed8f6b0aef4acd70e2436e | tar -x -C ../quasard-python-reference-a142b19
```

This restores historical README/config/tools alongside source for inspection, not an endorsed active deployment path. Never run old dependency installers or launchers merely to inspect parity. Original `start.ps1` is recoverable with `git show`; current launcher only wraps Docker Compose.

## Configuration migration

Canonical `config.example.toml` now contains Go CPU-only settings; former `config.go.example.toml` is removed. Existing ignored `config.toml` is not rewritten. Loader file order is explicit `--config` → `ALICE_CONFIG` → cwd `config.toml`; field env overrides still win over TOML. Missing explicitly selected files fail rather than falling back silently.

ASR defaults are Vosk with `device="cpu"`; TTS default is Piper. Historical GigaAM model/device settings, F5/Edge/remote speech engines and Python-only speech options produce migration errors. Removing these options is not equivalent to reimplementing their functionality. Full traffic logging and F5 voice cloning remain gaps. Example `llm.model="echo"` is a local placeholder, not evidence of working LLM inference.

## HTTP baseline

Source: `alice_server/http_api.py`. Preserve responses and parameters, not merely route names.

- GET `/generate_204`, `/analytics/startup`, `/check_updates`.
- POST `/token`, `/report`, `/quasar_metrics/write/json/batch`, `/quasar_metrics/write/batch`, `/register`, `/push_subscribe`.
- GET `/glagol/device_list`, `/glagol/family_device_list`, `/allow_update`, `/get_sync_info`, `/get_timezone`, `/get_user_details`, `/glagol/token`.
- POST `/tv/api/v4/updates`, `/tv/api/v5/updates`, `/update_device_state`, `/glagol/check_token`, `/check_token`, `/glagol/v2.0/check_token`.
- Source `app.py`: `/uni.ws`, `/_status`, `/_say/{device_id}`, and fallback methods GET/POST/PUT/DELETE/PATCH. Go `/_push/{device_id}` is an extension, not upstream parity; standalone push WebSocket is not implemented. Earlier inventory claiming upstream push routes was incorrect.
- Framework-generated docs/OpenAPI routes must be explicitly accounted for in final inventory.

Mock station `/token` is not Yandex Music authentication. Preserve real OAuth separation.

## Wire invariants

- JSON event/directive envelopes, header casing and `refMessageId` preserved.
- Audio: big-endian uint32 stream ID plus bytes. Client IDs odd, server IDs even starting at 2.
- AAPI control: `AAPI` prefix plus descriptor-based protobuf. Do not invent `.proto` source.
- Synchronize response uses `SessionId` and omits header `messageId`.
- SpotterEnd=2 does not close microphone stream; Close=0 differs from cancellation and no-speech reasons.
- One continuous Ogg/Opus stream per response, 120 ms inter-sentence silence.
- Commands precede Music, Music precedes LLM. Preserve `sound_quiter` spelling.
- Direct Music MP3 CDN URLs and `aqw_music_*`/`@@mm_stack_engine_get_next` callbacks remain compatible.

## Verification gates

1. Source-derived, sanitized fixtures and deterministic IDs/clocks. No tokens or signed live CDN URLs in fixtures.
2. Native ARM64 smoke: Vosk + Silero + Piper load once and process Russian audio without Python or GPU.
3. `go test ./...`, `go vet ./...`, session/store race tests, native resource cleanup tests.
4. Local Docker simulations for HTTP, JSON/AAPI, voice, cancellation, errors, repeated requests and persistence.
5. Recorded cold start, RSS, CPU, ASR real-time factor and TTS latency. Model archive size is not process memory.
6. Real LLM, Music account and station audio require separate authorization. Simulation is not proof of audible device playback.

## Open checks

- Actual version and used request/signing behavior of the Python `yandex_music` dependency.
- Exact history mutation on cancellation/error; all AAPI special-case mappings.
- Native ABI compatibility and verified artifact/model hashes.
- Complete model license and rights to upstream source/firmware-derived descriptor before redistribution.
- Pi 1 GB compatibility: not claimed or measured.
