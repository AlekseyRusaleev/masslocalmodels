<#
.SYNOPSIS
    Автозапуск Worker Agent на Windows: задача планировщика при входе пользователя.

.DESCRIPTION
    Положить бинарник в C:\ComputeNetwork\bin, enrollment выполнить заранее:

        compute-node.exe enroll --control https://api.nshno.tech --token dai_... --name owner-pc

    Скрипт создаёт задачу «ComputeNetwork Worker Agent», которая запускает
    compute-node --run при входе пользователя и сразу её запускает. Окно консоли
    не появляется: задача зарегистрирована как скрытая. Журнал пишется в файл
    с ротацией, поэтому историю видно после перезапуска.

    Права администратора не нужны: задача создаётся для текущего пользователя.

.PARAMETER Invite
    Код приглашения из панели (Узлы → Подключить компьютер). Если он передан,
    скрипт сам зарегистрирует узел, скачает модель и настроит харнес — участнику
    достаточно одной команды. Модель при этом берётся из приглашения.

.PARAMETER SelfRegister
    Зарегистрировать узел без приглашения: программа сама получит секрет
    устройства, ключ для харнеса и модель у платформы.

.PARAMETER Headless
    Запускать только агент, без трея. Нужно, когда на машине больше одного
    узла (у приложения один значок трея на пользователя) или на сервере.

.PARAMETER Model
    Модель, которую узел обслуживает (например qwen3:8b). Запоминается и в
    конфигурации, поэтому перезапуск не требует повторять флаг.

.PARAMETER Config
    Путь к конфигурации агента. По умолчанию C:\ComputeNetwork\agent.json.

.PARAMETER Log
    Путь к журналу. По умолчанию C:\ComputeNetwork\logs\worker-agent.log.

.PARAMETER Binary
    Что установить в C:\ComputeNetwork\bin. По умолчанию берётся уже лежащий там
    compute-node.exe; если указан другой путь, файл копируется.

.PARAMETER Uninstall
    Остановить и удалить задачу (сам агент и логи остаются на месте).

.EXAMPLE
    .\install-worker-agent.ps1 -Model qwen3:8b -Binary .\bin\compute-node.exe
#>
[CmdletBinding()]
param(
    [string]$Model = "",
    [string]$Config = "C:\ComputeNetwork\agent.json",
    [string]$Log = "C:\ComputeNetwork\logs\worker-agent.log",
    [string]$InstallDir = "C:\ComputeNetwork\bin",
    [string]$Binary = "",
    [string]$Invite = "",
    [string]$EnrollmentToken = "",
    [string]$Name = "",
    [string]$Control = "https://api.nshno.tech",
    [string]$TaskName = "ComputeNetwork Worker Agent",
    [string]$SelfRegisterCode = "",
    [switch]$SelfRegister,
    [switch]$Headless,
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"

function Stop-Agent {
    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($task) {
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        # Дать процессу выйти: иначе файл бинарника остаётся занятым.
        for ($i = 0; $i -lt 60; $i++) {
            if (-not (Get-Process compute-node -ErrorAction SilentlyContinue)) { return }
            Start-Sleep -Milliseconds 500
        }
    }
}

if ($Uninstall) {
    if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
        Stop-Agent
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
        Write-Host "задача удалена: $TaskName"
    } else {
        Write-Host "задачи нет: $TaskName"
    }
    return
}

$exe = Join-Path $InstallDir "compute-node.exe"

if ($Binary) {
    if (-not (Test-Path -LiteralPath $Binary)) { throw "нет файла: $Binary" }
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Stop-Agent
    Copy-Item -LiteralPath $Binary -Destination $exe -Force
    Write-Host "бинарник установлен: $exe"
}

if (-not (Test-Path -LiteralPath $exe)) { throw "не найден $exe — укажите -Binary" }

# Регистрация: если передан одноразовый токен, enrollment выполняется здесь же,
# чтобы у участника была одна команда, а не последовательность шагов.
if ($EnrollmentToken) {
    if (-not $Name) { $Name = $env:COMPUTERNAME }
    Write-Host "регистрирую узел «$Name» на $Control…"
    $enroll = Start-Process -FilePath $exe -Wait -PassThru -NoNewWindow -ArgumentList @(
        "enroll", "--control", $Control, "--token", $EnrollmentToken, "--name", $Name, "--config", $Config)
    if ($enroll.ExitCode -ne 0) { throw "enroll не выполнен (код $($enroll.ExitCode))" }
}

# Самозапись без приглашения: участнику не нужен код от владельца сети.
# Программа спрашивает у платформы секрет устройства, ключ для харнеса и модель,
# скачивает модель и настраивает харнес — всё сама.
if ($SelfRegister -and -not $Invite) {
    Write-Host "регистрирую узел на платформе…"
    $registerArgs = @("register", "--config", $Config, "--pull")
    if ($Control) { $registerArgs += @("--control", $Control) }
    if ($Name) { $registerArgs += @("--name", $Name) }
    if ($SelfRegisterCode) { $registerArgs += @("--code", $SelfRegisterCode) }
    $registered = Start-Process -FilePath $exe -ArgumentList $registerArgs -Wait -PassThru -NoNewWindow
    if ($registered.ExitCode -ne 0) { throw "самозапись не выполнена (код $($registered.ExitCode))" }
    Write-Host ""
}

# Подключение по приглашению: одна команда делает всё — регистрацию, загрузку
# модели и настройку харнеса. Пользователю не нужно разбираться в параметрах.
if ($Invite) {
    Write-Host "подключаю узел по приглашению…"
    $joinArgs = @("join", "--invite", $Invite, "--config", $Config, "--pull")
    if ($Name) { $joinArgs += @("--name", $Name) }
    # compute-node.exe — приложение без консоли (windowsgui): вызов через & не
    # дожидается его завершения, а $LASTEXITCODE остаётся от прошлой команды.
    # Поэтому запускаем как процесс и ждём его ExitCode.
    $join = Start-Process -FilePath $exe -ArgumentList $joinArgs -Wait -PassThru -NoNewWindow
    if ($join.ExitCode -ne 0) { throw "подключение по приглашению не выполнено (код $($join.ExitCode))" }
    Write-Host ""
}

if (-not (Test-Path -LiteralPath $Config)) {
    throw "нет конфигурации $Config — укажите -EnrollmentToken или выполните 'compute-node enroll'"
}

# Модель берём из самой конфигурации: status — тоже приложение без консоли, его
# вывод через & не перехватить. Конфиг уже записан enroll/join.
if (-not $Model -and (Test-Path -LiteralPath $Config)) {
    try {
        $savedConfig = Get-Content -LiteralPath $Config -Raw | ConvertFrom-Json
        if ($savedConfig.model) { $Model = [string]$savedConfig.model }
    } catch {
        Write-Warning "не удалось прочитать модель из $Config"
    }
}
if (-not $Model) {
    throw "модель не задана: укажите -Model (например -Model qwen3:8b)"
}

New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Log) | Out-Null

# Настройки рантайма под платформу. Квантованный кэш внимания вдвое-вчетверо
# уменьшает память под контекст, поэтому окно 16K остаётся целиком на GPU.
# Без этих переменных модель на 8 ГБ уезжает в CPU уже на 16K и работает
# втрое медленнее (измерено на RTX 5060: 70.6 против 29.9 tok/s).
#
# Глубина квантования KV зависит от видеопамяти. 16K окно qwen3:8b в q8_0
# требует 1.22 ГБ только под кэш: на 8 ГБ карте с занятым рабочим столом
# (браузер, телеграм — 1.1 ГБ) это уже не влезает и Ollama падает с
# "cudaMalloc failed: out of memory". q4_0 даёт те же 16K за 0.64 ГБ и
# сохраняет 67.5 tok/s. На картах от 12 ГБ оставляем q8_0 — качество выше.
#
# OLLAMA_NUM_PARALLEL=1 обязателен на 8 ГБ: без него два одновременных
# запроса (клиент вроде OpenCode шлёт чат и заголовок сессии) поднимают
# два llama-server, и второй падает по памяти вместо того, чтобы ждать.
$nvVramMb = 0
try {
    $raw = & nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits 2>$null |
        Select-Object -First 1
    if ($raw) { $nvVramMb = [int]($raw.Trim()) }
} catch {
    $nvVramMb = 0
}
$smallGpu = ($nvVramMb -gt 0) -and ($nvVramMb -le 9000)
$kvType = if ($smallGpu) { "q4_0" } else { "q8_0" }

$runtimeChanged = $false
$runtimeVars = [ordered]@{
    OLLAMA_FLASH_ATTENTION = "1"
    OLLAMA_KV_CACHE_TYPE   = $kvType
}
if ($smallGpu) {
    $runtimeVars["OLLAMA_NUM_PARALLEL"] = "1"
    $runtimeVars["OLLAMA_MAX_LOADED_MODELS"] = "2"
}
foreach ($name in $runtimeVars.Keys) {
    if ([Environment]::GetEnvironmentVariable($name, "User") -ne $runtimeVars[$name]) {
        [Environment]::SetEnvironmentVariable($name, $runtimeVars[$name], "User")
        Write-Host "рантайм: $name=$($runtimeVars[$name])"
        $runtimeChanged = $true
    }
}
if ($runtimeChanged) {
    Write-Host "рантайм: KV-кэш $kvType — перезапустите Ollama, чтобы она подхватила новые переменные (окно 16K на GPU)"
}

# Автозапуск. По умолчанию — приложение с треем: окно консоли не открывается,
# человек видит значок и статус. В режиме -Headless запускается только агент:
# так на одной машине может работать несколько узлов (трей у приложения один и
# второй экземпляр просто показал бы окно первого) — это нужно серверам с
# несколькими GPU и тестовым стендам.
$execute = $exe
if ($Headless) {
    $arguments = @(
        "run",
        "--model", "`"$Model`"",
        "--config", "`"$Config`"",
        "--log", "`"$Log`""
    ) -join " "
    Write-Host "автозапуск без трея: агент в фоне, режим для нескольких узлов на машине"
} else {
    $arguments = @(
        "--run",
        "--model", "`"$Model`"",
        "--config", "`"$Config`""
    ) -join " "
    Write-Host "автозапуск через приложение с треем (окно консоли не открывается)"
}

if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
    Stop-Agent
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
}

$user = "$env:USERDOMAIN\$env:USERNAME"
$action = New-ScheduledTaskAction -Execute $execute -Argument $arguments -WorkingDirectory $InstallDir
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $user
$settings = New-ScheduledTaskSettingsSet -Hidden -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew `
    -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
$principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited

Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Settings $settings `
    -Principal $principal -Description "Worker Agent: heartbeat и задания инференса для платформы" | Out-Null

Start-ScheduledTask -TaskName $TaskName
Start-Sleep -Seconds 4

$state = (Get-ScheduledTask -TaskName $TaskName).State
# Процессов может быть два: трей и дочерний агент. Печатаем их списком.
$processes = @(Get-Process compute-node -ErrorAction SilentlyContinue)
Write-Host ""
Write-Host "задача:   $TaskName ($state)"
Write-Host "бинарник: $exe"
Write-Host "модель:   $Model"
Write-Host "конфиг:   $Config"
Write-Host "журнал:   $Log"
if ($processes.Count -gt 0) {
    foreach ($process in $processes) {
        Write-Host ("процесс:  PID {0}, запущен {1}" -f $process.Id, $process.StartTime.ToString("HH:mm:ss"))
    }
} else {
    Write-Warning "процесс агента не запустился — проверьте журнал $Log"
}
