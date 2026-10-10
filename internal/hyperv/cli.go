package hyperv

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/J0sh0909/rift/internal/core"
	"github.com/J0sh0909/rift/internal/guestssh"
	"github.com/vbauerster/mpb/v8"
)

// HyperVBackend implements core.Hypervisor using PowerShell cmdlets.
//
// Folders: a VM's folder is its Hyper-V VM group (Get-VMGroup), or a
// "rift-folder: NAME" line in the VM's Notes, which takes precedence.
// A "rift-os: windows|linux|<os>" Notes line tells exec which shell to use
// when integration services don't report the guest OS.
//
// Guest exec: Windows guests use PowerShell Direct; other guests use SSH to
// the IPv4 address reported by integration services.
type HyperVBackend struct {
	s core.Settings

	ipOnce sync.Once
	ips    map[string][]string // VM Id -> IPv4 addresses
	ipErr  error
}

var (
	_ core.Hypervisor      = (*HyperVBackend)(nil)
	_ core.GuestExecutor   = (*HyperVBackend)(nil)
	_ core.GuestIPReporter = (*HyperVBackend)(nil)
	_ core.GuestOSReporter = (*HyperVBackend)(nil)
)

func init() {
	core.RegisterBackend("hyperv", func(s core.Settings) (core.Hypervisor, error) {
		if runtime.GOOS != "windows" {
			return nil, fmt.Errorf("Hyper-V is only available on Windows")
		}
		return &HyperVBackend{s: s}, nil
	})
}

// ---------------------------------------------------------------------------
// PowerShell runner
// ---------------------------------------------------------------------------

// psPreamble makes every script fail fast, keeps progress records out of the
// output, emits UTF-8 (non-English Windows otherwise mangles accents), and
// defines Get-RiftVM to select the VM named by $env:RIFT_VM (GUID or exact
// name - Get-VM -Name would treat [ and * as wildcards).
const psPreamble = `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
function Get-RiftVM {
  if ($env:RIFT_VM -match '^[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$') {
    $vm = Get-VM -Id $env:RIFT_VM
  } else {
    $vm = Get-VM | Where-Object { $_.Name -eq $env:RIFT_VM } | Select-Object -First 1
  }
  if (-not $vm) { throw "VM not found: $env:RIFT_VM" }
  $vm
}
`

// psExe and psPrelude are variables so tests can run the real scripts under
// PowerShell 7 with mocked Hyper-V cmdlets.
var (
	psExe     = "powershell"
	psPrelude = psPreamble
)

// encodeCommand encodes a script for powershell -EncodedCommand (UTF-16LE,
// base64), which avoids every command-line quoting problem.
func encodeCommand(script string) string {
	u := utf16.Encode([]rune(script))
	buf := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(buf[i*2:], c)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// psRun runs script and returns its stdout. Values (VM names, credentials,
// user scripts) travel as RIFT_* environment variables, never in the script
// text or on the process command line.
func psRun(ctx context.Context, script string, env map[string]string) (string, error) {
	cmd := exec.CommandContext(ctx, psExe, "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-EncodedCommand", encodeCommand(psPrelude+script))
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.String(), fmt.Errorf("powershell timed out")
	}
	// Windows PowerShell can prefix redirected UTF-8 output with a BOM.
	out := strings.TrimPrefix(stdout.String(), "\uFEFF")
	if err != nil {
		msg := psErrorText(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(out)
		}
		return out, fmt.Errorf("powershell: %s", msg)
	}
	return out, nil
}

// psErrorText condenses a PowerShell error record to its message lines.
func psErrorText(stderr string) string {
	var lines []string
	for _, l := range strings.Split(stderr, "\n") {
		l = strings.TrimSpace(l)
		isLocation := strings.HasPrefix(l, "At ") && strings.Contains(l, "char:")
		if l == "" || strings.HasPrefix(l, "+ ") || isLocation {
			continue
		}
		lines = append(lines, l)
		if len(lines) == 3 {
			break
		}
	}
	return strings.Join(lines, " ")
}

func vmEnv(vmPath string, extra ...string) map[string]string {
	env := map[string]string{"RIFT_VM": vmPath}
	for i := 0; i+1 < len(extra); i += 2 {
		env[extra[i]] = extra[i+1]
	}
	return env
}

// ---------------------------------------------------------------------------
// Listing
// ---------------------------------------------------------------------------

const listScript = `$groups = @{}
try {
  foreach ($g in Get-VMGroup -ErrorAction Stop) {
    foreach ($m in $g.VMMembers) {
      $id = [string]$m.Id
      if (-not $groups.ContainsKey($id)) { $groups[$id] = $g.Name }
    }
  }
} catch {}
$out = @(foreach ($vm in Get-VM) {
  $id = [string]$vm.Id
  [pscustomobject]@{ Name = $vm.Name; Id = $id; State = [int]$vm.State; Group = [string]$groups[$id]; Notes = [string]$vm.Notes }
})
[Console]::Out.Write((ConvertTo-Json -InputObject $out -Compress))
`

type psVM struct {
	Name  string `json:"Name"`
	ID    string `json:"Id"`
	State int    `json:"State"`
	Group string `json:"Group"`
	Notes string `json:"Notes"`
}

// vmStateRunning is Microsoft.HyperV.PowerShell.VMState.Running.
const vmStateRunning = 2

var (
	reNotesFolder = regexp.MustCompile(`(?im)^\s*rift[-_]folder\s*[:=]\s*(.+?)\s*$`)
	reNotesOS     = regexp.MustCompile(`(?im)^\s*rift[-_]os\s*[:=]\s*(.+?)\s*$`)
)

// parseVMList converts the list script's JSON (an array, a single object on
// some PowerShell versions, or nothing) into VMs.
func parseVMList(out string) ([]core.VM, error) {
	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return nil, nil
	}
	var raw []*psVM
	if strings.HasPrefix(out, "{") {
		var one psVM
		if err := json.Unmarshal([]byte(out), &one); err != nil {
			return nil, fmt.Errorf("parsing Hyper-V VM list: %w", err)
		}
		raw = []*psVM{&one}
	} else if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("parsing Hyper-V VM list: %w", err)
	}

	var vms []core.VM
	for _, r := range raw {
		if r == nil || r.Name == "" {
			continue
		}
		vm := core.VM{Name: r.Name, Path: r.ID, Folder: r.Group, Running: r.State == vmStateRunning}
		if vm.Path == "" {
			vm.Path = r.Name
		}
		if m := reNotesFolder.FindStringSubmatch(r.Notes); m != nil {
			vm.Folder = m[1]
		}
		if m := reNotesOS.FindStringSubmatch(r.Notes); m != nil {
			vm.OS = m[1]
		}
		if vm.Folder == "" {
			vm.Folder = core.UngroupedFolder
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

func (h *HyperVBackend) GetPowerState() ([]core.VM, error) {
	out, err := psRun(context.Background(), listScript, nil)
	if err != nil {
		return nil, err
	}
	return parseVMList(out)
}

func (h *HyperVBackend) EnsureVMwareRunning() error { return nil }

// ---------------------------------------------------------------------------
// Power
// ---------------------------------------------------------------------------

func (h *HyperVBackend) vmCmd(vmPath, script string, extra ...string) error {
	_, err := psRun(context.Background(), script, vmEnv(vmPath, extra...))
	return err
}

func (h *HyperVBackend) StartVM(vmPath string) error {
	return h.vmCmd(vmPath, `Get-RiftVM | Start-VM`)
}

// StopVM shuts the guest down through integration services, or turns the VM
// off immediately with mode "hard" (like pulling the plug, as on VMware).
func (h *HyperVBackend) StopVM(vmPath string, mode ...string) error {
	if len(mode) > 0 && mode[0] == "hard" {
		return h.vmCmd(vmPath, `Get-RiftVM | Stop-VM -TurnOff -Force`)
	}
	return h.vmCmd(vmPath, `Get-RiftVM | Stop-VM`)
}

func (h *HyperVBackend) SuspendVM(vmPath string) error {
	return h.vmCmd(vmPath, `Get-RiftVM | Save-VM`)
}

func (h *HyperVBackend) ResetVM(vmPath string) error {
	return h.vmCmd(vmPath, `Get-RiftVM | Restart-VM -Force`)
}

// ---------------------------------------------------------------------------
// Guest operations
// ---------------------------------------------------------------------------

const psDirectScript = `$vm = Get-RiftVM
$sec = ConvertTo-SecureString $env:RIFT_GUEST_PASS -AsPlainText -Force
$cred = New-Object System.Management.Automation.PSCredential ($env:RIFT_GUEST_USER, $sec)
$r = Invoke-Command -VMId $vm.Id -Credential $cred -ArgumentList $env:RIFT_SCRIPT -ScriptBlock {
  param($c)
  $o = cmd.exe /c "$c 2>&1" | Out-String
  @{ Output = $o; ExitCode = $LASTEXITCODE }
}
[Console]::Out.Write((ConvertTo-Json -InputObject @{ Output = [string]$r.Output; ExitCode = [int]$r.ExitCode } -Compress))
`

// ExecInGuest runs a command through PowerShell Direct (Windows guests) or
// SSH (everything else).
func (h *HyperVBackend) ExecInGuest(vm core.VM, req core.ExecRequest) (core.ExecResult, error) {
	if !req.Windows {
		return h.execSSH(vm, req)
	}
	ctx := context.Background()
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	out, err := psRun(ctx, psDirectScript, vmEnv(vm.Path,
		"RIFT_GUEST_USER", req.User, "RIFT_GUEST_PASS", req.Pass, "RIFT_SCRIPT", req.Script))
	if err != nil {
		return core.ExecResult{}, err
	}
	var res struct {
		Output   string
		ExitCode int
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		return core.ExecResult{}, fmt.Errorf("parsing PowerShell Direct result: %w", err)
	}
	return core.ExecResult{Output: strings.ReplaceAll(res.Output, "\r\n", "\n"), ExitCode: res.ExitCode}, nil
}

func (h *HyperVBackend) execSSH(vm core.VM, req core.ExecRequest) (core.ExecResult, error) {
	ips, err := h.GuestIPs(vm)
	if err != nil {
		return core.ExecResult{}, fmt.Errorf("no SSH address: %w", err)
	}
	out, code, err := guestssh.Run(guestssh.Config{
		Host:           ips[0],
		Port:           h.s.SSHPort,
		User:           req.User,
		Password:       req.Pass,
		KnownHostsPath: h.s.SSHKnownHosts,
		Timeout:        req.Timeout,
	}, req.Interpreter, req.Script)
	if err != nil {
		return core.ExecResult{}, err
	}
	return core.ExecResult{Output: out, ExitCode: code}, nil
}

// RunGuestCommand runs script with interpreter over SSH (used by bootstrap).
func (h *HyperVBackend) RunGuestCommand(vmPath, user, pass, interpreter, script, adminUser, adminPass string) (string, error) {
	res, err := h.execSSH(core.VM{Path: vmPath}, core.ExecRequest{User: user, Pass: pass, Interpreter: interpreter, Script: script})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return res.Output, fmt.Errorf("guest command exited with code %d: %s", res.ExitCode, strings.TrimSpace(res.Output))
	}
	return res.Output, nil
}

// RunGuestProgram runs program with a raw argument string through
// PowerShell Direct, like vmrun's runProgramInGuest (used by bootstrap).
func (h *HyperVBackend) RunGuestProgram(vmPath, user, pass, adminUser, adminPass, program string, args ...string) (string, error) {
	line := `"` + program + `"`
	if len(args) > 0 {
		line += " " + strings.Join(args, " ")
	}
	res, err := h.ExecInGuest(core.VM{Path: vmPath}, core.ExecRequest{User: user, Pass: pass, Script: line, Windows: true})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return res.Output, fmt.Errorf("guest program exited with code %d: %s", res.ExitCode, strings.TrimSpace(res.Output))
	}
	return res.Output, nil
}

func (h *HyperVBackend) CopyFileFromGuest(vmPath, user, pass, adminUser, adminPass, guestPath, hostPath string) error {
	return fmt.Errorf("not supported on Hyper-V - use exec")
}

func (h *HyperVBackend) DeleteFileInGuest(vmPath, user, pass, adminUser, adminPass, guestPath string) error {
	return fmt.Errorf("not supported on Hyper-V - use exec")
}

func (h *HyperVBackend) ListGuestProcesses(vmPath, user, pass, adminUser, adminPass string) error {
	return fmt.Errorf("not supported on Hyper-V - use exec")
}

const ipScript = `$out = @(foreach ($vm in Get-VM) {
  $id = [string]$vm.Id
  foreach ($a in (Get-VMNetworkAdapter -VM $vm)) {
    foreach ($ip in $a.IPAddresses) { [pscustomobject]@{ Id = $id; Name = $vm.Name; IP = [string]$ip } }
  }
})
[Console]::Out.Write((ConvertTo-Json -InputObject $out -Compress))
`

// parseIPList maps VM Id (and name) to usable IPv4 addresses.
func parseIPList(out string) (map[string][]string, error) {
	out = strings.TrimSpace(out)
	ips := map[string][]string{}
	if out == "" || out == "null" {
		return ips, nil
	}
	type row struct{ Id, Name, IP string }
	var rows []row
	if strings.HasPrefix(out, "{") {
		var one row
		if err := json.Unmarshal([]byte(out), &one); err != nil {
			return nil, err
		}
		rows = []row{one}
	} else if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		if !core.IsUsableIPv4(r.IP) {
			continue
		}
		for _, key := range []string{r.Id, r.Name} {
			if key != "" {
				ips[key] = append(ips[key], r.IP)
			}
		}
	}
	return ips, nil
}

// GuestIPs returns the IPv4 addresses reported by integration services. All
// VMs' adapters are fetched in one PowerShell call and cached, so "rift ip"
// over a folder costs one call rather than one per VM.
func (h *HyperVBackend) GuestIPs(vm core.VM) ([]string, error) {
	h.ipOnce.Do(func() {
		out, err := psRun(context.Background(), ipScript, nil)
		if err != nil {
			h.ipErr = err
			return
		}
		h.ips, h.ipErr = parseIPList(out)
	})
	if h.ipErr != nil {
		return nil, h.ipErr
	}
	if ips := h.ips[vm.Path]; len(ips) > 0 {
		return ips, nil
	}
	return nil, fmt.Errorf("no IP reported (integration services / hv_kvp_daemon not running?)")
}

const osNameScript = `$vm = Get-RiftVM
try {
  $cs = Get-CimInstance -Namespace root\virtualization\v2 -ClassName Msvm_ComputerSystem -Filter ("Name='" + [string]$vm.Id + "'")
  $kvp = Get-CimAssociatedInstance -InputObject $cs -ResultClassName Msvm_KvpExchangeComponent
  foreach ($item in $kvp.GuestIntrinsicExchangeItems) {
    $x = [xml]$item
    $props = $x.INSTANCE.PROPERTY
    if (($props | Where-Object { $_.NAME -eq 'Name' }).VALUE -eq 'OSName') {
      [Console]::Out.Write(($props | Where-Object { $_.NAME -eq 'Data' }).VALUE)
      break
    }
  }
} catch {}
`

// GuestOS returns the "rift-os:" Notes value, else the OS name reported by
// integration services (KVP), else "".
func (h *HyperVBackend) GuestOS(vm core.VM) string {
	if vm.OS != "" {
		return vm.OS
	}
	out, err := psRun(context.Background(), osNameScript, vmEnv(vm.Path))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ---------------------------------------------------------------------------
// Snapshots (checkpoints) and archives
// ---------------------------------------------------------------------------

func (h *HyperVBackend) CreateSnapshot(vmPath, name string) error {
	return h.vmCmd(vmPath, `Get-RiftVM | Checkpoint-VM -SnapshotName $env:RIFT_NAME`, "RIFT_NAME", name)
}

const checkpointSelect = `$cp = Get-RiftVM | Get-VMCheckpoint | Where-Object { $_.Name -eq $env:RIFT_NAME } | Select-Object -First 1
if (-not $cp) { throw "checkpoint not found: $env:RIFT_NAME" }
`

func (h *HyperVBackend) RevertToSnapshot(vmPath, name string) error {
	return h.vmCmd(vmPath, checkpointSelect+`$cp | Restore-VMCheckpoint -Confirm:$false`, "RIFT_NAME", name)
}

func (h *HyperVBackend) DeleteSnapshot(vmPath, name string) error {
	return h.vmCmd(vmPath, checkpointSelect+`$cp | Remove-VMCheckpoint -Confirm:$false`, "RIFT_NAME", name)
}

// ListSnapshots returns checkpoint names oldest first, so "revert --origin"
// picks the original checkpoint.
func (h *HyperVBackend) ListSnapshots(vmPath string) ([]string, error) {
	out, err := psRun(context.Background(), `Get-RiftVM | Get-VMCheckpoint | Sort-Object CreationTime | ForEach-Object { $_.Name }`, vmEnv(vmPath))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

func (h *HyperVBackend) FindOvftool() (string, error) {
	return "", fmt.Errorf("ovftool not applicable for Hyper-V")
}

func (h *HyperVBackend) ExportVM(vmPath, destPath string) error {
	return h.vmCmd(vmPath, `Get-RiftVM | Export-VM -Path $env:RIFT_DEST`, "RIFT_DEST", destPath)
}

func (h *HyperVBackend) ExportVMWithBar(vmPath, destPath string, bar *mpb.Bar) error {
	err := h.ExportVM(vmPath, destPath)
	if err == nil {
		bar.SetCurrent(100)
	}
	return err
}

func (h *HyperVBackend) ImportVM(srcPath, destPath string) error {
	_, err := psRun(context.Background(), `Import-VM -Path $env:RIFT_SRC -Copy -GenerateNewId | Out-Null`, map[string]string{"RIFT_SRC": srcPath})
	return err
}

func (h *HyperVBackend) WarmEncryptionCache(_ []string) {}
