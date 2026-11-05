# Совместимость Go-версии и контракт миграции

[Настройка](../README.md) · [Разработка и тесты](GO_DEVELOPMENT.md)

Исходная реализация: [amirilivice/quasard](https://github.com/amirilivice/quasard), Python-коммит `a142b19c997f3a8a4bed8f6b0aef4acd70e2436e`. Это исторический эталон поведения, не активный runtime. Удаление Python-кода и наличие Go-сборки сами по себе не закрывают требования совместимости.

## Текущий статус: 2026-11-01

- Go — основной сервер/CLI, CPU-only runtime; GigaAM/Whisper/Vosk, Piper, Silero и аудиокодеки включаются через Linux native bridge. Модели Vosk/Piper скачиваются отдельно.
- Пройдены Go race-тесты, vet/lint и CI сборки/упаковки; в Docker Linux ARM64 проверена inference с реальными моделями и JSON/AAPI text/voice round-trip.
- На собственной **Станции Лайт (micro)** установлен собственный CA, после ручной перезагрузки владелец подтвердил голосовой ответ. Это не универсальная сертификация firmware/моделей и не проверка всех функций облака.
- Автоматическая MCP-петля проверена с локальными fake LLM/MCP и настоящим упакованным backend/Piper. Реальные платные API и production endpoints не проверялись.
- Реальный Music OAuth/account/CDN, внешняя LLM-интеграция, полная parity и low-memory характеристики остаются отдельными задачами. Нет обещания полного стека на Raspberry Pi с 1 ГБ RAM.

Ранние записи о пропущенной inference/непроверенном запуске отражали промежуточный этап, а не текущее состояние. ARM64 inference не доказывает автоматически AMD64 inference, CI codec check — не полное голосовое тестирование.

## Матрица возможностей и приёмки

| Область | Где в Go | Реализовано / проверено | Что ещё требуется |
| --- | --- | --- | --- |
| Конфиг | `internal/config`, `cmd/quasard` | TOML, env override, выбор файла, валидация; unit/race | Регрессии с реальными конфигами оператора |
| HTTP активации/Glagol | `internal/httpapi`, `internal/store` | Известные mocks, private atomic store, readiness; тесты и simulator | Полная firmware-матрица; mock `200` не облачный сервис |
| JSON/AAPI WebSocket | `internal/session`, `internal/aliceapi`, `internal/protocol` | Descriptor-based framing, директивы, отмена; JSON/AAPI Docker smoke | Длительные сессии, firmware/callback parity |
| Локальные команды | `internal/intents`, `internal/session` | Детерминированные команды, reset, volume/music directives; тесты | Все варианты распознавания и поведения устройства |
| LLM | `internal/llm` | OpenAI-совместимый SSE, история, splitting, auth/extras/timeout, RU/EN model ID; mock-тесты | Реальные провайдеры, качество/задержка, лимиты |
| MCP | `internal/mcpclient`, `internal/llm` | Streamable HTTP, несколько серверов, схемы, опциональный allowlist, автоматический бюджет/отмена; SDK tests и Docker mock smoke | Production endpoints, более широкая совместимость схем |
| ASR/VAD | `internal/speech`, `native` | CPU GigaAM/Whisper/Vosk/Silero, utterance/cancellation; ARM64 real-model smoke | AMD64 real-model, latency/RSS и микрофонные сценарии разных устройств |
| TTS/аудио | `internal/speech`, `internal/audio`, `native` | CPU Piper, chunk/resample/Opus; ARM64 real-model и ограниченный station result | Голоса/прошивки, длительное воспроизведение, двуязычие |
| Яндекс Музыка | `internal/music`, `internal/session` | HTTP-клиент, signing, очереди, device-code login, callbacks; фикстуры | Реальный аккаунт, OAuth, CDN, подписка и station playback |
| Другие музыкальные провайдеры | Пока нет | Идеи расширения отмечены в README | YouTube Music и выбор провайдера в Go не реализованы |
| DNS | `internal/dnsproxy`, `internal/config` | Exact rewrite/block, forwarding UDP/TCP, ACL/лимиты, metadata logging, SIGHUP; tests и Docker smoke | Конкретные NAT/firewall/IPv6 условия каждой сети |
| Tools | `cmd/quasard-tools`, `internal/tools` | CA, HAR import, activation/station sims, fake LLM, login, voice-prep | Реальные входные данные только по разрешению оператора |
| Упаковка | `Dockerfile`, `native`, `scripts`, workflows | Минимальный distroless, AMD64/ARM64 builds, bundles, Windows tools | Проверка конкретных tag releases и target machines |

## Что не заменено

- F5/Edge/remote TTS adapters и F5-клонирование голоса отсутствуют. Piper — локальный синтезатор, не замена клонирования.
- `voice-prep` режет аудио, но не расшифровывает, не обучает голос и не отбирает образцы через VAD.
- Нет универсального reverse proxy, HAR recorder, полного протокольного debug trace и отдельного historical push WebSocket.
- Выбор `model_ru`/`model_en` меняет LLM, не ASR/TTS. Русские Vosk/Piper не становятся двуязычными.
- DNS rewrite не гарантирует отсутствие исходящего трафика/телеметрии и не отключает TLS verification.

## Исторический Python: посмотреть без изменения рабочего дерева

Имена `.py` ниже относятся к baseline, не к текущим файлам:

```sh
git show a142b19c997f3a8a4bed8f6b0aef4acd70e2436e:alice_server/config.py
git show a142b19c997f3a8a4bed8f6b0aef4acd70e2436e:alice_server/tts.py
git show a142b19c997f3a8a4bed8f6b0aef4acd70e2436e:tools/voice_prep.py
git show a142b19c997f3a8a4bed8f6b0aef4acd70e2436e:config.example.toml
```

Для отдельной копии создайте **новый пустой каталог вне checkout**, не распаковывайте поверх исходников или личных данных:

```sh
mkdir ../quasard-python-reference-a142b19
git archive a142b19c997f3a8a4bed8f6b0aef4acd70e2436e | tar -x -C ../quasard-python-reference-a142b19
```

Это копия для изучения, не рекомендация запускать старые скрипты или устанавливать их зависимости. Личные конфиги, сертификаты, токены, модели и `.dev` восстанавливать/публиковать из неё не нужно.

## Условия принятия дальнейших изменений

1. Сохранять HTTP/JSON/AAPI наблюдаемые контракты, изоляцию устройств и приватные atomic writes.
2. Отменять и завершать workers при новом запросе/остановке; не исполнять усечённые tool calls и не повторять неоднозначно завершённые действия автоматически.
3. Отличать fixture tests, native builds, inference и результат настоящей станции в отчётах.
4. Любые реальные аккаунты, внешние платные API, SSH/CA/DNS/router changes выполнять только с разрешением владельца; не подменять их mock-тестами в заявлениях.
5. Для выпуска сохранять лицензии/notice зависимостей и моделей, отдельно проверять права на исторические артефакты: [THIRD_PARTY_NOTICES](../THIRD_PARTY_NOTICES.md). Новый GPL-3.0-only не перелицензирует чужую реализацию.
