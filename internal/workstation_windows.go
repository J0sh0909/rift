//go:build windows

package internal

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// wsStartDetached on Windows delegates to wsVmrun. The nogui argument in
// StartVM ensures vmrun returns immediately without opening a GUI window.
func wsStartDetached(vmrunPath string, args ...string) (string, error) {
	return wsVmrun(vmrunPath, args...)
}

// EnsureVMwareRunning checks whether vmware.exe is already running. If it is
// not, it starts VMware Workstation minimized and waits briefly for it to
// initialize before vmrun start is called.
func (w *WorkstationBackend) EnsureVMwareRunning() error {
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq vmware.exe").Output()
	if err != nil {
		return fmt.Errorf("checking VMware Workstation status: %w", err)
	}
	if strings.Contains(strings.ToLower(string(out)), "vmware.exe") {
		return nil
	}

	vmwarePath := filepath.Join(filepath.Dir(w.s.VmrunPath), "vmware.exe")
	if err := exec.Command("cmd", "/c", "start", "", "/MIN", vmwarePath).Start(); err != nil {
		return fmt.Errorf("starting VMware Workstation: %w", err)
	}
	time.Sleep(3 * time.Second)
	return nil
}
