<#
.SYNOPSIS
    Собирает веб-панель и выкладывает её на VPS.

.DESCRIPTION
    Панель — статика Vite. На сервере она лежит в /opt/compute-control/web,
    nginx отдаёт её на https://control.nshno.tech и проксирует /api/* на
    api-gateway.

    Файлы заливаются в отдельный каталог выкладки /tmp/web-deploy-<время>, а
    затем переносятся на место. Каталог уникален для каждого запуска: иначе в
    /tmp остаются сборки прошлых версий и попадают в прод вместе с новыми.

    Параметры подключения берутся из окружения: PVE_HOST, PVE_PORT, PVE_USER,
    PVE_PASS (те же, что у tools/ssh_run.py).

.EXAMPLE
    $env:PVE_HOST='144.31.61.208'; $env:PVE_USER='root'; $env:PVE_PASS='...'
    .\scripts\deploy-web.ps1
#>
[CmdletBinding()]
param(
    [string]$Remote = "/opt/compute-control/web",
    [string]$PublicUrl = "https://control.nshno.tech"
)

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Push-Location (Join-Path $root "web")
try {
    Write-Host "сборка панели…"
    npm run build
    if ($LASTEXITCODE -ne 0) { throw "сборка не прошла" }
} finally {
    Pop-Location
}

$dist = Join-Path $root "web\dist"
$staging = "/tmp/web-deploy-" + (Get-Date -Format "yyyyMMddHHmmss")

Push-Location $root
try {
    Write-Host "готовлю каталог выкладки…"
    "mkdir -p $staging/assets" | python tools\ssh_run.py -
    if ($LASTEXITCODE -ne 0) { throw "не удалось создать каталог выкладки" }

    Write-Host "заливка файлов…"
    python tools\ssh_put.py (Join-Path $dist "index.html") "$staging/index.html"
    if ($LASTEXITCODE -ne 0) { throw "не удалось залить index.html" }

    Get-ChildItem (Join-Path $dist "assets") -File | ForEach-Object {
        python tools\ssh_put.py $_.FullName "$staging/assets/$($_.Name)"
        if ($LASTEXITCODE -ne 0) { throw "не удалось залить $($_.Name)" }
    }
} finally {
    Pop-Location
}

$remoteScript = @"
set -e
mkdir -p $Remote/assets
install -m 0644 $staging/index.html $Remote/index.html
rm -f $Remote/assets/*
cp -f $staging/assets/* $Remote/assets/
chmod 0644 $Remote/assets/*
rm -rf $staging
echo '--- файлы панели ---'
ls -1 $Remote/assets
echo '--- ссылки из index.html ---'
grep -o 'assets/[^"]*' $Remote/index.html
"@

Push-Location $root
try {
    $remoteScript | python tools\ssh_run.py -
    if ($LASTEXITCODE -ne 0) { throw "не удалось разложить файлы на сервере" }
} finally {
    Pop-Location
}

$response = Invoke-WebRequest -Uri $PublicUrl -UseBasicParsing -TimeoutSec 30
Write-Host ""
Write-Host "панель: $PublicUrl → HTTP $($response.StatusCode), $($response.RawContentLength) байт"
