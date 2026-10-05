<#
.SYNOPSIS
  Removes the Windstream logon task, firewall rules and binaries. The config
  directory (with your users and certificates) is kept unless -RemoveData.
#>
#Requires -RunAsAdministrator
[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:ProgramFiles 'Windstream'),
    [string]$DataDir    = (Join-Path $env:ProgramData 'Windstream'),
    [switch]$RemoveData
)
$ErrorActionPreference = 'Stop'
Stop-ScheduledTask -TaskName 'Windstream' -ErrorAction SilentlyContinue
Unregister-ScheduledTask -TaskName 'Windstream' -Confirm:$false -ErrorAction SilentlyContinue
Get-Process -Name 'windstreamw' -ErrorAction SilentlyContinue | Stop-Process -Force
Get-NetFirewallRule -DisplayName 'Windstream *' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
if (Test-Path $InstallDir) { Remove-Item $InstallDir -Recurse -Force }
if ($RemoveData -and (Test-Path $DataDir)) { Remove-Item $DataDir -Recurse -Force }
Write-Host "Windstream removed." $(if (-not $RemoveData) { "Config kept in $DataDir." })
