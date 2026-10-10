package vbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/J0sh0909/rift/internal/core"
	"github.com/vbauerster/mpb/v8"
)

func init() {
	core.RegisterBackend("vbox", func(s core.Settings) (core.Hypervisor, error) {
		return NewVBoxBackend(s.VBoxManagePath)
	})
}

var (
	_ core.Hypervisor      = (*VBoxBackend)(nil)
	_ core.GuestExecutor   = (*VBoxBackend)(nil)
	_ core.GuestIPReporter = (*VBoxBackend)(nil)
	_ core.GuestOSReporter = (*VBoxBackend)(nil)
)

// VBoxBackend wraps VBoxManage CLI commands for VirtualBox VM management.
type VBoxBackend struct {
	vboxManage string // path to VBoxManage
}

// VBoxVMInfo holds detailed info about a VirtualBox VM.
type VBoxVMInfo struct {
	Name     string
	UUID     string
	OSType   string
	CPUs     int
	MemoryMB int
	State    string
	NICs     []VBoxNIC
	Disks    []string // disk file paths
}

// VBoxNIC describes a VirtualBox network adapter.
type VBoxNIC struct {
	Index   int
	Type    string // connection type: nat, bridged, hostonly, intnet, none
	NICType string // hardware type: virtio, 82540EM, 82545EM, Am79C970A, Am79C973
}

// VBoxVM holds minimal info about a VirtualBox VM.
type VBoxVM struct {
	Name  string
	UUID  string
	State string
}

// NewVBoxBackend creates a VBoxBackend. path is VBOX_MANAGE_PATH from .env;
// when empty, VBoxManage is looked up in PATH.
func NewVBoxBackend(path string) (*VBoxBackend, error) {
	if path == "" {
		p, err := exec.LookPath("VBoxManage")
		if err != nil {
			return nil, fmt.Errorf("VBoxManage not found - set VBOX_MANAGE_PATH in .env or add it to PATH")
		}
		path = p
	}
	return &VBoxBackend{vboxManage: path}, nil
}

// VBoxManagePath returns the path to VBoxManage.
func (v *VBoxBackend) VBoxManagePath() string { return v.vboxManage }

func (v *VBoxBackend) run(args ...string) (string, error) {
	cmd := exec.Command(v.vboxManage, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("VBoxManage %v failed: %w\nOutput: %s", args, err, out)
	}
	return string(out), nil
}

var (
	reListLine = regexp.MustCompile(`^"(.*)"\s+\{([0-9a-fA-F-]+)\}\s*$`)
	reUUID     = regexp.MustCompile(`\{([0-9a-fA-F-]+)\}`)
	reSnapName = regexp.MustCompile(`Name:\s+(.+?)\s+\(UUID:`)
	reNetIP    = regexp.MustCompile(`/VirtualBox/GuestInfo/Net/(\d+)/V4/IP`)
)

// parseListVMs parses "VBoxManage list vms" output into name/UUID pairs,
// skipping inaccessible registrations.
func parseListVMs(out string) []VBoxVM {
	var vms []VBoxVM
	for _, line := range strings.Split(out, "\n") {
		m := reListLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || m[1] == "<inaccessible>" {
			continue
		}
		vms = append(vms, VBoxVM{Name: m[1], UUID: m[2]})
	}
	return vms
}

// ListVMs returns all registered VMs with running state (two VBoxManage calls).
func (v *VBoxBackend) ListVMs() ([]VBoxVM, error) {
	allOut, err := v.run("list", "vms")
	if err != nil {
		return nil, err
	}
	runningOut, _ := v.run("list", "runningvms")
	runningSet := make(map[string]bool)
	for _, line := range strings.Split(runningOut, "\n") {
		if m := reUUID.FindStringSubmatch(line); m != nil {
			runningSet[m[1]] = true
		}
	}
	vms := parseListVMs(allOut)
	for i := range vms {
		vms[i].State = "poweroff"
		if runningSet[vms[i].UUID] {
			vms[i].State = "running"
		}
	}
	return vms, nil
}

// DirectStartVM starts a VM in headless mode.
func (v *VBoxBackend) DirectStartVM(nameOrUUID string) error {
	_, err := v.run("startvm", nameOrUUID, "--type", "headless")
	return err
}

// DirectStopVM stops a VM. If hard is true, uses poweroff; otherwise acpipowerbutton.
func (v *VBoxBackend) DirectStopVM(nameOrUUID string, hard bool) error {
	action := "acpipowerbutton"
	if hard {
		action = "poweroff"
	}
	_, err := v.run("controlvm", nameOrUUID, action)
	return err
}

// GetVMInfo returns detailed info about a VM by parsing showvminfo output.
func (v *VBoxBackend) GetVMInfo(nameOrUUID string) (VBoxVMInfo, error) {
	out, err := v.run("showvminfo", nameOrUUID, "--machinereadable")
	if err != nil {
		return VBoxVMInfo{}, err
	}
	info := VBoxVMInfo{}
	data := parseMachineReadable(out)

	info.Name = data["name"]
	info.UUID = data["UUID"]
	info.OSType = data["ostype"]
	info.State = data["VMState"]
	fmt.Sscanf(data["cpus"], "%d", &info.CPUs)
	fmt.Sscanf(data["memory"], "%d", &info.MemoryMB)

	// Parse NICs (up to 8).
	for i := 1; i <= 8; i++ {
		nicType := data[fmt.Sprintf("nic%d", i)]
		if nicType == "" || nicType == "none" {
			continue
		}
		hwType := data[fmt.Sprintf("nictype%d", i)]
		info.NICs = append(info.NICs, VBoxNIC{Index: i, Type: nicType, NICType: hwType})
	}

	info.Disks = diskAttachments(data)
	return info, nil
}

// reAttachment matches storage attachment keys such as "SATA Controller-0-0"
// for any controller name.
var reAttachment = regexp.MustCompile(`^(.+)-(\d+)-(\d+)$`)

// diskAttachments returns attached disk image paths ordered by controller,
// port and device, so the boot disk on the first port comes first.
func diskAttachments(data map[string]string) []string {
	type att struct {
		ctl       string
		port, dev int
		path      string
	}
	var atts []att
	for k, val := range data {
		m := reAttachment.FindStringSubmatch(strings.Trim(k, `"`))
		if m == nil {
			continue
		}
		lv := strings.ToLower(val)
		if !(strings.HasSuffix(lv, ".vdi") || strings.HasSuffix(lv, ".vmdk") || strings.HasSuffix(lv, ".vhd") || strings.HasSuffix(lv, ".vhdx")) {
			continue
		}
		port, _ := strconv.Atoi(m[2])
		dev, _ := strconv.Atoi(m[3])
		atts = append(atts, att{m[1], port, dev, val})
	}
	sort.Slice(atts, func(i, j int) bool {
		a, b := atts[i], atts[j]
		if a.ctl != b.ctl {
			return a.ctl < b.ctl
		}
		if a.port != b.port {
			return a.port < b.port
		}
		return a.dev < b.dev
	})
	disks := make([]string, 0, len(atts))
	for _, a := range atts {
		disks = append(disks, a.path)
	}
	return disks
}

// ImportOVF imports an OVF/OVA file.
func (v *VBoxBackend) ImportOVF(ovfPath string) error {
	_, err := v.run("import", ovfPath)
	return err
}

// ExportOVF exports a VM to an OVF/OVA file.
func (v *VBoxBackend) ExportOVF(nameOrUUID, outputPath string) error {
	_, err := v.run("export", nameOrUUID, "-o", outputPath)
	return err
}

// CreateVM creates and registers a new VM with the given specs.
func (v *VBoxBackend) CreateVM(name, osType string, cpus, ramMB int) error {
	if _, err := v.run("createvm", "--name", name, "--ostype", osType, "--register"); err != nil {
		return err
	}
	_, err := v.run("modifyvm", name,
		"--cpus", fmt.Sprintf("%d", cpus),
		"--memory", fmt.Sprintf("%d", ramMB),
	)
	return err
}

// AddSATAController adds a SATA controller to a VM.
func (v *VBoxBackend) AddSATAController(vmName, ctlName string) error {
	_, err := v.run("storagectl", vmName, "--name", ctlName, "--add", "sata", "--controller", "IntelAhci")
	return err
}

// AttachDisk attaches a disk image to a SATA controller port.
func (v *VBoxBackend) AttachDisk(vmName, diskPath, controller string) error {
	_, err := v.run("storageattach", vmName,
		"--storagectl", controller,
		"--port", "0",
		"--device", "0",
		"--type", "hdd",
		"--medium", diskPath,
	)
	return err
}

// SnapshotCreate takes a snapshot.
func (v *VBoxBackend) SnapshotCreate(vmName, snapName string) error {
	_, err := v.run("snapshot", vmName, "take", snapName)
	return err
}

// SnapshotList returns snapshot names.
func (v *VBoxBackend) SnapshotList(vmName string) ([]string, error) {
	out, err := v.run("snapshot", vmName, "list")
	if err != nil {
		if strings.Contains(err.Error(), "does not have any snapshots") {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, m := range reSnapName.FindAllStringSubmatch(out, -1) {
		names = append(names, m[1])
	}
	return names, nil
}

// SnapshotRevert restores a snapshot.
func (v *VBoxBackend) SnapshotRevert(vmName, snapName string) error {
	_, err := v.run("snapshot", vmName, "restore", snapName)
	return err
}

// SnapshotDelete deletes a snapshot.
func (v *VBoxBackend) SnapshotDelete(vmName, snapName string) error {
	_, err := v.run("snapshot", vmName, "delete", snapName)
	return err
}

// DeleteVM unregisters and deletes a VM and its files.
func (v *VBoxBackend) DeleteVM(nameOrUUID string) error {
	_, err := v.run("unregistervm", nameOrUUID, "--delete")
	return err
}

// parseMachineReadable parses VBoxManage --machinereadable output into a map.
func parseMachineReadable(output string) map[string]string {
	data := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		key := line[:idx]
		val := strings.Trim(line[idx+1:], "\"")
		data[key] = val
	}
	return data
}

// folderFromGroups maps a VirtualBox groups value ("/DEV", "/LAB/DEV,/X",
// "/") to rift's logical folder: the last segment of the first group, the
// same "immediate parent" rule VMware inventory folders follow.
func folderFromGroups(groups string) string {
	first := strings.TrimSpace(strings.Split(groups, ",")[0])
	first = strings.Trim(first, "/")
	if first == "" {
		return core.UngroupedFolder
	}
	return path.Base(first)
}

// ---------------------------------------------------------------------------
// core.Hypervisor interface implementation
// ---------------------------------------------------------------------------

// showInfoParallelism bounds concurrent "showvminfo" calls while listing.
const showInfoParallelism = 8

// GetPowerState lists VMs with their group (folder), guest OS type and state.
// Path is the VM UUID, which stays unambiguous when two VMs share a name.
func (v *VBoxBackend) GetPowerState() ([]core.VM, error) {
	out, err := v.run("list", "vms")
	if err != nil {
		return nil, err
	}
	listed := parseListVMs(out)
	result := make([]core.VM, len(listed))

	sem := make(chan struct{}, showInfoParallelism)
	var wg sync.WaitGroup
	for i, vm := range listed {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			r := core.VM{Name: vm.Name, Path: vm.UUID, Folder: core.UngroupedFolder}
			if info, err := v.run("showvminfo", vm.UUID, "--machinereadable"); err == nil {
				data := parseMachineReadable(info)
				r.Folder = folderFromGroups(data["groups"])
				r.OS = data["ostype"]
				r.Running = data["VMState"] == "running"
			}
			result[i] = r
		}()
	}
	wg.Wait()
	return result, nil
}

func (v *VBoxBackend) EnsureVMwareRunning() error { return nil }

// StartVM implements core.Hypervisor - delegates to headless start.
func (v *VBoxBackend) StartVM(vmxPath string) error {
	_, err := v.run("startvm", vmxPath, "--type", "headless")
	return err
}

// StopVM implements core.Hypervisor.
func (v *VBoxBackend) StopVM(vmxPath string, mode ...string) error {
	action := "acpipowerbutton"
	if len(mode) > 0 && mode[0] == "hard" {
		action = "poweroff"
	}
	_, err := v.run("controlvm", vmxPath, action)
	return err
}

func (v *VBoxBackend) SuspendVM(vmxPath string) error {
	_, err := v.run("controlvm", vmxPath, "savestate")
	return err
}

func (v *VBoxBackend) ResetVM(vmxPath string) error {
	_, err := v.run("controlvm", vmxPath, "reset")
	return err
}

// ---------------------------------------------------------------------------
// Guest operations (Guest Additions via "VBoxManage guestcontrol")
// ---------------------------------------------------------------------------

// guestRun runs a program in the guest through guestcontrol and returns its
// combined output and exit code. The password is passed through a 0600 temp
// file rather than the command line, where other host users could read it.
// argv[0] is the program name as seen by the guest. When raw is true the
// arguments are passed unquoted, which cmd.exe needs to parse "/c ..." itself.
func (v *VBoxBackend) guestRun(ctx context.Context, uuid, user, pass, exe string, raw bool, argv ...string) (string, int, error) {
	pwFile, err := os.CreateTemp("", "rift-vbox-*")
	if err != nil {
		return "", -1, fmt.Errorf("creating password file: %w", err)
	}
	defer os.Remove(pwFile.Name())
	_, werr := pwFile.WriteString(pass)
	cerr := pwFile.Close()
	if werr != nil || cerr != nil {
		return "", -1, fmt.Errorf("writing password file: %v %v", werr, cerr)
	}

	args := []string{"guestcontrol", uuid, "run",
		"--username", user, "--passwordfile", pwFile.Name(),
		"--exe", exe, "--wait-stdout", "--wait-stderr"}
	if raw {
		args = append(args, "--unquoted-args")
	}
	args = append(args, "--")
	args = append(args, argv...)

	// stdout and stderr are copied by separate goroutines, so the shared
	// buffer must be locked (and must not expose bytes.Buffer.ReadFrom, which
	// truncates concurrent writes).
	var combined lockedBuffer
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, v.vboxManage, args...)
	cmd.Stdout = &combined
	cmd.Stderr = io.MultiWriter(&combined, &stderr)
	cmd.WaitDelay = 2 * time.Second // don't hang on pipes inherited by children after a timeout
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return combined.String(), -1, fmt.Errorf("guest command timed out")
	}
	if runErr == nil {
		return combined.String(), 0, nil
	}
	// VBoxManage reports its own failures (auth, no Guest Additions) as
	// "VBoxManage: error: ..."; otherwise the exit code is the guest's.
	if errText := vboxErrorText(stderr.String()); errText != "" {
		return "", -1, fmt.Errorf("%s", errText)
	}
	if ee, ok := runErr.(*exec.ExitError); ok {
		return combined.String(), ee.ExitCode(), nil
	}
	return "", -1, runErr
}

// lockedBuffer is a write-only buffer safe for concurrent writers.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// vboxErrorText returns VBoxManage's own error lines from stderr, or "".
func vboxErrorText(stderr string) string {
	var lines []string
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "VBoxManage: error:") {
			lines = append(lines, strings.TrimPrefix(l, "VBoxManage: error: "))
		}
	}
	return strings.Join(lines, "; ")
}

// ExecInGuest runs a shell command through Guest Additions.
func (v *VBoxBackend) ExecInGuest(vm core.VM, req core.ExecRequest) (core.ExecResult, error) {
	ctx, cancel := guestTimeout(req.Timeout)
	defer cancel()
	var (
		out  string
		code int
		err  error
	)
	if req.Windows {
		out, code, err = v.guestRun(ctx, vm.Path, req.User, req.Pass, core.WindowsShell, true, "cmd.exe", "/c", req.Script)
	} else {
		interp := req.Interpreter
		if interp == "" {
			interp = "/bin/sh"
		}
		out, code, err = v.guestRun(ctx, vm.Path, req.User, req.Pass, interp, false, interp, "-c", req.Script)
	}
	if err != nil {
		return core.ExecResult{}, err
	}
	return core.ExecResult{Output: out, ExitCode: code}, nil
}

// RunGuestCommand runs script with interpreter in the guest (used by bootstrap).
func (v *VBoxBackend) RunGuestCommand(vmxPath, user, pass, interpreter, script, adminUser, adminPass string) (string, error) {
	out, code, err := v.guestRun(context.Background(), vmxPath, user, pass, interpreter, false, interpreter, "-c", script)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return out, fmt.Errorf("guest command exited with code %d: %s", code, strings.TrimSpace(out))
	}
	return out, nil
}

// RunGuestProgram runs program with a raw argument string, like vmrun's
// runProgramInGuest (used by bootstrap's cmd.exe calls).
func (v *VBoxBackend) RunGuestProgram(vmxPath, user, pass, adminUser, adminPass, program string, args ...string) (string, error) {
	argv := append([]string{guestBase(program)}, args...)
	out, code, err := v.guestRun(context.Background(), vmxPath, user, pass, program, true, argv...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return out, fmt.Errorf("guest program exited with code %d: %s", code, strings.TrimSpace(out))
	}
	return out, nil
}

// guestBase returns the file name of a guest path with either separator.
func guestBase(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (v *VBoxBackend) CopyFileFromGuest(vmxPath, user, pass, adminUser, adminPass, guestPath, hostPath string) error {
	return fmt.Errorf("not supported on VirtualBox - use exec")
}

func (v *VBoxBackend) DeleteFileInGuest(vmxPath, user, pass, adminUser, adminPass, guestPath string) error {
	return fmt.Errorf("not supported on VirtualBox - use exec")
}

func (v *VBoxBackend) ListGuestProcesses(vmxPath, user, pass, adminUser, adminPass string) error {
	return fmt.Errorf("not supported on VirtualBox - use exec")
}

// GuestIPs returns the IPv4 addresses Guest Additions report, in adapter order.
func (v *VBoxBackend) GuestIPs(vm core.VM) ([]string, error) {
	out, err := v.run("guestproperty", "enumerate", vm.Path)
	if err != nil {
		return nil, err
	}
	ips := parseGuestIPs(out)
	if len(ips) == 0 {
		return nil, fmt.Errorf("no IP reported (Guest Additions not running?)")
	}
	return ips, nil
}

// parseGuestIPs extracts /VirtualBox/GuestInfo/Net/<n>/V4/IP values. It
// accepts both the VirtualBox 6 ("Name: ..., value: ...") and 7
// ("name = 'value' @ ...") enumerate formats.
func parseGuestIPs(out string) []string {
	type entry struct {
		idx int
		ip  string
	}
	var found []entry
	for _, line := range strings.Split(out, "\n") {
		loc := reNetIP.FindStringSubmatchIndex(line)
		if loc == nil {
			continue
		}
		idx, _ := strconv.Atoi(line[loc[2]:loc[3]])
		if ip := core.FirstIPv4(line[loc[1]:]); ip != "" {
			found = append(found, entry{idx, ip})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].idx < found[j].idx })
	ips := make([]string, 0, len(found))
	for _, e := range found {
		ips = append(ips, e.ip)
	}
	return ips
}

// GuestOS returns the VirtualBox ostype collected while listing.
func (v *VBoxBackend) GuestOS(vm core.VM) string {
	if vm.OS != "" {
		return vm.OS
	}
	if out, err := v.run("showvminfo", vm.Path, "--machinereadable"); err == nil {
		return parseMachineReadable(out)["ostype"]
	}
	return ""
}

// ---------------------------------------------------------------------------
// Snapshots and archives
// ---------------------------------------------------------------------------

func (v *VBoxBackend) CreateSnapshot(vmxPath, name string) error {
	_, err := v.run("snapshot", vmxPath, "take", name)
	return err
}

func (v *VBoxBackend) RevertToSnapshot(vmxPath, name string) error {
	_, err := v.run("snapshot", vmxPath, "restore", name)
	return err
}

func (v *VBoxBackend) DeleteSnapshot(vmxPath, name string) error {
	_, err := v.run("snapshot", vmxPath, "delete", name)
	return err
}

func (v *VBoxBackend) ListSnapshots(vmxPath string) ([]string, error) {
	return v.SnapshotList(vmxPath)
}

func (v *VBoxBackend) FindOvftool() (string, error) {
	return "", fmt.Errorf("ovftool not applicable for VirtualBox - use VBoxManage export/import")
}

func (v *VBoxBackend) ExportVM(vmxPath, destPath string) error {
	return v.ExportOVF(vmxPath, destPath)
}

func (v *VBoxBackend) ExportVMWithBar(vmxPath, destPath string, bar *mpb.Bar) error {
	err := v.ExportOVF(vmxPath, destPath)
	if err == nil {
		bar.SetCurrent(100)
	}
	return err
}

func (v *VBoxBackend) ImportVM(srcPath, destVmxPath string) error {
	return v.ImportOVF(srcPath)
}

func (v *VBoxBackend) WarmEncryptionCache(_ []string) {}

// guestTimeout returns a context bounded by d, or unbounded when d <= 0.
func guestTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), d)
}
