# quasard

Локальный исследовательский бэкенд для Яндекс Станции, переписываемый на Go. Цель — HTTP-моки активации и конфигурации, JSON/AAPI uniproxy, голосовой цикл **Vosk → LLM → Piper**, Silero VAD и Яндекс Музыка. Только CPU; Python/GPU для текущего сервера не нужны.

> **Миграция не завершена.** На Docker Linux ARM64 проверены реальные Vosk/Piper/Silero и JSON/AAPI голосовой round-trip через симулятор. Слышимый ответ реальной колонки, внешний LLM и Music OAuth/CDN ещё не подтверждены. Это не готовая замена облака и не обещание работы на Raspberry Pi с 1 ГБ RAM. Удаление старого кода эти пробелы не закрывает — см. [контракт parity](docs/parity.md).

Неофициальный проект, не связанный с Яндексом. Только собственные устройства и исследовательские цели. Ни запуск, ни приведённые ниже команды не перенастраивают DNS, роутер, колонку или доверенные сертификаты ОС. Внешние LLM и Яндекс Музыка требуют сети; полной автономности и отсутствия исходящего трафика проект не гарантирует.

## Текущее состояние

- Go HTTP-моки активации/Glagol, private device store, JSON и descriptor-based AAPI WebSocket.
- Локальные команды, OpenAI-совместимый streaming LLM, история и отмена запросов.
- Опциональные RU/EN model ID и MCP Streamable HTTP: автоматическая ограниченная петля инструментов, опциональный allowlist — [настройка и ограничения](docs/LLM_MCP.md).
- Go-клиент Яндекс Музыки, очередь и device-code OAuth; проверка с реальным аккаунтом отдельно.
- Native Linux CPU bridge для Vosk, Piper и ONNX Runtime/Silero; нужны внешние модели.
- `quasard-tools`: сертификаты, импорт HAR, симуляторы, fake LLM, Music login, нарезка аудио.
- **Нет** GigaAM, F5/Edge/remote speech adapters и клонирования голоса. Piper не заменяет F5-клонирование. `voice-prep` только режет аудио, не расшифровывает и не выбирает образцы через VAD.

## Встроенный DNS

Опциональный DNS-forwarder в том же процессе, без отдельного AdGuard. По умолчанию выключен. Точные имена из `rewrite_hosts` получают LAN-IP сервера; остальные запросы идут в заданные upstream DNS. Это **не proxy**: музыка/CDN и остальной трафик продолжают ходить напрямую.

Пример `[dns]` есть в `config.example.toml`. Для LAN задайте реальные значения:

```toml
[dns]
enabled = true
listen = "0.0.0.0:5353"
answer_ipv4 = "192.168.148.10" # Пример: заменить на LAN-IP сервера.
rewrite_hosts = ["quasar.yandex.net", "uniproxy.alice.yandex.net"]
allow_cidrs = ["192.168.148.143/32"] # Пример: заменить на IP Станции.
upstreams = ["1.1.1.1:53", "9.9.9.9:53"]
```

Для Docker — opt-in override, не меняющий базовый Compose:

```sh
mkdir configuration
cp config.example.toml configuration/config.toml
cp compose.dns.example.yaml compose.dns.yaml
# Настроить configuration/config.toml, включая DNS, модели и HTTP/TLS.
# Базовый Compose также требует существующий ./config.toml.
export QUASARD_LAN_IP=192.168.148.10 # Заменить на LAN-IP хоста.
docker compose -f compose.yaml -f compose.dns.yaml up -d
```

Override публикует TCP/UDP53 только на указанном LAN-IP → непривилегированный5353 контейнера; nonroot/cap_drop сохраняются. Убедитесь, что53 свободен; не открывайте рекурсивный DNS в WAN. В Keenetic назначьте этот LAN-IP как DNS **только Станции** — [инструкция](docs/KEENETIC.md). Docker/NAT может менять видимый IP источника: проверяйте его до настройки ACL. Не используйте `/0` и не направляйте upstream обратно в собственный DNS.

Измените `configuration/config.toml`, затем:

```sh
docker compose -f compose.yaml -f compose.dns.yaml kill -s HUP quasard
```

SIGHUP перечитывает **только DNS**: включение/выключение, правила, ACL, upstream и listen. Ошибка оставляет прежнюю конфигурацию. Модели, HTTP и голосовые сессии не перезапускаются. Для native Linux bundle — `kill -HUP <PID>`. Docker port mappings и environment не меняются через HUP: их изменение требует пересоздания контейнера.

Directory bind `configuration/` позволяет редактору атомарно заменять файл. У обычного single-file bind `config.toml` замена через rename может оставить контейнер со старым inode; потребуется recreate либо контролируемая запись в тот же файл. Пути моделей остаются относительно `/app`, не каталога config. DNS env overrides `ALICE_DNS_*` фиксируются при старте; списки — JSON arrays, boolean — `true`/`false`.

Без `answer_ipv6` подменённые имена получают пустой AAAA; HTTPS/SVCB для них тоже не уходят upstream. DNS не меняет HTTP-порт и не решает TLS trust. TTL/кэш клиента задерживает применение ответа. `/_ready` проверяет speech/HTTP, **не DNS**; DNS отдельно проверяется через `dig @SERVER_IP quasar.yandex.net A`, AAAA, `+tcp` и постороннее имя.

Перед выключением DNS сначала верните Станции прежний профиль, иначе она останется без резолвинга. Настройка LAN и реальные запросы колонки автоматически не выполняются.

## Linux bundle

Linux AMD64/ARM64 bundle содержит сервер, tools, native runtime и ограниченный FFmpeg, **без моделей**. Это не static binary и не универсальный musl/Alpine-пакет.

Распакуйте архив своей архитектуры в новую папку, проверьте SHA-256 по `SHA256SUMS`, перейдите в корень bundle. Пример после распаковки:

```sh
cp config.example.toml config.toml
./quasard --version
./quasard-tools --version
./quasard-tools --help
mkdir -p data models certs
```

`config.toml` — личный файл, не добавляйте его в Git и не заменяйте новым примером поверх своих настроек. Если он уже существует, перенесите настройки вручную. Перед стартом подготовьте модели и TLS по следующим разделам, затем:

```sh
./quasard --config config.toml
```

Launcher запускается из корня bundle и разрешает относительные пути там. На Windows выпускаются **только `quasard-tools.exe` AMD64/ARM64**, не сервер. Для сервера на Windows нужен Linux-контейнер Docker Desktop/WSL2. Для `voice-prep` на Windows нужен отдельный полный `ffmpeg.exe`.

## Модели и TLS

Модели не включены в Git, Docker image или release bundle. Выберите доверенные источники, проверьте лицензии и контрольные суммы; не запускайте случайные установочные скрипты. Структура для примера конфигурации:

```text
models/
  vosk-model-small-ru-0.22/       # распакованная модель Vosk, не zip
  ru_RU-dmitri-medium.onnx       # Piper
  ru_RU-dmitri-medium.onnx.json  # конфигурация того же голоса Piper
alice_server/assets/silero_vad.onnx
```

Descriptor `alice_server/aliceapi.desc` и Silero asset сохраняются как ресурсы Go backend, а не Python runtime. Модель Piper и её JSON должны соответствовать друг другу. Лицензии зависимостей и моделей — отдельные от лицензии проекта, см. [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

Для локального HTTPS tools создаёт свой CA и server certificate. Команда **записывает ключи в `certs/`**, но ничего не устанавливает в ОС. Не запускайте её поверх чужих сертификатов без проверки:

```sh
./quasard-tools certs --out certs --ip 127.0.0.1
```

В checkout вместо `./quasard-tools` можно использовать `go run ./cmd/quasard-tools`. Для локальных HTTP-проверок допустимо задать `server.https_port = 0`; для станции TLS и сеть требуют отдельного решения, не автоматической настройки.

## Docker Compose: локальный стенд

Требуется Docker с Linux-контейнерами. В доверенном checkout подготовьте `config.toml`, каталоги `data`, `models`, `certs`, модели и сертификаты. Затем при осознанном разрешении сборки/запуска:

```sh
docker compose up --build
```

На Windows `./start.ps1 -Build` выполняет ту же команду; без `-Build` — `docker compose up`. Python launcher удалён, автоматического поиска API-ключей и запуска F5 больше нет.

Compose слушает **только loopback**: `http://127.0.0.1:8080` и `https://127.0.0.1:8443`. `config.toml` монтируется read-only в `/app/config.toml`, `models`/`certs` — read-only, `data` — writable. Порты в примере config должны соответствовать container ports Compose. Это стенд для компьютера, не инструкция подключения станции. Отсутствующий `config.toml` не создаётся автоматически как каталог.

Compose не подменяет LLM-настройки конфигурации. Для внешнего API задайте `llm.base_url`, `llm.model`, ключ; для env-overrides добавьте нужные `ALICE_*` через локальный Compose override. Адрес `127.0.0.1` внутри контейнера — сам контейнер, не host. В Docker Desktop локальный host API обычно доступен через `host.docker.internal`; на Linux требуется отдельная настройка, см. [сеть](docs/NETWORKING.md).

`/_ready` и `--healthcheck` проверяют readiness, включая прогрев speech; `/_status` показывает устройства и подключения. Отсутствующие модели/native runtime дают ошибку готовности, а не рабочий голосовой режим.

## Конфигурация

Единственный актуальный шаблон — [`config.example.toml`](config.example.toml). Порядок выбора файла:

1. Явный `--config /path/config.toml`.
2. Переменная `ALICE_CONFIG`, если флаг не задан.
3. `config.toml` из рабочей директории.

Явно выбранный через флаг или env отсутствующий/нечитаемый файл — ошибка. Только отсутствующий неявный `config.toml` оставляет встроенные defaults. Относительные пути моделей, TLS и данных разрешаются от **рабочей директории**, не от каталога выбранного config. Запускайте из корня checkout/bundle либо задавайте абсолютные пути.

Значения `ALICE_<SECTION>_<KEY>` перекрывают TOML, например `ALICE_SERVER_HTTP_PORT=8080`, `ALICE_LLM_API_KEY=...`. `ALICE_CONFIG` выбирает файл, а не секцию. `llm.extra_body` из TOML заменяет defaults, через env задаётся JSON-объект.

Пример включает безопасный `llm.model = "echo"`: локальная заглушка, не реальная LLM. Встроенные defaults без файла отличаются: порты 80/443 и внешняя LLM-конфигурация; используйте явный пример для стенда. Реальный API может тарифицироваться. Токены, `config.toml`, `data/`, `models/`, `certs/` и голосовые записи не должны попадать в Git.

Старый Python TOML не совместим автоматически. Loader отклоняет GigaAM/F5/Edge/remote ASR/TTS engines, legacy speech options и `asr.device = "auto"/"cuda"` вместо молчаливого выбора Vosk/Piper. Начните с нового примера, перенесите server/LLM/device/music настройки вручную. Сейчас `asr.model_path` задаёт реальный путь Vosk, `asr.model` — имя; путь Piper — `tts.model`. Legacy `server.log_traffic` не означает, что полный старый traffic logger реализован.

## Разработка и история

```sh
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/quasard-tools --help
```

Нужен Go 1.24+. Обычная сборка на macOS/Windows без Linux native backend годится для tools/протокольных тестов, но speech недоступен. Native build и реальные inference gates описаны в [GO_DEVELOPMENT.md](docs/GO_DEVELOPMENT.md).

- [Состояние переноса, gaps и восстановление Python reference](docs/parity.md).
- [Ручная настройка и ограничения сети](docs/NETWORKING.md).
- [Keenetic: отдельная инструкция](docs/KEENETIC.md).

Старый backend и Python tools удалены из активного дерева по разрешённой очистке. Reference commit `a142b19c997f3a8a4bed8f6b0aef4acd70e2436e` остаётся в Git; удаление не означает завершённого parity. Лицензия нового кода — [GPL-3.0-only](LICENSE), права на внешние ресурсы требуют отдельной проверки.
