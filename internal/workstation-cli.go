package internal

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/vbauerster/mpb/v8"
)

// WorkstationBackend implements Hypervisor using VMware Workstation (vmrun).
type WorkstationBackend struct {
	s Settings

	// hostnameCache maps vmxPath → cached Windows computer name.
	// Populated lazily on first auth retry for each VM.
	hostnameMu    sync.Mutex
	hostnameCache map[string]string
}

// ---------------------------------------------------------------------------
// vmrun wrapper (private)
// ---------------------------------------------------------------------------

// isEncryptedVM checks the VMX file for encryption markers (vtpm or encryption keys).
func isEncryptedVM(vmxPath string) bool {
	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return false
	}
	if strings.EqualFold(data["vtpm.present"], "TRUE") {
		return true
	}
	if strings.EqualFold(data["encryption.encryptedvmx"], "TRUE") {
		return true
	}
	if _, ok := data["encryption.keysafe"]; ok {
		return true
	}
	return false
}

// vpArgsForVM returns the -vp <password> prefix if the VM is encrypted and a
// password is configured. Prints a warning to stderr if encrypted but no
// password is available. Returns nil for non-encrypted VMs.
func (w *WorkstationBackend) vpArgsForVM(vmxPath string) []string {
	if !isEncryptedVM(vmxPath) {
		return nil
	}
	if w.s.EncryptionPass != "" {
		return []string{"-vp", w.s.EncryptionPass}
	}
	fmt.Fprintf(os.Stderr, "%s: VM is encrypted — use --vp or set VM_ENCRYPTION_PASS in .env\n", filepath.Base(filepath.Dir(vmxPath)))
	return nil
}

func wsVmrun(vmrunPath string, args ...string) (string, error) {
	cmd := exec.Command(vmrunPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("vmrun %v failed: %w\nOutput: %s", args, err, output)
	}
	return string(output), nil
}

// ---------------------------------------------------------------------------
// VM Discovery & Power State
// ---------------------------------------------------------------------------

func wsParseInventory(inventoryPath string) ([]VM, error) {
	content, err := os.ReadFile(inventoryPath)
	if err != nil {
		return nil, fmt.Errorf("reading inventory file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	entries := make(map[string]map[string]string)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "vmlist") {
			continue
		}
		parts := strings.SplitN(line, " = ", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		value := strings.Trim(parts[1], "\"")

		dotIndex := strings.Index(key, ".")
		if dotIndex == -1 {
			continue
		}
		num := key[6:dotIndex]
		prop := key[dotIndex+1:]

		if entries[num] == nil {
			entries[num] = make(map[string]string)
		}
		entries[num][prop] = value
	}

	folders := make(map[string]string)
	for _, entry := range entries {
		if entry["Type"] == "2" {
			folders[entry["ItemID"]] = entry["DisplayName"]
		}
	}

	var vms []VM
	for _, entry := range entries {
		config := entry["config"]
		if !strings.HasSuffix(config, ".vmx") {
			continue
		}
		folderName := folders[entry["ParentID"]]
		if folderName == "" {
			folderName = "Ungrouped"
		}
		vms = append(vms, VM{
			Name:   entry["DisplayName"],
			Path:   config,
			Folder: folderName,
		})
	}

	return vms, nil
}

func wsListRunning(vmrunPath string, args ...string) ([]string, error) {
	if len(args) == 0 {
		args = []string{"list"}
	}
	output, err := wsVmrun(vmrunPath, args...)
	if err != nil {
		return nil, fmt.Errorf("listing running VMs: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) <= 1 {
		return []string{}, nil
	}
	return lines[1:], nil
}

func (w *WorkstationBackend) GetPowerState() ([]VM, error) {
	vms, err := wsParseInventory(w.s.VmInventory)
	if err != nil {
		return nil, fmt.Errorf("parsing inventory: %w", err)
	}
	runningVMs, err := wsListRunning(w.s.VmrunPath)
	if err != nil {
		return nil, fmt.Errorf("listing running VMs: %w", err)
	}
	for i := range vms {
		vms[i].Running = false
		for _, runningVM := range runningVMs {
			if strings.TrimSpace(vms[i].Path) == strings.TrimSpace(runningVM) {
				vms[i].Running = true
				break
			}
		}
	}
	return vms, nil
}

// ---------------------------------------------------------------------------
// Power Operations
// ---------------------------------------------------------------------------

func (w *WorkstationBackend) StartVM(vmxPath string) error {
	args := append(w.vpArgsForVM(vmxPath), "start", vmxPath, "nogui")
	_, err := wsStartDetached(w.s.VmrunPath, args...)
	if err != nil {
		return fmt.Errorf("starting VM %s: %w", vmxPath, err)
	}
	return nil
}

func (w *WorkstationBackend) StopVM(vmxPath string, mode ...string) error {
	args := append(w.vpArgsForVM(vmxPath), "stop", vmxPath)
	if len(mode) > 0 {
		args = append(args, mode[0])
	}
	_, err := wsVmrun(w.s.VmrunPath, args...)
	if err != nil {
		return fmt.Errorf("stopping VM %s: %w", vmxPath, err)
	}
	return nil
}

func (w *WorkstationBackend) SuspendVM(vmxPath string) error {
	args := append(w.vpArgsForVM(vmxPath), "suspend", vmxPath)
	_, err := wsVmrun(w.s.VmrunPath, args...)
	if err != nil {
		return fmt.Errorf("suspending VM %s: %w", vmxPath, err)
	}
	return nil
}

func (w *WorkstationBackend) ResetVM(vmxPath string) error {
	args := append(w.vpArgsForVM(vmxPath), "reset", vmxPath)
	_, err := wsVmrun(w.s.VmrunPath, args...)
	if err != nil {
		return fmt.Errorf("resetting VM %s: %w", vmxPath, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Guest Operations
// ---------------------------------------------------------------------------

func (w *WorkstationBackend) RunGuestCommand(vmxPath, user, pass, interpreter, script, adminUser, adminPass string) (string, error) {
	args := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", user, "-gp", pass, "runScriptInGuest", vmxPath, interpreter, script)
	cmd := exec.Command(w.s.VmrunPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if isInvalidCredentials(output) && !strings.Contains(user, `\`) {
			if hostname, hErr := w.guestHostname(vmxPath, adminUser, adminPass); hErr == nil {
				qUser := qualifiedUser(hostname, user)
				retryArgs := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", qUser, "-gp", pass, "runScriptInGuest", vmxPath, interpreter, script)
				retryCmd := exec.Command(w.s.VmrunPath, retryArgs...)
				retryOut, retryErr := retryCmd.CombinedOutput()
				if retryErr == nil {
					return string(retryOut), nil
				}
			}
		}
		return "", fmt.Errorf("vmrun runScriptInGuest failed: %w\nOutput: %s", err, output)
	}
	return string(output), nil
}

func (w *WorkstationBackend) RunGuestProgram(vmxPath, user, pass, adminUser, adminPass, program string, args ...string) (string, error) {
	cmdArgs := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", user, "-gp", pass, "runProgramInGuest", vmxPath, program)
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command(w.s.VmrunPath, cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if isInvalidCredentials(output) && !strings.Contains(user, `\`) {
			if hostname, hErr := w.guestHostname(vmxPath, adminUser, adminPass); hErr == nil {
				qUser := qualifiedUser(hostname, user)
				retryArgs := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", qUser, "-gp", pass, "runProgramInGuest", vmxPath, program)
				retryArgs = append(retryArgs, args...)
				retryCmd := exec.Command(w.s.VmrunPath, retryArgs...)
				retryOut, retryErr := retryCmd.CombinedOutput()
				if retryErr == nil {
					return string(retryOut), nil
				}
			}
		}
		return "", fmt.Errorf("vmrun runProgramInGuest failed: %w\nOutput: %s", err, output)
	}
	return string(output), nil
}

func (w *WorkstationBackend) CopyFileFromGuest(vmxPath, user, pass, adminUser, adminPass, guestPath, hostPath string) error {
	args := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", user, "-gp", pass, "copyFileFromGuestToHost", vmxPath, guestPath, hostPath)
	_, err := wsVmrun(w.s.VmrunPath, args...)
	if err != nil {
		if isInvalidCredentials([]byte(err.Error())) && !strings.Contains(user, `\`) {
			if hostname, hErr := w.guestHostname(vmxPath, adminUser, adminPass); hErr == nil {
				qUser := qualifiedUser(hostname, user)
				retryArgs := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", qUser, "-gp", pass, "copyFileFromGuestToHost", vmxPath, guestPath, hostPath)
				if _, retryErr := wsVmrun(w.s.VmrunPath, retryArgs...); retryErr == nil {
					return nil
				}
			}
		}
		return fmt.Errorf("copyFileFromGuestToHost failed: %w", err)
	}
	return nil
}

func (w *WorkstationBackend) DeleteFileInGuest(vmxPath, user, pass, adminUser, adminPass, guestPath string) error {
	args := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", user, "-gp", pass, "deleteFileInGuest", vmxPath, guestPath)
	_, err := wsVmrun(w.s.VmrunPath, args...)
	if err != nil {
		if isInvalidCredentials([]byte(err.Error())) && !strings.Contains(user, `\`) {
			if hostname, hErr := w.guestHostname(vmxPath, adminUser, adminPass); hErr == nil {
				qUser := qualifiedUser(hostname, user)
				retryArgs := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", qUser, "-gp", pass, "deleteFileInGuest", vmxPath, guestPath)
				if _, retryErr := wsVmrun(w.s.VmrunPath, retryArgs...); retryErr == nil {
					return nil
				}
			}
		}
		return fmt.Errorf("deleteFileInGuest failed: %w", err)
	}
	return nil
}

func (w *WorkstationBackend) ListGuestProcesses(vmxPath, user, pass, adminUser, adminPass string) error {
	args := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", user, "-gp", pass, "listProcessesInGuest", vmxPath)
	_, err := wsVmrun(w.s.VmrunPath, args...)
	if err != nil {
		if isInvalidCredentials([]byte(err.Error())) && !strings.Contains(user, `\`) {
			if hostname, hErr := w.guestHostname(vmxPath, adminUser, adminPass); hErr == nil {
				qUser := qualifiedUser(hostname, user)
				retryArgs := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", qUser, "-gp", pass, "listProcessesInGuest", vmxPath)
				if _, retryErr := wsVmrun(w.s.VmrunPath, retryArgs...); retryErr == nil {
					return nil
				}
			}
		}
	}
	return err
}

// ---------------------------------------------------------------------------
// Windows hostname-prefixed auth retry
// ---------------------------------------------------------------------------

// isInvalidCredentials checks if vmrun output indicates bad credentials.
// The error message appears in vmrun's stdout/stderr output, not in Go's
// exec error (which is just "exit status N").
func isInvalidCredentials(output []byte) bool {
	return strings.Contains(string(output), "Invalid user name or password")
}

// guestHostname returns the Windows computer name for a VM, caching the result.
// It runs "cmd /c hostname" using the supplied admin credentials.
func (w *WorkstationBackend) guestHostname(vmxPath, adminUser, adminPass string) (string, error) {
	w.hostnameMu.Lock()
	if h, ok := w.hostnameCache[vmxPath]; ok {
		w.hostnameMu.Unlock()
		return h, nil
	}
	w.hostnameMu.Unlock()

	if adminUser == "" || adminPass == "" {
		return "", fmt.Errorf("no admin credentials provided for hostname lookup")
	}

	// Use runProgramInGuest with output redirected to a temp file, then copy
	// it back. Plain "hostname" produces console output which causes vmrun to
	// return bogus exit codes, but redirecting to a file avoids that.
	guestTmp := `C:\Windows\Temp\rift-hostname.txt`
	progArgs := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", adminUser, "-gp", adminPass,
		"runProgramInGuest", vmxPath,
		`C:\Windows\System32\cmd.exe`, "/c hostname > "+guestTmp)
	progCmd := exec.Command(w.s.VmrunPath, progArgs...)
	if progOut, progErr := progCmd.CombinedOutput(); progErr != nil {
		return "", fmt.Errorf("hostname lookup failed: %w\nOutput: %s", progErr, progOut)
	}

	// Copy the file from the guest to a local temp file.
	hostTmp, err := os.CreateTemp("", "rift-hostname-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file for hostname: %w", err)
	}
	hostTmpPath := hostTmp.Name()
	hostTmp.Close()
	defer os.Remove(hostTmpPath)

	copyArgs := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", adminUser, "-gp", adminPass,
		"copyFileFromGuestToHost", vmxPath, guestTmp, hostTmpPath)
	copyCmd := exec.Command(w.s.VmrunPath, copyArgs...)
	if copyOut, copyErr := copyCmd.CombinedOutput(); copyErr != nil {
		return "", fmt.Errorf("copying hostname file from guest: %w\nOutput: %s", copyErr, copyOut)
	}

	// Clean up guest temp file (best-effort).
	delArgs := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", adminUser, "-gp", adminPass,
		"deleteFileInGuest", vmxPath, guestTmp)
	delCmd := exec.Command(w.s.VmrunPath, delArgs...)
	_ = delCmd.Run()

	data, err := os.ReadFile(hostTmpPath)
	if err != nil {
		return "", fmt.Errorf("reading hostname file: %w", err)
	}

	hostname := strings.TrimSpace(string(data))
	if hostname == "" {
		return "", fmt.Errorf("hostname lookup returned empty output")
	}

	w.hostnameMu.Lock()
	if w.hostnameCache == nil {
		w.hostnameCache = make(map[string]string)
	}
	w.hostnameCache[vmxPath] = hostname
	w.hostnameMu.Unlock()
	return hostname, nil
}

// qualifiedUser returns "HOSTNAME\user" if user doesn't already contain a backslash.
func qualifiedUser(hostname, user string) string {
	if strings.Contains(user, `\`) {
		return user
	}
	return hostname + `\` + user
}

// ---------------------------------------------------------------------------
// Snapshot Operations
// ---------------------------------------------------------------------------

func (w *WorkstationBackend) CreateSnapshot(vmxPath, name string) error {
	args := append(w.vpArgsForVM(vmxPath), "snapshot", vmxPath, name)
	_, err := wsVmrun(w.s.VmrunPath, args...)
	if err != nil {
		return fmt.Errorf("creating snapshot %q on %s: %w", name, vmxPath, err)
	}
	return nil
}

func (w *WorkstationBackend) RevertToSnapshot(vmxPath, name string) error {
	args := append(w.vpArgsForVM(vmxPath), "revertToSnapshot", vmxPath, name)
	_, err := wsVmrun(w.s.VmrunPath, args...)
	if err != nil {
		return fmt.Errorf("reverting %s to snapshot %q: %w", vmxPath, name, err)
	}
	return nil
}

func (w *WorkstationBackend) DeleteSnapshot(vmxPath, name string) error {
	args := append(w.vpArgsForVM(vmxPath), "deleteSnapshot", vmxPath, name)
	_, err := wsVmrun(w.s.VmrunPath, args...)
	if err != nil {
		return fmt.Errorf("deleting snapshot %q from %s: %w", name, vmxPath, err)
	}
	return nil
}

func (w *WorkstationBackend) ListSnapshots(vmxPath string) ([]string, error) {
	args := append(w.vpArgsForVM(vmxPath), "listSnapshots", vmxPath)
	out, err := wsVmrun(w.s.VmrunPath, args...)
	if err != nil {
		return nil, fmt.Errorf("listing snapshots for %s: %w", vmxPath, err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) <= 1 {
		return []string{}, nil
	}
	var snapshots []string
	for _, l := range lines[1:] {
		l = strings.TrimSpace(l)
		if l != "" {
			snapshots = append(snapshots, l)
		}
	}
	return snapshots, nil
}

// ---------------------------------------------------------------------------
// Archive & OVF Tool Operations
// ---------------------------------------------------------------------------

// RenderProgressBar prints a 50-char wide progress bar to stdout using \r so
// it overwrites the current line. Call fmt.Println() after the final update.
func RenderProgressBar(percent int) {
	const width = 50
	filled := percent * width / 100
	bar := strings.Repeat("=", filled) + strings.Repeat(" ", width-filled)
	fmt.Printf("\r[%s] %3d%%", bar, percent)
}

// splitOnCR is a bufio.SplitFunc that splits on \r, \n, or \r\n.
// ovftool separates progress updates with bare \r, not \n.
func splitOnCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, b := range data {
		if b == '\r' || b == '\n' {
			next := i + 1
			if b == '\r' && next < len(data) && data[next] == '\n' {
				next++
			}
			return next, data[:i], nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// parseOvftoolProgress returns the percentage from an ovftool progress line
// (e.g. "Disk progress: 45%" or "Progress: 45%"). Returns (0, false) if the
// line does not end with a bare integer followed by %.
func parseOvftoolProgress(line string) (int, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasSuffix(line, "%") {
		return 0, false
	}
	idx := len(line) - 1 // index of %
	start := idx
	for start > 0 && line[start-1] >= '0' && line[start-1] <= '9' {
		start--
	}
	if start == idx {
		return 0, false
	}
	n, err := strconv.Atoi(line[start:idx])
	if err != nil || n < 0 || n > 100 {
		return 0, false
	}
	return n, true
}

// wsRunOvftool runs ovftool with the given arguments, streaming stdout line by
// line. Progress lines are rendered as an in-place progress bar; all other
// lines are printed normally. Stderr is forwarded to os.Stderr.
func wsRunOvftool(ovftoolPath string, args ...string) error {
	cmd := exec.Command(ovftoolPath, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	lines := make(chan string, 64)
	var wg sync.WaitGroup

	scan := func(r io.Reader) {
		defer wg.Done()
		s := bufio.NewScanner(r)
		s.Split(splitOnCR)
		for s.Scan() {
			if line := strings.TrimSpace(s.Text()); line != "" {
				lines <- line
			}
		}
	}

	wg.Add(2)
	go scan(stdout)
	go scan(stderr)
	go func() {
		wg.Wait()
		close(lines)
	}()

	barActive := false
	for line := range lines {
		if pct, ok := parseOvftoolProgress(line); ok {
			RenderProgressBar(pct)
			barActive = true
		} else {
			if barActive {
				fmt.Println()
				barActive = false
			}
			fmt.Println(line)
		}
	}
	if barActive {
		fmt.Println()
	}

	return cmd.Wait()
}

// wsRunOvftoolWithBar runs ovftool, updating bar with disk progress percentages.
// Non-progress output is discarded since the bar owns the display.
func wsRunOvftoolWithBar(ovftoolPath string, bar *mpb.Bar, args ...string) error {
	cmd := exec.Command(ovftoolPath, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	scan := func(r io.Reader) {
		defer wg.Done()
		s := bufio.NewScanner(r)
		s.Split(splitOnCR)
		for s.Scan() {
			if line := strings.TrimSpace(s.Text()); line != "" {
				if pct, ok := parseOvftoolProgress(line); ok {
					bar.SetCurrent(int64(pct))
				}
			}
		}
	}

	wg.Add(2)
	go scan(stdout)
	go scan(stderr)
	wg.Wait()

	return cmd.Wait()
}

// FindOvftool locates ovftool from PATH or relative to the vmrun installation.
func (w *WorkstationBackend) FindOvftool() (string, error) {
	if p, err := exec.LookPath("ovftool"); err == nil {
		return p, nil
	}
	ovftoolBin := "ovftool"
	if runtime.GOOS == "windows" {
		ovftoolBin = "ovftool.exe"
	}
	candidate := filepath.Join(filepath.Dir(w.s.VmrunPath), "OVFTool", ovftoolBin)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("ovftool not found in PATH or %s", filepath.Join(filepath.Dir(w.s.VmrunPath), "OVFTool"))
}

// ExportVM exports a VM to the given destination path using ovftool.
func (w *WorkstationBackend) ExportVM(vmxPath, destPath string) error {
	ovftoolPath, err := w.FindOvftool()
	if err != nil {
		return err
	}
	if err := wsRunOvftool(ovftoolPath, "--diskMode=thin", vmxPath, destPath); err != nil {
		return fmt.Errorf("ovftool export failed: %w", err)
	}
	return nil
}

// ExportVMWithBar exports a VM using ovftool, updating an mpb bar with progress.
func (w *WorkstationBackend) ExportVMWithBar(vmxPath, destPath string, bar *mpb.Bar) error {
	ovftoolPath, err := w.FindOvftool()
	if err != nil {
		return err
	}
	if err := wsRunOvftoolWithBar(ovftoolPath, bar, "--diskMode=thin", vmxPath, destPath); err != nil {
		return fmt.Errorf("ovftool export failed: %w", err)
	}
	return nil
}

// ImportVM imports an OVF or OVA archive using ovftool.
func (w *WorkstationBackend) ImportVM(srcPath, destVmxPath string) error {
	ovftoolPath, err := w.FindOvftool()
	if err != nil {
		return err
	}
	if err := wsRunOvftool(ovftoolPath, srcPath, destVmxPath); err != nil {
		return fmt.Errorf("ovftool import failed: %w", err)
	}
	return nil
}
