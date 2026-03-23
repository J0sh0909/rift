//go:build !windows

package internal

import (
	"os/exec"
	"strings"
	"time"
)

// wsStartDetached on non-Windows delegates to wsVmrun. Process detachment is
// only needed on Windows CI runners where the runner kills child processes.
func wsStartDetached(vmrunPath string, args ...string) (string, error) {
	return wsVmrun(vmrunPath, args...)
}

// EnsureVMwareRunning checks whether the vmware process is running on Linux.
// If not, it derives the vmware binary path from VmrunPath by replacing the
// "vmrun" filename with "vmware", starts it in the background, and waits
// briefly for it to initialize.
func (w *WorkstationBackend) EnsureVMwareRunning() error {
	out, err := exec.Command("pgrep", "-x", "vmware").Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return nil
	}

	vmwarePath := strings.Replace(w.s.VmrunPath, "vmrun", "vmware", 1)
	cmd := exec.Command(vmwarePath)
	if err := cmd.Start(); err != nil {
		return nil // best-effort: vmware may not be installed, allow vmrun to proceed
	}
	time.Sleep(3 * time.Second)
	return nil
}
