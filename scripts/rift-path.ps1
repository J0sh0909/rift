<#
.SYNOPSIS
    Checks (and with -Fix, repairs) which rift a new terminal runs.

.DESCRIPTION
    The pipeline installs rift into RiftHome (C:\actions-runner) and always
    calls it by full path. A terminal resolves `rift` through PATH instead:
    the machine PATH, then the user's PATH, first match wins. If RiftHome is
    not on the machine PATH, or a directory before it holds another rift,
    a terminal runs a stale copy.

    Without -Fix this only reports; it needs no admin rights and runs as part
    of every deploy. -Fix puts RiftHome first on the machine PATH, which needs
    an elevated shell (the runner service account cannot write it). Copies in
    a user's PATH come after the machine PATH, so -Fix shadows them too.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File C:\actions-runner\rift-path.ps1
    powershell -ExecutionPolicy Bypass -File C:\actions-runner\rift-path.ps1 -Fix
    powershell -ExecutionPolicy Bypass -File C:\actions-runner\rift-path.ps1 -Fix -RemoveStale
#>
param(
    [string]$RiftHome = $(if ($env:RIFT_HOME) { $env:RIFT_HOME } else { 'C:\actions-runner' }),
    # Put RiftHome first on the machine PATH (elevated shell required).
    [switch]$Fix,
    # Also delete the other rift copies found on PATH.
    [switch]$RemoveStale,
    # Overrides for tests; default to the registry / current user.
    [string]$MachinePath,
    [string]$UserPath
)

$ErrorActionPreference = 'Stop'
$envKey = 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment'

function Get-Key([string]$dir) {
    [Environment]::ExpandEnvironmentVariables($dir).Trim().TrimEnd('\', '/').ToLowerInvariant()
}

function Split-PathList([string]$value) {
    @($value -split ';' | Where-Object { $_.Trim() -ne '' })
}

# Files named rift with an executable extension (rift.exe, rift.cmd, ...) in a directory.
function Find-Rift([string]$dir) {
    $pathext = if ($env:PATHEXT) { $env:PATHEXT } else { '.COM;.EXE;.BAT;.CMD' }
    $exts = @($pathext -split ';' | Where-Object { $_ })
    $found = @()
    foreach ($ext in $exts) {
        $p = Join-Path ([Environment]::ExpandEnvironmentVariables($dir)) ("rift" + $ext.ToLowerInvariant())
        if (Test-Path -LiteralPath $p -PathType Leaf) { $found += $p }
    }
    $found
}

function Get-Version([string]$exe) {
    if ($exe -notlike '*.exe') { return '' }
    try { (& $exe version 2>$null | Select-Object -First 1) -join '' } catch { '' }
}

$isWin = [Environment]::OSVersion.Platform -eq 'Win32NT'
if (-not $PSBoundParameters.ContainsKey('MachinePath')) {
    $key = [Microsoft.Win32.Registry]::LocalMachine.OpenSubKey($envKey)
    $MachinePath = $key.GetValue('Path', '', 'DoNotExpandEnvironmentNames')
    $key.Close()
}
if (-not $PSBoundParameters.ContainsKey('UserPath')) {
    $UserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
}

$homeKey = Get-Key $RiftHome
$machine = @(Split-PathList $MachinePath)
$user = @(Split-PathList $UserPath)
$homeIndex = -1
for ($i = 0; $i -lt $machine.Count; $i++) {
    if ((Get-Key $machine[$i]) -eq $homeKey) { $homeIndex = $i; break }
}

# Walk PATH in the order Windows does (machine, then user) and note every rift.
$hits = @()
$order = 0
foreach ($dir in ($machine + $user)) {
    foreach ($exe in (Find-Rift $dir)) {
        $hits += [pscustomobject]@{
            Order     = $order
            Path      = $exe
            Installed = (Get-Key $dir) -eq $homeKey
        }
    }
    $order++
}
$resolved = $hits | Select-Object -First 1
$stale = @($hits | Where-Object { -not $_.Installed })

$problems = @()
if ($homeIndex -lt 0) {
    $problems += "$RiftHome is not on the machine PATH"
} else {
    # A copy in a machine PATH directory before RiftHome wins over it.
    foreach ($s in @($stale | Where-Object { $_.Order -lt $homeIndex })) {
        $problems += "$($s.Path) comes before $RiftHome on PATH"
    }
}

Write-Output "installed: $(Join-Path $RiftHome 'rift.exe')"
if ($resolved) {
    Write-Output "a new terminal runs: $($resolved.Path) $(Get-Version $resolved.Path)"
} else {
    Write-Output "a new terminal runs: nothing (rift is not on PATH)"
}
foreach ($s in $stale) { Write-Output "other copy: $($s.Path) $(Get-Version $s.Path)" }

if ($Fix) {
    if ($isWin) {
        $principal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
        if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
            Write-Error "-Fix changes the machine PATH: run it from an elevated PowerShell (Run as administrator)"
        }
    }
    $rest = @($machine | Where-Object { (Get-Key $_) -ne $homeKey })
    $newPath = (@($RiftHome) + $rest) -join ';'
    if ($newPath -ne ($machine -join ';')) {
        $key = [Microsoft.Win32.Registry]::LocalMachine.OpenSubKey($envKey, $true)
        # ExpandString keeps entries like %SystemRoot% working.
        $key.SetValue('Path', $newPath, [Microsoft.Win32.RegistryValueKind]::ExpandString)
        $key.Close()
        # Tell Explorer the environment changed, so terminals opened from now on see it.
        Add-Type -Namespace RiftPath -Name Native -MemberDefinition @'
[DllImport("user32.dll", CharSet = CharSet.Unicode)]
public static extern System.IntPtr SendMessageTimeout(System.IntPtr hWnd, uint Msg, System.UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out System.UIntPtr lpdwResult);
'@
        $r = [UIntPtr]::Zero
        [void][RiftPath.Native]::SendMessageTimeout([IntPtr]0xffff, 0x1A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$r)
        Write-Output "fixed: $RiftHome is now first on the machine PATH"
    } else {
        Write-Output "machine PATH already starts with $RiftHome"
    }
    if ($RemoveStale) {
        foreach ($s in $stale) {
            Remove-Item -LiteralPath $s.Path -Force
            Write-Output "removed: $($s.Path)"
        }
    } elseif ($stale.Count -gt 0) {
        Write-Output "the other copies are now shadowed; -RemoveStale deletes them"
    }
    Write-Output "open a new terminal: windows already open keep their old PATH"
    exit 0
}

if ($problems.Count -eq 0) {
    Write-Output "ok: a new terminal runs the installed rift"
    exit 0
}

$fixCmd = "powershell -ExecutionPolicy Bypass -File $(Join-Path $RiftHome 'rift-path.ps1') -Fix"
foreach ($p in $problems) { Write-Output "problem: $p" }
Write-Output "fix (once, elevated): $fixCmd"
if ($env:GITHUB_ACTIONS -eq 'true') {
    $node = if ($env:RUNNER_NAME) { $env:RUNNER_NAME } else { $env:COMPUTERNAME }
    Write-Output "::warning title=rift not on PATH ($node)::$($problems -join '; '). Run once in an elevated PowerShell on $($node): $fixCmd"
    if ($env:GITHUB_STEP_SUMMARY) {
        Add-Content -LiteralPath $env:GITHUB_STEP_SUMMARY -Value @(
            "### $node`: rift in a terminal is not the installed one"
            ''
            ($problems | ForEach-Object { "- $_" })
            ''
            "Run once in an elevated PowerShell on $($node):"
            ''
            '```powershell'
            $fixCmd
            '```'
        )
    }
}
# Reporting only: the install itself succeeded and the pipeline calls rift by full path.
exit 0
