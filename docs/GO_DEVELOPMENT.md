# Разработка и сборка Go-версии

[Настройка для пользователя](../README.md) · [Статус совместимости](parity.md) · [Диагностика](TRAFFIC_CAPTURE.md)

Основной runtime — Go и CPU native-библиотеки. Исторический Python-код остаётся доступен в Git, но не нужен для запуска текущего образа. Сборка и тесты не означают полную совместимость с облаком Яндекса или всеми Станциями.

## Структура

| Каталог | Содержимое |
| --- | --- |
| `cmd/quasard` | Сервер, запуск слушателей, сигналы, DNS reload |
| `cmd/quasard-tools` | Утилиты оператора и симуляторы |
| `internal/config` | TOML, `ALICE_*`, валидация и загрузчик DNS |
| `internal/session`, `internal/aliceapi`, `internal/protocol` | JSON/AAPI WebSocket и сессии |
| `internal/llm`, `internal/mcpclient` | OpenAI-совместимый LLM и автоматическая MCP-петля |
| `internal/music` | Яндекс Музыка, очередь, авторизация |
| `internal/speech`, `internal/audio`, `native` | Vosk/Piper/Silero, Opus, CPU bridge |
| `internal/httpapi`, `internal/store`, `internal/dnsproxy` | HTTP-моки, приватные данные, DNS |
| `alice_server/aliceapi.desc`, `alice_server/assets` | Сохранённые дескрипторы и Silero VAD, не Python-runtime |

Личные `configuration/`, `config.toml`, `data/`, `models/`, `certs/`, `.dev/`, аудио и HAR не публикуются. Воспроизводимые команды первичной настройки находятся в README и не требуют локального `.dev`.

## Быстрые проверки исходников

Версия Go задаётся в `go.mod` (минимум Go 1.24). Линтер должен поддерживать используемый Go toolchain; старый golangci-lint может выдавать typecheck-ошибки в стандартной библиотеке нового Go.

```sh
go test -race -count=1 -timeout=90s ./...
go vet ./...
golangci-lint run ./...
git diff --check
```

Без `-tags native` эти тесты проверяют Go-логику, фикстуры и fake speech; они **не доказывают работу реальных моделей**. Утилиты без native-зависимостей можно посмотреть так:

```sh
go run ./cmd/quasard-tools --help
go run ./cmd/quasard-tools certs --help
go run ./cmd/quasard-tools fake-llm --help
```

Go-бинарник сервера без native speech не является готовой заменой Docker-образа с Vosk/Piper. Для реального голоса используйте штатную Linux native-сборку.

## Docker: готовый образ или сборка

Для колонки основной путь — [README](../README.md): готовый образ, один сервис, локальный override в `configuration/`, публикация 53/80/443. Базовый `compose.yaml` сам по себе публикует только loopback 8080/8443 и выбирает `quasard-go:local`.

Локальная сборка того же runtime:

```sh
docker build -t quasard-go:local .
```

`Dockerfile` собирает CPU native bridge, Go-бинарники с `-tags native`, ограниченный набор аудиокодеков и минимальный distroless runtime. В финальном образе нет shell, Python, package manager или Go toolchain. Поэтому `docker exec ... sh` не подходит для диагностики; используйте CLI, HTTP и логи. Не добавляйте shell в production-образ ради одной проверки.

Сборка загружает исходники/зависимости и требует сети. Большие Vosk/Piper-модели не включены: их устанавливает оператор по README. Silero VAD уже включён. Первый native build может быть долгим.

Для запуска локально собранного образа по station-рецепту выставьте `IMAGE=quasard-go:local` вместо `dev-1`; не выполняйте `docker pull` для локального тега. Сначала подготовьте конфиг, модели, права и сертификаты. Не используйте параллельно два сервиса на одних 53/80/443.

## Утилиты

Все команды доступны в готовом образе через entrypoint `/usr/local/bin/quasard-tools`:

```sh
IMAGE=ghcr.io/maintainer64/quasard:dev-1
docker run --rm --entrypoint /usr/local/bin/quasard-tools "$IMAGE" --help
```

| Команда | Для чего |
| --- | --- |
| `certs` | Собственный CA и leaf; `--out`, повторяемые `--ip`/`--host` |
| `activation-sim` | HTTP-моки активации, `--base`, явный `--verify` |
| `station-sim` | JSON/AAPI текст или Ogg, `--url`, `--text`, `--proto`, `--verify`, `--out` |
| `fake-llm` | Локальный streaming mock без платных запросов |
| `music-login` | Внешняя device-code авторизация; `--token-file` для приватного токена |
| `import-har` | Локальный импорт поддерживаемых ответов из HAR |
| `voice-prep` | Нарезка аудио; не обучение, не ASR-разметка и не отбор через VAD |

Всегда сверяйте дополнительные флаги через `<command> --help`. Текстовые/голосовые симуляторы могут сохранять приватные аудиоответы. Примеры — [диагностика](TRAFFIC_CAPTURE.md). При TLS-тестах явно используйте `--verify` и собственный публичный CA в процессном trust store; не превращайте отключение TLS в инструкцию подключения станции.

## Конфигурация и reload

Выбор файла: явный `--config` → `ALICE_CONFIG` → `config.toml` в рабочей директории. Относительные пути ресурсов считаются **от рабочей директории**, не от расположения TOML. Для Docker-пути в README абсолютные.

`ALICE_<SECTION>_<KEY>` переопределяет параметры; сложные значения передаются в поддерживаемой JSON-форме. Окружение host не появляется внутри контейнера автоматически: нужные env следует явно передать в Compose. Не печатайте ключи, токены и полный rendered config в публичные логи.

- SIGHUP перечитывает только DNS из исходного пути конфигурации и с сохранёнными startup env override.
- Ошибка в новом DNS-конфиге не заменяет последнюю рабочую политику.
- Для изменения LLM/MCP/ASR/TTS нужен перезапуск, для env/mount/ports — пересоздание контейнера.
- Directory bind конфигурации позволяет видеть атомарные замены файла; одиночный file bind может остаться на старом inode.

## CI, образы и бандлы

Workflow-файлы в `.github/workflows/` — источник точных условий публикации:

- `checks.yml` / `ci.yml`: Go checks, lint и проверки сборки.
- `images.yml`: AMD64/ARM64 native images и проверки упаковки.
- `publish.yml`: PR из этого же репозитория после проверок публикует `ghcr.io/maintainer64/quasard:dev-<номер PR>`; текущий пользовательский пример — `dev-1`. PR из внешнего fork не получает право публикации.
- `release.yml`: tag-release workflow, Linux-бандлы и Windows tools. Фактическую доступность конкретного артефакта проверяйте в [Releases](https://github.com/maintainer64/quasard/releases) и GitHub Actions; наличие workflow не равно проверенному релизу.

Linux-бандл использует включённый runtime и launcher `scripts/bundle-launch.sh`; модели/конфиг/сертификаты оператор готовит отдельно. Windows tools — утилиты, не обещание native Windows Vosk/Piper-сервера. Для серверного пути на Windows используйте Linux Docker/WSL и отдельно проверьте LAN-доступ к опубликованным портам.

## Что действительно проверено

Актуальный срез на 2026-11-01:

- Go race-тесты, vet и lint прошли; тесты включают DNS, MCP, LLM auth/timeout/extras, бюджеты и отмену цепочки.
- В Docker Linux ARM64 прошли реальные Piper → Ogg → Vosk, Silero и JSON/AAPI text/voice round-trip; HTTP/TLS, DNS UDP/TCP, forwarding и SIGHUP.
- Автоматический MCP в упакованном backend проверен с локальными fake LLM и MCP через JSON и AAPI: два инструмента подряд, окончательный ответ и Piper-аудио. Это не платный production endpoint.
- На собственной Лайт (micro) установлен свой CA; владелец после перезагрузки подтвердил работу голосового ответа. Это ограниченный результат для конкретного устройства.
- CI проверяет native-сборки/загрузку зависимостей/кодеки/упаковку, но не реальный аккаунт Music и не всю модельную inference-матрицу.

Осталось: полная parity, реальный внешний LLM и production MCP, Music OAuth/CDN, двуязычный ASR/TTS, полные latency/RSS/startup-замеры, real-model AMD64 и low-memory устройства. Не заменяйте эти пункты успешной компиляцией. Контракт и исторический baseline — [parity.md](parity.md).
