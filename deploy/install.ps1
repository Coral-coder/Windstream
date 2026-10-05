<#
.SYNOPSIS
  Installs Windstream: copies the binaries, creates a locked-down config
  directory, opens the firewall ports and registers a logon task that starts
  the server in your desktop session.

.DESCRIPTION
  Run from an elevated PowerShell in the folder containing windstream.exe and
  windstreamw.exe:

      powershell -ExecutionPolicy Bypass -File .\install.ps1

  Windstream must run inside the logged-in user's session: Desktop
  Duplication capture and SendInput do not work from a Windows service
  (session 0). The scheduled task starts it at logon instead, hidden, and
  restarts it if it exits.

.PARAMETER Limited
  Run the server without Administrator rights. The virtual display adapter
  then has to be left enabled permanently (display.virtual_keep_enabled), and
  keyboard/mouse input cannot reach elevated windows.
#>
#Requires -RunAsAdministrator
[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:ProgramFiles 'Windstream'),
    [string]$DataDir    = (Join-Path $env:ProgramData 'Windstream'),
    [int]$HttpsPort     = 8443,
    [int]$UdpPort       = 8444,
    [string]$User       = "$env:USERDOMAIN\$env:USERNAME",
    [switch]$Limited
)
$ErrorActionPreference = 'Stop'
$src = $PSScriptRoot

foreach ($f in 'windstream.exe', 'windstreamw.exe') {
    if (-not (Test-Path (Join-Path $src $f))) { throw "$f not found next to install.ps1" }
}

Write-Host "Stopping any running instance..."
Stop-ScheduledTask -TaskName 'Windstream' -ErrorAction SilentlyContinue
Get-Process -Name 'windstreamw' -ErrorAction SilentlyContinue | Stop-Process -Force

Write-Host "Installing to $InstallDir"
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Copy-Item (Join-Path $src 'windstream.exe'), (Join-Path $src 'windstreamw.exe') $InstallDir -Force
if (Test-Path (Join-Path $src 'ViGEmClient.dll')) {
    Copy-Item (Join-Path $src 'ViGEmClient.dll') $InstallDir -Force
} elseif (-not (Test-Path (Join-Path $InstallDir 'ViGEmClient.dll'))) {
    Write-Warning "ViGEmClient.dll not found: gamepads will be disabled until you add it to $InstallDir (see docs/windows-setup.md)."
}

# Config directory: holds password hashes, TOTP secrets and TLS keys, so only
# SYSTEM and Administrators may read it (the elevated server runs as an admin).
New-Item -ItemType Directory -Force -Path $DataDir, (Join-Path $DataDir 'logs') | Out-Null
$cfg = Join-Path $DataDir 'windstream.toml'
if (-not (Test-Path $cfg)) {
    Copy-Item (Join-Path $src 'windstream.example.toml') $cfg
    Write-Host "Created $cfg - edit it before first start."
}
icacls $DataDir /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
if ($Limited) {
    icacls $DataDir /grant "${User}:(OI)(CI)M" | Out-Null
}

Write-Host "Configuring Windows Firewall (TCP $HttpsPort, UDP $UdpPort)"
Get-NetFirewallRule -DisplayName 'Windstream *' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
foreach ($exe in 'windstream.exe', 'windstreamw.exe') {
    $prog = Join-Path $InstallDir $exe
    New-NetFirewallRule -DisplayName "Windstream HTTPS ($exe)" -Direction Inbound -Action Allow `
        -Protocol TCP -LocalPort $HttpsPort -Program $prog -Profile Any | Out-Null
    New-NetFirewallRule -DisplayName "Windstream media ($exe)" -Direction Inbound -Action Allow `
        -Protocol UDP -LocalPort $UdpPort -Program $prog -Profile Any | Out-Null
}

Write-Host "Registering logon task for $User"
$exe = Join-Path $InstallDir 'windstreamw.exe'
$log = Join-Path $DataDir 'logs\windstream.log'
$action = New-ScheduledTaskAction -Execute $exe -Argument "serve -config `"$cfg`" -log-file `"$log`"" -WorkingDirectory $DataDir
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $User
$runLevel = if ($Limited) { 'Limited' } else { 'Highest' }
$principal = New-ScheduledTaskPrincipal -UserId $User -LogonType Interactive -RunLevel $runLevel
$settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
    -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -MultipleInstances IgnoreNew -StartWhenAvailable
$settings.Priority = 4   # above the default (7) so capture/encode keeps up under load
Register-ScheduledTask -TaskName 'Windstream' -Action $action -Trigger $trigger `
    -Principal $principal -Settings $settings -Force | Out-Null

Write-Host ""
Write-Host "Installed. Next steps:"
Write-Host "  1. Edit $cfg (users, TLS, display)."
Write-Host "  2. Validate:  & '$InstallDir\windstream.exe' check -config '$cfg'"
Write-Host "  3. Start:     Start-ScheduledTask -TaskName Windstream   (or sign out and back in)"
Write-Host "  4. Logs:      Get-Content '$log' -Wait"
Write-Host "  5. Forward TCP $HttpsPort and UDP $UdpPort on your router to this PC."
