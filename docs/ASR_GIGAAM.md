# GigaAM v3_e2e_rnnt в Go backend

Локальное распознавание русского на CPU. Сервер вызывает ONNX Runtime через
native bridge; Python, PyTorch, внешний ASR-сервис и GPU при работе не нужны.
Экспорт из официальных весов выполняется один раз при подготовке модели.

## Конфигурация

В существующей секции `[asr]`:

```toml
[asr]
engine = "gigaam"
model = "v3_e2e_rnnt"
model_path = "models/gigaam"
language = "ru"
device = "cpu"
threads = 2
max_utterance_ms = 15000
```

`model_path` указывает на каталог с `manifest.json`, `encoder.onnx`,
`decoder.onnx`, `joiner.onnx`, `LICENSE.GigaAM`. Для Docker это обычно
`/app/models/gigaam` внутри models volume. При старте проверяются схема, размер
и SHA-256 графов, затем выполняется прогрев до readiness.

Это русская модель: `auto` и `en` отклоняются конфигурацией. Для автоматического
RU/EN остаётся отдельный [Whisper engine](ASR_RU_EN.md). LLM-профили устройств
и выбор голосов Piper не зависят от выбранного ASR engine.

## Подготовить модель

В текущем рабочем checkout подготовленный набор хранится в `models/gigaam/`.
Большие бинарные веса исключены из Git; исходники native bridge и экспортера
находятся в проекте. Для нового checkout можно скачать артефакт
`gigaam-v3-e2e-rnnt-RUN_ID` успешно завершённого PR/release workflow либо экспортировать:

```sh
python3 -m venv .dev/gigaam-export
# Linux: сначала CPU-варианты torch/torchaudio, затем остальные зависимости.
.dev/gigaam-export/bin/python -m pip install torch torchaudio --index-url https://download.pytorch.org/whl/cpu
.dev/gigaam-export/bin/python -m pip install -r native/requirements-gigaam-export.txt
.dev/gigaam-export/bin/python native/export-gigaam.py --output models/gigaam
```

На macOS установка torch/torchaudio выполняется из обычного PyPI без Linux CPU
index. Экспортёр сохраняет исходную модель `v3_e2e_rnnt`, квантирует постоянные
MatMul/Gemm encoder в INT8 и оставляет RNNT decoder/joiner в float32. Ключ
`--quantization float32` создаёт более крупный вариант без квантования encoder.
Оба варианта имеют один и тот же формат manifest и native ABI.

Официальный код закреплён на
[`7447938d791c4f3e643386ee22c33777004293a5`](https://github.com/salute-developers/GigaAM/tree/7447938d791c4f3e643386ee22c33777004293a5).
Загрузчик GigaAM проверяет upstream checksum исходных весов. В manifest записаны
SHA-256 весов/токенизатора, параметры frontend, словарь SentencePiece, контрольные
суммы ONNX и версии экспортирующих библиотек. Полная MIT-лицензия включена в набор.
Экспортёр не принимает и не упаковывает пользовательское аудио.

## Выполнение и ограничения

- Вход: непрерывный mono PCM16 16000 Hz. VAD определяет конец фразы; внутренняя
  временная шкала GigaAM не склеивается из отдельных VAD-фреймов.
- Нет нормализации, шумодава, замедления или подсказки распознаваемой фразы.
- Frontend повторяет GigaAM: окно/FFT 320, hop 160, periodic Hann, 64 HTK mel,
  power=2, natural log/clamp, center=false. Mel-матрица берётся из оригинальной модели.
- RNNT greedy использует исходный blank=1024 и max_symbols_per_step=10.
  Состояния decoder создаются заново для каждого запроса.
- Один resident ASR-контекст сериализует запросы; ожидающие и активные запросы
  поддерживают отмену. Активный ONNX Run прерывается через RunOptions termination.
- Максимум 25 секунд на одну фразу; превышение возвращает ошибку без обрезания.
  `threads` — 1–8, CPU-only. Выбраны только модель GigaAM и голоса Piper;
  Vosk/Whisper ASR-модели одновременно не загружаются.

## Проверки

Публикуемые образы amd64/arm64 проходят `TestNativeGigaAM` на публичных
синтетических фразах Irina → Opus → PCM → GigaAM, на разных длинах, отмене,
ожидании очереди и повторном использовании контекста. CI экспортирует только
официальные веса и не получает пользовательские записи.

Локально проверены исходная тихая запись, её VAD-фрагмент и варианты обработки.
В Linux ARM64 с 2 CPU и лимитом 1536 MiB исходная фраза распознаётся без усиления.
Это ограниченная проверка, а не общая оценка качества, скорости или памяти
на любом устройстве. Реальная Станция и конкретный сервер требуют отдельной проверки.

DEBUG-событие `GigaAM inference finished` показывает frontend/encoder/decoder
в миллисекундах, число кадров/токенов и `language=ru`. Текст остаётся в общем
`ASR finished`, только на DEBUG. [Диагностический WAV](DIAGNOSTICS.md) содержит
ровно тот PCM, который был передан GigaAM.
