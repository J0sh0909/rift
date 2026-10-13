<#
.SYNOPSIS
    Checks (and with -Fix, repairs) which rift a new terminal runs.

.DESCRIPTION
    The pipeline installs rift into RiftHome (C:\actions-runner) and always
    calls it by full path. A terminal resolves `rift` through PATH instead:
    the machine PATH, then the user's PATH, first match wins. If RiftHome is
    not on the machine PATH, or a directory before it holds another rift,
    a terminal runs a stale copy.

    Without a switch this only reports. -Fix puts RiftHome first on the
    machine PATH, which needs an elevated shell. Copies in a user's PATH come
    after the machine PATH, so -Fix shadows them too.

    Every deploy runs it with -Auto: fix the machine PATH if it is wrong and
    the runner account may write it, otherwise warn with the -Fix command
    (a runner service running as NETWORK SERVICE may not).

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File C:\actions-runner\rift-path.ps1
    powershell -ExecutionPolicy Bypass -File C:\actions-runner\rift-path.ps1 -Fix
    powershell -ExecutionPolicy Bypass -File C:\actions-runner\rift-path.ps1 -Fix -RemoveStale
#>
param(
    [string]$RiftHome = $(if ($env:RIFT_HOME) { $env:RIFT_HOME } else { 'C:\actions-runner' }),
    # Put RiftHome first on the machine PATH (elevated shell required).
    [switch]$Fix,
    # Deploy mode: fix when needed and permitted, warn otherwise. Never fails.
    [switch]$Auto,
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

# Writes the machine PATH and tells Explorer, so terminals opened from now on
# see it. Returns $false when this account may not write it.
function Set-MachinePath([string]$value) {
    $hklm = [Microsoft.Win32.Registry]::LocalMachine
    if (-not $hklm) { return $false }  # not Windows (tests)
    try {
        $key = $hklm.OpenSubKey($envKey, $true)
    } catch [System.Security.SecurityException], [System.UnauthorizedAccessException] {
        return $false
    }
    if (-not $key) { return $false }
    # ExpandString keeps entries like %SystemRoot% working.
    $key.SetValue('Path', $value, [Microsoft.Win32.RegistryValueKind]::ExpandString)
    $key.Close()
    Add-Type -Namespace RiftPath -Name Native -MemberDefinition @'
[DllImport("user32.dll", CharSet = CharSet.Unicode)]
public static extern System.IntPtr SendMessageTimeout(System.IntPtr hWnd, uint Msg, System.UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out System.UIntPtr lpdwResult);
'@
    $r = [UIntPtr]::Zero
    [void][RiftPath.Native]::SendMessageTimeout([IntPtr]0xffff, 0x1A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$r)
    $true
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

$rest = @($machine | Where-Object { (Get-Key $_) -ne $homeKey })
$newPath = (@($RiftHome) + $rest) -join ';'

if ($Auto -and $problems.Count -gt 0 -and (Set-MachinePath $newPath)) {
    Write-Output "fixed: $RiftHome is now first on the machine PATH"
    $problems = @()
}

if ($Fix) {
    if ($newPath -eq ($machine -join ';')) {
        Write-Output "machine PATH already starts with $RiftHome"
    } elseif (Set-MachinePath $newPath) {
        Write-Output "fixed: $RiftHome is now first on the machine PATH"
    } else {
        Write-Error "-Fix changes the machine PATH: run it from an elevated PowerShell (Run as administrator)"
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
if ($Auto) { Write-Output "could not fix it: this account may not write the machine PATH" }
Write-Output "fix (once, elevated): $fixCmd"
if ($env:GITHUB_ACTIONS -eq 'true') {
    $node = if ($env:RUNNER_NAME) { $env:RUNNER_NAME } else { $env:COMPUTERNAME }
    Write-Output "::warning title=rift not on PATH ($node)::$($problems -join '; '). The runner account may not change the machine PATH; run once in an elevated PowerShell on $($node): $fixCmd"
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
