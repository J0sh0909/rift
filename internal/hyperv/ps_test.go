package hyperv

// These tests run the backend's real PowerShell scripts under PowerShell 7
// with the Hyper-V cmdlets replaced by mocks, so script syntax, the
// environment-variable plumbing and the JSON contract with the Go parsers are
// all exercised off Windows. Set RIFT_TEST_PWSH to a pwsh binary to run them.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/J0sh0909/rift/internal/core"
)

const psMocks = `
enum VMState { Running = 2; Off = 3 }
$script:vms = @(
  [pscustomobject]@{ Name = 'web-1'; Id = [guid]'0f1e2d3c-1111-2222-3333-444455556666'; State = [VMState]::Running; Notes = '' },
  [pscustomobject]@{ Name = 'db [1]'; Id = [guid]'aaaaaaaa-1111-2222-3333-444455556666'; State = [VMState]::Off; Notes = "owner: x` + "`" + `r` + "`" + `nrift-folder: PROD` + "`" + `r` + "`" + `nrift-os: ubuntu-64" },
  [pscustomobject]@{ Name = "it's"; Id = [guid]'bbbbbbbb-1111-2222-3333-444455556666'; State = [VMState]::Off; Notes = $null }
)
function Log($msg) { Add-Content -LiteralPath $env:RIFT_MOCK_LOG -Value $msg }
function Get-VM { param([string]$Id)
  if ($Id) { return $script:vms | Where-Object { [string]$_.Id -eq $Id } }
  $script:vms
}
function Get-VMGroup { param($ErrorAction)
  [pscustomobject]@{ Name = 'DEV'; VMMembers = @($script:vms[0], $script:vms[1]) }
}
function Get-VMNetworkAdapter { param($VM)
  if ($VM.Name -eq 'web-1') { [pscustomobject]@{ IPAddresses = @('192.168.1.20', 'fe80::1') } }
  else { [pscustomobject]@{ IPAddresses = @() } }
}
function Start-VM { param([Parameter(ValueFromPipeline)]$VM) process { Log "start $($VM.Name)" } }
function Stop-VM { param([Parameter(ValueFromPipeline)]$VM, [switch]$TurnOff, [switch]$Force) process { Log "stop $($VM.Name) turnoff=$TurnOff" } }
function Get-VMCheckpoint { param([Parameter(ValueFromPipeline)]$VM) process {
  [pscustomobject]@{ Name = 'second'; CreationTime = [datetime]'2026-02-01' }
  [pscustomobject]@{ Name = 'origin'; CreationTime = [datetime]'2026-01-01' }
} }
function Restore-VMCheckpoint { param([Parameter(ValueFromPipeline)]$Checkpoint, $Confirm) process { Log "restore $($Checkpoint.Name)" } }
function Checkpoint-VM { param([Parameter(ValueFromPipeline)]$VM, $SnapshotName) process { Log "checkpoint $($VM.Name) $SnapshotName" } }
function Invoke-Command { param($VMId, $Credential, $ArgumentList, $ScriptBlock)
  Log "invoke $VMId as $($Credential.UserName) pass=$($Credential.GetNetworkCredential().Password)"
  & $ScriptBlock $ArgumentList
}
function cmd.exe { if ("$args" -match 'fail') { $global:LASTEXITCODE = 5 } else { $global:LASTEXITCODE = 0 }; "ran: $args" }
function Get-CimInstance { param($Namespace, $ClassName, $Filter) Log "cim $Filter"; [pscustomobject]@{ Name = 'x' } }
function Get-CimAssociatedInstance { param($InputObject, $ResultClassName)
  [pscustomobject]@{ GuestIntrinsicExchangeItems = @(
    '<INSTANCE CLASSNAME="Msvm_KvpExchangeDataItem"><PROPERTY NAME="Data" TYPE="string"><VALUE>Windows 10 Pro</VALUE></PROPERTY><PROPERTY NAME="Name" TYPE="string"><VALUE>OSName</VALUE></PROPERTY></INSTANCE>'
  ) }
}
`

func withPwsh(t *testing.T) string {
	t.Helper()
	pwsh := os.Getenv("RIFT_TEST_PWSH")
	if pwsh == "" {
		t.Skip("set RIFT_TEST_PWSH to a PowerShell 7 binary to run script tests")
	}
	log := filepath.Join(t.TempDir(), "mock.log")
	oldExe, oldPrelude := psExe, psPrelude
	psExe, psPrelude = pwsh, psPreamble+psMocks
	t.Setenv("RIFT_MOCK_LOG", log)
	t.Cleanup(func() { psExe, psPrelude = oldExe, oldPrelude })
	return log
}

func mockLog(t *testing.T, path string) string {
	t.Helper()
	data, _ := os.ReadFile(path)
	return string(data)
}

func TestPSGetPowerState(t *testing.T) {
	withPwsh(t)
	got, err := (&HyperVBackend{}).GetPowerState()
	if err != nil {
		t.Fatal(err)
	}
	want := []core.VM{
		{Name: "web-1", Path: "0f1e2d3c-1111-2222-3333-444455556666", Folder: "DEV", Running: true},
		{Name: "db [1]", Path: "aaaaaaaa-1111-2222-3333-444455556666", Folder: "PROD", OS: "ubuntu-64"},
		{Name: "it's", Path: "bbbbbbbb-1111-2222-3333-444455556666", Folder: core.UngroupedFolder},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetPowerState =\n%+v\nwant\n%+v", got, want)
	}
}

func TestPSPowerAndCheckpoints(t *testing.T) {
	log := withPwsh(t)
	h := &HyperVBackend{}
	// By GUID and by an exact name containing wildcard characters.
	if err := h.StartVM("0f1e2d3c-1111-2222-3333-444455556666"); err != nil {
		t.Fatal(err)
	}
	if err := h.StopVM("db [1]", "hard"); err != nil {
		t.Fatal(err)
	}
	if err := h.StartVM("missing-vm"); err == nil || !strings.Contains(err.Error(), "VM not found: missing-vm") {
		t.Errorf("missing VM error = %v", err)
	}
	names, err := h.ListSnapshots("it's")
	if err != nil || !reflect.DeepEqual(names, []string{"origin", "second"}) {
		t.Errorf("ListSnapshots = %v, %v (want oldest first)", names, err)
	}
	if err := h.RevertToSnapshot("it's", "origin"); err != nil {
		t.Fatal(err)
	}
	if err := h.RevertToSnapshot("it's", "nope"); err == nil || !strings.Contains(err.Error(), "checkpoint not found") {
		t.Errorf("missing checkpoint error = %v", err)
	}
	if err := h.CreateSnapshot("web-1", "it's a 'test'"); err != nil {
		t.Fatal(err)
	}
	got := mockLog(t, log)
	for _, want := range []string{"start web-1", "stop db [1] turnoff=True", "restore origin", "checkpoint web-1 it's a 'test'"} {
		if !strings.Contains(got, want) {
			t.Errorf("mock log missing %q:\n%s", want, got)
		}
	}
}

func TestPSDirectExec(t *testing.T) {
	log := withPwsh(t)
	h := &HyperVBackend{}
	vm := core.VM{Path: "0f1e2d3c-1111-2222-3333-444455556666"}
	res, err := h.ExecInGuest(vm, core.ExecRequest{User: "admin", Pass: `p"a's$s`, Script: `ipconfig /all`, Windows: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Output, "ran: /c ipconfig /all 2>&1") {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(mockLog(t, log), `as admin pass=p"a's$s`) {
		t.Errorf("credentials not passed through intact:\n%s", mockLog(t, log))
	}
	res, err = h.ExecInGuest(vm, core.ExecRequest{User: "a", Pass: "b", Script: "fail now", Windows: true})
	if err != nil || res.ExitCode != 5 {
		t.Errorf("exit code not propagated: %+v %v", res, err)
	}
}

func TestPSGuestIPsAndOS(t *testing.T) {
	log := withPwsh(t)
	h := &HyperVBackend{}
	ips, err := h.GuestIPs(core.VM{Path: "0f1e2d3c-1111-2222-3333-444455556666"})
	if err != nil || !reflect.DeepEqual(ips, []string{"192.168.1.20"}) {
		t.Errorf("GuestIPs = %v, %v", ips, err)
	}
	if _, err := h.GuestIPs(core.VM{Path: "bbbbbbbb-1111-2222-3333-444455556666"}); err == nil {
		t.Error("VM without IP should error")
	}
	if got := h.GuestOS(core.VM{Path: "web-1"}); got != "Windows 10 Pro" {
		t.Errorf("GuestOS = %q", got)
	}
	if !strings.Contains(mockLog(t, log), "cim Name='0f1e2d3c-1111-2222-3333-444455556666'") {
		t.Errorf("KVP lookup should filter by VM GUID:\n%s", mockLog(t, log))
	}
	// A rift-os Notes tag wins without querying.
	if got := h.GuestOS(core.VM{Path: "x", OS: "ubuntu-64"}); got != "ubuntu-64" {
		t.Errorf("GuestOS with notes tag = %q", got)
	}
}
