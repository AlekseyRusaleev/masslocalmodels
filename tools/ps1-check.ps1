# Проверяет синтаксис PowerShell-скрипта без выполнения.
param([Parameter(Mandatory = $true)][string]$Path, [string]$OutputPath)

$errors = $null
$null = [System.Management.Automation.Language.Parser]::ParseFile($Path, [ref]$null, [ref]$errors)
if ($errors -and $errors.Count -gt 0) {
    $errors | ForEach-Object { Write-Output $_.Message }
    exit 1
}
Write-Output "синтаксис ок: $Path"
