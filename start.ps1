# Локальный Go CPU-only backend через Docker Compose. Не меняет сеть/сертификаты ОС.
# Сначала подготовьте config.toml, модели и certs по README.md.
param([switch]$Build)
$ErrorActionPreference = "Stop"
Push-Location $PSScriptRoot
try {
    if (-not (Test-Path -LiteralPath "config.toml" -PathType Leaf)) {
        throw "Нет config.toml. Скопируйте config.example.toml и настройте по README.md."
    }
    if ($Build) {
        & docker compose up --build
    } else {
        & docker compose up
    }
    $code = $LASTEXITCODE
} finally {
    Pop-Location
}
exit $code
