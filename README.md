# quasard — локальный бэкенд для Яндекс Станции

Go-сервер, который принимает часть запросов Станции вместо облака: активация и конфигурация, голосовой WebSocket,
локальные команды и цепочка **GigaAM/Vosk/Whisper → LLM → Piper** с Silero VAD. Работает на CPU; Python и GPU в контейнере не нужны.

**Основной сценарий:** один готовый Docker-контейнер на компьютере, собственный сертификат в своей колонке и DNS этого
компьютера в настройках Keenetic. Никаких отдельных LAN-стеков, NAT/DNAT, установки CA на роутер или перенаправления
HTTPS через роутер не требуется.

> Проект экспериментальный, неофициальный и не связан с Яндексом. На собственной **Станции Лайт (micro)** установлен
> собственный CA; после перезагрузки владелец подтвердил работу голосового ответа. В Docker Linux ARM64 проверены реальные
> Vosk/Piper/Silero и JSON/AAPI. Это не гарантия для любой прошивки, не полная замена облака и не обещание работы на
> Raspberry Pi с 1 ГБ RAM. Внешний LLM и реальное воспроизведение Яндекс Музыки требуют отдельной проверки.
> Подробности — [статус совместимости](docs/parity.md).

## Содержание

- [Как устроено подключение](#как-устроено-подключение)
- [1. Подготовить компьютер и получить образ](#1-подготовить-компьютер-и-получить-образ)
- [2. Скачать модели](#2-скачать-модели)
- [3. Создать собственные сертификаты](#3-создать-собственные-сертификаты)
- [4. Настроить сервер](#4-настроить-сервер)
- [5. Запустить один контейнер](#5-запустить-один-контейнер)
- [6. Установить CA на Станцию Лайт micro](#6-установить-ca-на-станцию-лайт-micro)
- [7. Указать DNS в Keenetic](#7-указать-dns-в-keenetic)
- [8. Проверить подключение и голос](#8-проверить-подключение-и-голос)
- [LLM, MCP и музыка](#llm-mcp-и-музыка)
- [Управление, диагностика и откат](#управление-диагностика-и-откат)
- [Где что лежит](#где-что-лежит)
- [Авторы и идеи](#авторы-и-идеи)

## Как устроено подключение

```text
Станция → DNS компьютера с quasard (порт 53 UDP/TCP)
               ├─ quasar.yandex.net          → LAN IP компьютера
               ├─ uniproxy.alice.yandex.net  → LAN IP компьютера
               └─ остальные имена           → обычные upstream DNS

Станция → компьютер:80/443 → HTTP-моки / голосовой WebSocket
                                → GigaAM/Vosk/Whisper → LLM → Piper → ответ колонки
```

DNS только сообщает IP. Он не проксирует весь интернет-трафик Станции и не отменяет проверку TLS. Поэтому нужны **оба
шага**: доверие собственному CA на колонке и DNS на ваш сервер. Внешние LLM, MCP и Яндекс Музыка могут обращаться в
интернет. Полную автономность и отключение всей телеметрии проект не гарантирует.

## 1. Подготовить компьютер и получить образ

Нужно:

- Linux или macOS с Docker; Docker Desktop подходит для Mac, включая Apple Silicon.
- Docker Compose **2.24.4+**: ниже используются `ports: !override` и `volumes: !override`.
- Постоянный LAN IPv4 компьютера и желательно закреплённый DHCP-адрес Станции.
- Свободные порты компьютера: **53 UDP/TCP, 80 TCP, 443 TCP**.
- Для команд подготовки: POSIX shell, Git, `curl`, OpenSSL, `unzip`; для проверки — `dig`, `ssh`. На Windows удобнее
  выполнять этот сценарий в WSL; отдельные Windows tools описаны в [документе разработки](docs/GO_DEVELOPMENT.md).
- Для установки CA — **уже имеющийся разрешённый root SSH-доступ к своей Станции**. На проверенной Лайт (micro) это порт
  2222. Способы получения доступа и обхода защиты здесь не рассматриваются.

Не открывайте эти порты в интернет. Компьютер должен оставаться включённым и доступным Станции; сон ноутбука остановит и
DNS. Если порт занят, сначала разберитесь, каким сервисом: не останавливайте чужие службы вслепую.

```sh
git clone --branch go-rewrite https://github.com/maintainer64/quasard.git
cd quasard

export IMAGE=ghcr.io/maintainer64/quasard:dev-1
export SERVER_IP=192.168.1.10    # ЗАМЕНИТЬ: LAN IP компьютера, не контейнера
export STATION_IP=192.168.1.20   # ЗАМЕНИТЬ: IP своей колонки
export QUASARD_UID="$(id -u)"
export QUASARD_GID="$(id -g)"
test "$QUASARD_UID" -ne 0       # Основной сценарий запускает контейнер не от root

docker compose version
docker pull "$IMAGE"
docker run --rm "$IMAGE" --version
```

`dev-1` — изменяемый образ [PR №1](https://github.com/maintainer64/quasard/pull/1), не стабильный релиз. Образы
публикуются для Linux AMD64/ARM64. Для воспроизводимого развёртывания зафиксируйте подходящий digest вместо изменяемого
тега. Сборка из исходников и Linux-бандлы — [здесь](docs/GO_DEVELOPMENT.md).

Все следующие команды выполняйте **из корня репозитория**, в одном shell с указанными переменными. В новой сессии
экспортируйте их снова. Не перезаписывайте существующие рабочие файлы: сначала сохраните копии и сравните настройки.

```sh
umask 077
mkdir -p configuration data models certs
```

`configuration/`, `data/`, `models/`, `certs/` исключены из Git. В них будут ваши настройки, токены, модели и ключи.
Ничего из этого не нужно прикладывать к issue или публиковать.

## 2. Скачать модели

Для автоматического русского/английского распознавания доступен
[Whisper с language=auto](docs/ASR_RU_EN.md). Для русского доступен
[GigaAM v3_e2e_rnnt через native ONNX Runtime](docs/ASR_GIGAAM.md). Пример ниже оставляет прежний Vosk.

В готовом образе есть библиотеки GigaAM/Vosk/Whisper/Piper и Silero VAD, готовая GigaAM v3_e2e_rnnt; голоса Piper и другие ASR-модели скачиваются отдельно. Начальный
вариант: `vosk-model-small-ru-0.22` и русский голос `ru_RU-dmitri-medium`.

Ниже закреплены ревизии файлов и SHA-256 из проверенного набора артефактов. Это контроль целостности, не независимая
подпись поставщика. Перед использованием и распространением изучите лицензии
моделей: [Vosk](https://huggingface.co/rhasspy/vosk-models), [Piper voices](https://huggingface.co/rhasspy/piper-voices).

Скачивания и распаковку делаем в новые пустые каталоги, не поверх исходников или действующей модели:

```sh
(
set -eu
STAGE=$(mktemp -d "$PWD/models/.download-XXXXXXXX")
mkdir "$STAGE/vosk-download" "$STAGE/vosk-extract" \
  "$STAGE/piper-model" "$STAGE/piper-config"

curl --fail --location --output "$STAGE/vosk-download/model.zip" \
  'https://huggingface.co/rhasspy/vosk-models/resolve/8e5f85a35b402c35022b5af62c101dd6a06d0219/ru/vosk-model-small-ru-0.22.zip'
curl --fail --location --output "$STAGE/piper-model/ru_RU-dmitri-medium.onnx" \
  'https://huggingface.co/rhasspy/piper-voices/resolve/b4e358ffcb232591bf73a540e70f7de528e4174a/ru/ru_RU/dmitri/medium/ru_RU-dmitri-medium.onnx'
curl --fail --location --output "$STAGE/piper-config/ru_RU-dmitri-medium.onnx.json" \
  'https://huggingface.co/rhasspy/piper-voices/resolve/b4e358ffcb232591bf73a540e70f7de528e4174a/ru/ru_RU/dmitri/medium/ru_RU-dmitri-medium.onnx.json'

# OpenSSL есть и в Linux, и в macOS; после любой ошибки дальше не продолжать.
check_sha256() {
  actual=$(openssl dgst -sha256 -r "$1" | cut -d ' ' -f 1)
  test "$actual" = "$2"
}
check_sha256 "$STAGE/vosk-download/model.zip" \
  961d5ff98a17f4aa6de69864d0aa71fa5bac682301d2b5d17a3f24c5c99a46d4 || exit 1
check_sha256 "$STAGE/piper-model/ru_RU-dmitri-medium.onnx" \
  f073356ebc4bd0f80c5af58df2953a5988bd5bdab1eb38635ce960b071fbefcb || exit 1
check_sha256 "$STAGE/piper-config/ru_RU-dmitri-medium.onnx.json" \
  667ef3117bc642c2892dff7690d8bdc8ca4228aeaa783b2dc1416df632855e0d || exit 1

unzip -Z1 "$STAGE/vosk-download/model.zip" > "$STAGE/vosk-entries.txt"
# Только ожидаемый корень, без выхода из каталога и символических ссылок.
if grep -Ev '^vosk-model-small-ru-0\.22(/|$)' "$STAGE/vosk-entries.txt" || \
   grep -E '(^|/)\.\.?(/|$)|\\' "$STAGE/vosk-entries.txt"; then
  printf '%s\n' 'Неожиданное содержимое ZIP; остановка.' >&2
  exit 1
fi
unzip -Z -l "$STAGE/vosk-download/model.zip" > "$STAGE/vosk-attributes.txt"
if grep -E '^l' "$STAGE/vosk-attributes.txt"; then exit 1; fi
unzip -q "$STAGE/vosk-download/model.zip" -d "$STAGE/vosk-extract"
test -s "$STAGE/vosk-extract/vosk-model-small-ru-0.22/am/final.mdl" || exit 1

# Не заменять существующие модели без отдельного решения.
test ! -e models/vosk-model-small-ru-0.22 || exit 1
test ! -e models/ru_RU-dmitri-medium.onnx || exit 1
test ! -e models/ru_RU-dmitri-medium.onnx.json || exit 1
mv "$STAGE/vosk-extract/vosk-model-small-ru-0.22" models/
mv "$STAGE/piper-model/ru_RU-dmitri-medium.onnx" models/
mv "$STAGE/piper-config/ru_RU-dmitri-medium.onnx.json" models/
printf 'Модели установлены; staging: %s\n' "$STAGE"
)
```

При ошибке `curl`, проверки или распаковки остановитесь, не переходите к следующей команде. Остатки скачивания остаются
в `$STAGE` для проверки; готовый сервер их не использует. Не запускайте скрипты из скачанных архивов. Отдельно скачивать
`alice_server/assets/silero_vad.onnx` не нужно: он уже включён в образ.

## 3. Создать собственные сертификаты

Генератор находится в том же готовом образе:

```sh
docker run --rm --user "$QUASARD_UID:$QUASARD_GID" \
  --mount "type=bind,src=$PWD/certs,dst=/work" \
  --entrypoint /usr/local/bin/quasard-tools \
  "$IMAGE" certs --out /work --ip "$SERVER_IP"

openssl x509 -in certs/ca.crt -noout -subject -dates -fingerprint -sha256
openssl verify -CAfile certs/ca.crt certs/server.crt
```

| Файл               | Назначение                     | Куда передавать                                                     |
|--------------------|--------------------------------|---------------------------------------------------------------------|
| `certs/ca.crt`     | Публичный сертификат вашего CA | Только его добавляем в доверенные на своей Станции                  |
| `certs/ca.key`     | Приватный ключ CA              | Хранить закрыто; не передавать ни станции, ни серверному контейнеру |
| `certs/server.crt` | Сертификат HTTPS-сервера       | Read-only mount в контейнер                                         |
| `certs/server.key` | Приватный ключ HTTPS-сервера   | Read-only mount в контейнер, не публиковать                         |

Генератор создаёт каталог с правами `0700`, файлы — `0600`. Основной запуск ниже использует UID/GID владельца этих
файлов, поэтому не нужно делать ключ общедоступным через `chmod 644`. В SAN уже есть основные имена Станции; `--ip`
добавляет IP компьютера.

**Повторная генерация сохраняет валидную пару CA, но перезаписывает сертификат и ключ сервера.** Не запускайте её без
необходимости на рабочем комплекте. Для замены leaf остановите сервер, сохраните прежние файлы, сгенерируйте и проверьте
новые, затем пересоздайте контейнер с файловыми mounts. Потеря/смена CA потребует обновить доверие на станции. Не
используйте чужие скомпрометированные CA и не отключайте TLS-проверку.

## 4. Настроить сервер

Если `configuration/config.toml` ещё нет, скопируйте шаблон:

```sh
test ! -e configuration/config.toml && cp config.example.toml configuration/config.toml
```

Замените соответствующие секции, **не добавляйте вторую `[server]` или `[dns]` в конец**. Минимальная конфигурация для
первого запуска:

```toml
[server]
host = "0.0.0.0"
http_port = 8080
https_port = 8443
certfile = "/app/server.crt"
keyfile = "/app/server.key"
data_dir = "/app/data"
public_uniproxy_url = "wss://uniproxy.alice.yandex.net/uni.ws"
log_level = "INFO"

[dns]
enabled = true
listen = "0.0.0.0:5353"
answer_ipv4 = "192.168.1.10" # ЗАМЕНИТЬ на SERVER_IP
answer_ipv6 = ""
rewrite_hosts = ["quasar.yandex.net", "uniproxy.alice.yandex.net"]
block_hosts = []
log_queries = false
allow_cidrs = ["192.168.1.20/32"] # ЗАМЕНИТЬ на STATION_IP/32
upstreams = ["1.1.1.1:53", "8.8.8.8:53"]
ttl = 30
timeout_ms = 2000

[asr]
engine = "vosk"
model_path = "/app/models/vosk-model-small-ru-0.22"
vad_model_path = "/app/alice_server/assets/silero_vad.onnx"
device = "cpu"
language = "ru"

[tts]
engine = "piper"
model = "/app/models/ru_RU-dmitri-medium.onnx"
config_path = "/app/models/ru_RU-dmitri-medium.onnx.json"

[llm]
base_url = "echo"
model = "echo"
extra_body = {}

[mcp]
enabled = false

[music]
enabled = false
```

На первом этапе `echo` повторяет текст и не обращается к внешней модели — это проверка подключения и речи, не
полноценный умный ответ. После проверки замените LLM-настройки, как описано [ниже](#llm-mcp-и-музыка).

**Важно про DNS:** upstream должен быть независимым DNS, а не Keenetic, который сам отправляет запросы обратно в
quasard. Иначе получится петля. `allow_cidrs` — ACL источника запросов. Docker Desktop может показывать NAT/gateway
вместо IP колонки: в таком случае нужна проверка фактического источника и точечная правка ACL, а не `0.0.0.0/0`.
Проверка с самого компьютера тоже требует разрешить его фактический адрес.
Подробности — [сеть и DNS](docs/NETWORKING.md).

Пути в конфиге разрешаются от рабочей директории процесса, не от каталога конфигурации. Поэтому здесь указаны абсолютные
контейнерные `/app/...`.

## 5. Запустить один контейнер

Базовый `compose.yaml` рассчитан на локальную разработку: он выбирает локальный образ и публикует 8080/8443 только на
loopback. Для готового образа и реальной Станции создайте **локальный** `configuration/compose.station.yaml`:

```yaml
services:
  quasard:
    image: "${IMAGE:?Export IMAGE}"
    user: "${QUASARD_UID:?Export QUASARD_UID}:${QUASARD_GID:?Export QUASARD_GID}"
    environment:
      ALICE_CONFIG: /app/configuration/config.toml
    ports: !override
      - "${SERVER_IP:?Export SERVER_IP}:80:8080"
      - "${SERVER_IP:?Export SERVER_IP}:443:8443"
      - "${SERVER_IP:?Export SERVER_IP}:53:5353/udp"
      - "${SERVER_IP:?Export SERVER_IP}:53:5353/tcp"
    volumes: !override
      - type: bind
        source: ./configuration
        target: /app/configuration
        read_only: true
        bind:
          create_host_path: false
      - type: bind
        source: ./data
        target: /app/data
        bind:
          create_host_path: false
      - type: bind
        source: ./models
        target: /app/models
        read_only: true
        bind:
          create_host_path: false
      - type: bind
        source: ./certs/server.crt
        target: /app/server.crt
        read_only: true
        bind:
          create_host_path: false
      - type: bind
        source: ./certs/server.key
        target: /app/server.key
        read_only: true
        bind:
          create_host_path: false
```

Это **тот же сервис** из корневого Compose, не второй сервер. Относительные bind-пути считаются от первого файла
`compose.yaml`, то есть от корня проекта. `!override` заменяет списки целиком: приватный `ca.key` не попадёт в контейнер
через исходный mount каталога `certs`.

```sh
docker compose -f compose.yaml -f configuration/compose.station.yaml config
docker compose -f compose.yaml -f configuration/compose.station.yaml up -d --no-build
docker compose -f compose.yaml -f configuration/compose.station.yaml ps
docker compose -f compose.yaml -f configuration/compose.station.yaml logs -f quasard
```

`Ctrl+C` завершит просмотр логов, не контейнер. Явные `-f` не подхватывают случайный `compose.override.yaml`.
`--no-build` использует скачанный образ, а не запускает сборку. Каталог `data` должен быть доступен выбранному UID/GID
на запись; модели и leaf-файлы — на чтение. При ошибке прав исправляйте владельца/путь, не открывайте приватные ключи
всем.

Ожидайте сообщения о готовности локальной речи и слушателей. Проверка без изменения колонки:

```sh
curl --fail --noproxy '*' "http://$SERVER_IP/_ready"
curl --fail --noproxy '*' --cacert certs/ca.crt \
  --resolve "quasar.yandex.net:443:$SERVER_IP" \
  https://quasar.yandex.net/_ready
```

Сервер открывает и административные HTTP-пути. LAN bind не ограничивает источник соединений: разрешайте доступ только
доверенным клиентам, проверьте firewall и не делайте WAN port forwarding. Docker Desktop может отдельно запросить
разрешение на привилегированные порты/входящие подключения; не отключайте firewall целиком.

## 6. Установить CA на Станцию Лайт micro

На проверенном собственном устройстве использовалось следующее:

1. Root SSH на порту **2222**.
2. Резервная копия штатного набора CA локально и в отдельном каталоге `/data` на колонке.
3. Передача **только `certs/ca.crt`**.
4. Добавление своего CA к существующему bundle:
   **`/system/vendor/quasar/certs/ca-certificates.crt`**.
5. Кратковременный remount `/` в read-write, атомарная замена подготовленного bundle с сохранением штатных CA и возврат
   в read-only.
6. Проверка результата и ручная перезагрузка Станции для перечитывания доверенных CA.

**Это изменение системного доверия своей колонки. Ошибка может нарушить её соединения. Не заменяйте штатный bundle одним
вашим сертификатом, не передавайте `ca.key`/`server.key` и не выполняйте запись без резервной копии.**

Подробные команды с проверками, fingerprint, установкой и откатом: *
*[Собственный CA на Станции Лайт (micro)](docs/STATION_CA.md)**. Сначала прочитайте документ целиком. Если нет
собственного SSH-доступа или путь/раздел отличаются, остановитесь: этот рецепт не гарантирован для другой прошивки.
Установка CA на Mac, Keenetic и другие домашние устройства для этого сценария не требуется.

## 7. Указать DNS в Keenetic

**На роутере достаточно прописать LAN IP компьютера как DNS для Станции.** Всё остальное — сервер, модели и доверенный
CA — подготовлено выше.

1. В списке клиентов Keenetic найдите свою Станцию и закрепите её IP.
2. Если прошивка поддерживает отдельный DNS-профиль клиента, создайте профиль с единственным DNS **`SERVER_IP`** и
   назначьте его Станции. Названия меню зависят от KeeneticOS.
3. Если меняете DNS всей домашней сети, учитывайте, что теперь от компьютера будут зависеть и остальные устройства; DNS
   ACL сервера должен соответствовать выбранному охвату.
4. Не добавляйте публичный DNS как «второй» в тот же профиль: клиенты могут использовать его параллельно, обходя
   подмену.
5. Переподключите или перезагрузите колонку, чтобы она получила DNS и сбросила кеш.

**Не нужны:** маршруты к доменам, DNAT 53/443, HTTPS-прокси на Keenetic, установка туда CA или самого quasard. Пошаговые
проверки и откат — [Keenetic](docs/KEENETIC.md).

## 8. Проверить подключение и голос

Сначала проверьте DNS из разрешённого ACL клиента. Для проверки с компьютера может понадобиться временно разрешить его
фактический исходный IP:

```sh
dig @"$SERVER_IP" quasar.yandex.net A +short
dig @"$SERVER_IP" uniproxy.alice.yandex.net A +tcp +short
dig @"$SERVER_IP" uniproxy.alice.yandex.net AAAA
dig @"$SERVER_IP" example.org A
```

Первые два ответа — `SERVER_IP`. AAAA для подменённого имени — `NOERROR` без AAAA-ответа, а не чужой IPv6. `example.org`
должен разрешаться через upstream. Проверьте оба имени по UDP и TCP, если диагностируете частичную работу.

После DNS/CA спросите колонку что-нибудь. В `echo` ожидается повтор распознанного текста и тестовая фраза. Это проверяет
речь и соединение, но не внешний LLM. Затем подключите модель. Для временного наблюдения DNS включите
`log_queries = true` и перечитайте конфиг; не публикуйте DNS-имена/аудио/логи без проверки.

Если HTTPS работает через `curl --cacert`, но колонка не соединяется, проверьте её DNS, доверие к **тому же CA**, дату
устройства и применимость рецепта к прошивке. Не лечите ошибку отключением проверки TLS. `200 {"status":"ok"}` на
неизвестном пути — заглушка, не доказательство полной совместимости.

## LLM, MCP и музыка

### Внешний или локальный LLM

Отдельные ключи, модели и параметры для колонок задаются через
`[devices."DEVICE_ID".llm]`; пример и правила наследования —
[в настройках LLM](docs/LLM_MCP.md#настройки-по-device_id).
[Диагностический режим](docs/DIAGNOSTICS.md) показывает этапы обработки,
распознанный текст и задержки; DEBUG можно временно переключать SIGUSR1.

После проверки `echo` замените секцию `[llm]`, например:

```toml
[llm]
base_url = "https://api.groq.com/openai/v1"
model = "qwen/qwen3.8-27b"
api_key = "" # Передайте GROQ_API_KEY контейнеру или заполните приватный конфиг
extra_body = { reasoning_effort = "none" }
model_ru = ""
model_en = ""
max_tool_calls = 4
```

Это пример OpenAI-совместимого endpoint, не обязательный провайдер. Доступность model ID и тарифы проверяйте у своего
поставщика; внешние вызовы могут стоить денег. Для `GROQ_API_KEY` добавьте в `environment` локального override
`GROQ_API_KEY: "${GROQ_API_KEY:?Export GROQ_API_KEY}"`, экспортируйте ключ в shell и пересоздайте сервис. Вывод
`docker compose config` может содержать секреты — не публикуйте его.

Модель на компьютере доступна контейнеру не через `127.0.0.1` компьютера: в Docker Desktop обычно используется
`host.docker.internal`; на Linux нужен отдельно настроенный доступ к host или общей Docker-сети. `model_ru`/`model_en`
выбирают **LLM**, а не язык Vosk и не голос Piper.

### MCP-инструменты

Серверы подключаются опционально в `[mcp]`. Модель вызывает инструменты **автоматически**, цепочкой, без голосового
подтверждения. Отсутствующий `allowed_tools` или `[]` разрешает все обнаруженные инструменты настроенного сервера;
непустой список ограничивает доступ. Лимит цепочки — `max_tool_calls`, по умолчанию 4.

Инструменты могут менять внешние данные сразу: подключайте только доверенные endpoints с нужными правами. Подробная
конфигурация, токены, ограничения и RU/EN — **[LLM и MCP](docs/LLM_MCP.md)**.

### Музыка

В Go реализован клиент **Яндекс Музыки**, очередь и device-code login. Реальное OAuth/CDN-воспроизведение с аккаунтом
ещё нужно проверять отдельно; это не заявленная гарантия работы подписки на любой станции.

```sh
docker run --rm -it --user "$QUASARD_UID:$QUASARD_GID" \
  --mount "type=bind,src=$PWD/data,dst=/work" \
  --entrypoint /usr/local/bin/quasard-tools "$IMAGE" \
  music-login --token-file /work/music_token.json
```

Команда обращается к внешней авторизации: выполняйте её, только если хотите подключить свой аккаунт. Токен сохраняется в
приватном `data`; затем включите `[music] enabled = true` и пересоздайте контейнер. Точные флаги можно посмотреть через
`music-login --help`.

**YouTube Music и переключение нескольких музыкальных провайдеров в этом Go-runtime пока не реализованы.** Идеи такого
расширения есть в форке Serewasfera; благодарность ниже не означает готовую поддержку.

## Управление, диагностика и откат

Везде используйте те же два Compose-файла и переменные окружения:

```sh
# Логи
docker compose -f compose.yaml -f configuration/compose.station.yaml logs -f quasard

# Перечитать только DNS (включая block_hosts и log_queries)
docker compose -f compose.yaml -f configuration/compose.station.yaml kill -s HUP quasard

# Применить изменения LLM/MCP/TTS, mounts или переданных env
docker compose -f compose.yaml -f configuration/compose.station.yaml up -d --no-build --force-recreate
```

Невалидный DNS reload сохраняет последнюю рабочую политику. Изменение публикации портов требует пересоздания контейнера.
Пример монтирует **каталог** конфигурации: атомарная замена `config.toml` редактором будет видна процессу.

| Симптом                     | Куда смотреть                                                                |
|-----------------------------|------------------------------------------------------------------------------|
| Не запускается HTTPS        | Пути leaf-файлов, соответствие пары, права UID/GID, занятый 443              |
| DNS timeout/refused         | Публикация UDP **и** TCP 53, ACL, Docker NAT, firewall, upstream             |
| DNS отвечает не тем IP      | Профиль Keenetic, кеш, второй DNS, IPv6/DoH, точные `rewrite_hosts`          |
| `/_ready` возвращает 503    | Загрузка моделей, пути и совместимость Piper/Vosk; логи старта               |
| `echo` работает, модель нет | `base_url` из контейнера, ключ, model ID, доступность провайдера             |
| Нет полного сетевого лога   | `log_level` не включает HAR recorder; [диагностика](docs/TRAFFIC_CAPTURE.md) |

**Перед остановкой сервера сначала верните прежний DNS Станции/профиля Keenetic.** Иначе колонка, а при общей настройке
и другие устройства, потеряют разрешение имён. При необходимости отдельно откатите CA
по [инструкции](docs/STATION_CA.md#откат).

```sh
docker compose -f compose.yaml -f configuration/compose.station.yaml down
```

Команда не должна сопровождаться удалением каталогов `data`, `certs`, моделей или backup. Не используйте `down -v` как
способ «починить» настройки.

## Где что лежит

| Путь / документ                                | Для чего                                                 |
|------------------------------------------------|----------------------------------------------------------|
| [config.example.toml](config.example.toml)     | Все параметры и комментарии; личный конфиг отдельно      |
| [compose.yaml](compose.yaml)                   | Базовый один сервис и ограничения контейнера             |
| `configuration/`                               | Ваш локальный конфиг и station override, не Git          |
| `models/`, `data/`, `certs/`                   | Модели, приватные данные и сертификаты, не Git           |
| [Сертификат на Лайт micro](docs/STATION_CA.md) | Публичный CA, backup, SSH-установка, проверка, откат     |
| [Keenetic](docs/KEENETIC.md)                   | Достаточно DNS; проверка и возврат настроек              |
| [Сеть](docs/NETWORKING.md)                     | DNS rewrite/forward/block, ACL, NAT, TLS и ограничения   |
| [LLM/MCP](docs/LLM_MCP.md)                     | Провайдеры LLM, RU/EN и автоматические инструменты       |
| [Диагностика](docs/TRAFFIC_CAPTURE.md)         | Проверки протокола, симуляторы, импорт HAR и приватность |
| [Разработка](docs/GO_DEVELOPMENT.md)           | Сборка, native dependencies, CI, tools, bundles          |
| [Совместимость](docs/parity.md)                | Что проверено и что осталось от Python-контракта         |
| `cmd/`, `internal/`, `native/`                 | Go entrypoints, backend и CPU native bridge              |

## Авторы и идеи

- **[amirilivice](https://github.com/amirilivice/quasard)** — автор исходного Python-проекта quasard. Его реализация и
  исследование протокола стали отправной точкой Go-версии. Спасибо за основу проекта.
- **[Serewasfera](https://github.com/Serewasfera/quasard)** — спасибо за идеи добавления музыкальных провайдеров и
  переключения между ними, в том числе направление YouTube Music. Это благодарность за идеи; поддержка этих провайдеров
  в Go пока не заявляется.

Новый Go-код — **GPL-3.0-only**. Модели, native-библиотеки и сохранённые артефакты имеют собственные лицензии/условия;
благодарность не меняет их. История Python доступна в Git. Подробности и ограничения прав на исходную
реализацию/артефакты — [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md), текст лицензии — [LICENSE](LICENSE).
