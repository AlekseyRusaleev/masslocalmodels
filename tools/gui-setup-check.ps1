<#
.SYNOPSIS
    Проверка графического установщика: ввод приглашения и запуск подключения.

.DESCRIPTION
    Скрипт запускает worker-setup.exe, находит окно, вставляет код приглашения,
    нажимает «Подключиться» и ждёт результата.

    Проверяются два источника: текст элементов окна (интерфейс действительно
    обновился) и журнал установщика в %TEMP%\worker-setup.log (рабочий поток
    действительно прошёл шаги).

.EXAMPLE
    .\tools\gui-setup-check.ps1 -Invite "eyJuZXR3..."
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Invite,
    [string]$Exe = ".\bin\worker-setup.exe",
    [int]$TimeoutSeconds = 300
)

$ErrorActionPreference = "Stop"

Add-Type @"
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;

public static class Win {
    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    public static extern IntPtr FindWindowEx(IntPtr parent, IntPtr after, string cls, string title);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    public static extern IntPtr SendMessage(IntPtr hwnd, uint msg, IntPtr wParam, string lParam);

    [DllImport("user32.dll")]
    public static extern IntPtr SendMessage(IntPtr hwnd, uint msg, IntPtr wParam, IntPtr lParam);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    public static extern int GetWindowText(IntPtr hwnd, StringBuilder text, int max);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    public static extern int GetClassName(IntPtr hwnd, StringBuilder text, int max);

    [DllImport("user32.dll")] public static extern int GetDlgCtrlID(IntPtr hwnd);
    [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr hwnd, uint msg, IntPtr wParam, IntPtr lParam);

    public static string Text(IntPtr hwnd) {
        var buf = new StringBuilder(16384);
        GetWindowText(hwnd, buf, buf.Capacity);
        return buf.ToString();
    }

    public static string Cls(IntPtr hwnd) {
        var buf = new StringBuilder(256);
        GetClassName(hwnd, buf, buf.Capacity);
        return buf.ToString();
    }

    public static List<IntPtr> Children(IntPtr parent) {
        var list = new List<IntPtr>();
        IntPtr current = IntPtr.Zero;
        while (true) {
            current = FindWindowEx(parent, current, null, null);
            if (current == IntPtr.Zero) break;
            list.Add(current);
        }
        return list;
    }

    // Тот же путь, которым Windows сообщает о нажатии кнопки, но без фокуса.
    public static void Click(IntPtr parent, IntPtr button) {
        int id = GetDlgCtrlID(button);
        SendMessage(parent, 0x0111, (IntPtr)(id & 0xFFFF), button);
    }
}
"@

$WM_SETTEXT = 0x000C
$WM_CLOSE = 0x0010

$logPath = Join-Path $env:TEMP "worker-setup.log"
Remove-Item $logPath -ErrorAction SilentlyContinue

$exePath = (Resolve-Path $Exe).Path
Write-Host "запускаю $exePath"
$process = Start-Process -FilePath $exePath -PassThru

$window = [IntPtr]::Zero
for ($i = 0; $i -lt 30; $i++) {
    $process.Refresh()
    if ($process.HasExited) { throw "установщик завершился сразу после запуска" }
    if ($process.MainWindowHandle -ne [IntPtr]::Zero) { $window = $process.MainWindowHandle; break }
    Start-Sleep -Milliseconds 500
}
if ($window -eq [IntPtr]::Zero) { throw "окно установщика не найдено" }
Write-Host ("окно найдено: {0}" -f [Win]::Text($window))

$children = [Win]::Children($window)
Write-Host ("элементов управления: {0}" -f $children.Count)

$inviteEdit = $null
$startButton = $null
foreach ($child in $children) {
    $class = [Win]::Cls($child)
    $text = [Win]::Text($child)
    if ($class -eq "Edit" -and $text -eq "" -and $null -eq $inviteEdit) { $inviteEdit = $child }
    if ($class -eq "Button" -and $text -eq "Подключиться") { $startButton = $child }
}
if ($null -eq $inviteEdit) { throw "поле приглашения не найдено" }
if ($null -eq $startButton) { throw "кнопка «Подключиться» не найдена" }

[void][Win]::SendMessage($inviteEdit, $WM_SETTEXT, [IntPtr]::Zero, $Invite)
Start-Sleep -Milliseconds 300
[Win]::Click($window, $startButton)
Write-Host "приглашение вставлено, нажато «Подключиться»"

$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
$statusText = ""
$logText = ""
$finished = $false

while ((Get-Date) -lt $deadline) {
    Start-Sleep -Seconds 2

    $statics = @()
    foreach ($child in [Win]::Children($window)) {
        if ([Win]::Cls($child) -eq "Static") {
            $text = [Win]::Text($child)
            if ($text.Length -gt 0) { $statics += $text }
        }
    }
    $statusText = ($statics | Where-Object { $_ -notmatch "^(Подключение|Вставьте|Код|Имя|Модель|Состояние|Журнал)" }) -join " / "

    if (Test-Path $logPath) {
        $logText = Get-Content $logPath -Raw -Encoding UTF8
        if ($logText -match "готово|ОШИБКА") { $finished = $true; break }
    }
}

Write-Host ""
Write-Host ("состояние в окне: {0}" -f $statusText)
Write-Host "--- журнал установщика ---"
if ($logText) {
    ($logText -split "`r?`n" | Select-Object -Last 22) -join "`n"
} else {
    "журнал пуст"
}

[void][Win]::PostMessage($window, $WM_CLOSE, [IntPtr]::Zero, [IntPtr]::Zero)
Start-Sleep -Seconds 1
if (-not $process.HasExited) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue }

if ($finished -and $logText -notmatch "ОШИБКА") { exit 0 } else { exit 1 }
