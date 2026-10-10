package vmware

import (
	"fmt"

	"github.com/J0sh0909/rift/internal/core"
	"github.com/vbauerster/mpb/v8"
)

func init() {
	core.RegisterBackend("workstation-api", func(s core.Settings) (core.Hypervisor, error) {
		return &WorkstationAPIBackend{}, nil
	})
}

// WorkstationAPIBackend is a placeholder for a future VMware Workstation
// API-based backend. All methods return an error indicating the API backend
// is not yet implemented.
type WorkstationAPIBackend struct{}

var errWSAPI = fmt.Errorf("vmware workstation API backend not implemented")

func (w *WorkstationAPIBackend) GetPowerState() ([]core.VM, error)           { return nil, errWSAPI }
func (w *WorkstationAPIBackend) EnsureVMwareRunning() error                  { return nil }
func (w *WorkstationAPIBackend) StartVM(vmxPath string) error                { return errWSAPI }
func (w *WorkstationAPIBackend) StopVM(vmxPath string, mode ...string) error { return errWSAPI }
func (w *WorkstationAPIBackend) SuspendVM(vmxPath string) error              { return errWSAPI }
func (w *WorkstationAPIBackend) ResetVM(vmxPath string) error                { return errWSAPI }

func (w *WorkstationAPIBackend) RunGuestCommand(vmxPath, user, pass, interpreter, script, adminUser, adminPass string) (string, error) {
	return "", errWSAPI
}
func (w *WorkstationAPIBackend) RunGuestProgram(vmxPath, user, pass, adminUser, adminPass, program string, args ...string) (string, error) {
	return "", errWSAPI
}
func (w *WorkstationAPIBackend) CopyFileFromGuest(vmxPath, user, pass, adminUser, adminPass, guestPath, hostPath string) error {
	return errWSAPI
}
func (w *WorkstationAPIBackend) DeleteFileInGuest(vmxPath, user, pass, adminUser, adminPass, guestPath string) error {
	return errWSAPI
}
func (w *WorkstationAPIBackend) ListGuestProcesses(vmxPath, user, pass, adminUser, adminPass string) error {
	return errWSAPI
}

func (w *WorkstationAPIBackend) CreateSnapshot(vmxPath, name string) error      { return errWSAPI }
func (w *WorkstationAPIBackend) RevertToSnapshot(vmxPath, name string) error    { return errWSAPI }
func (w *WorkstationAPIBackend) DeleteSnapshot(vmxPath, name string) error      { return errWSAPI }
func (w *WorkstationAPIBackend) ListSnapshots(vmxPath string) ([]string, error) { return nil, errWSAPI }

func (w *WorkstationAPIBackend) FindOvftool() (string, error)            { return "", errWSAPI }
func (w *WorkstationAPIBackend) ExportVM(vmxPath, destPath string) error { return errWSAPI }
func (w *WorkstationAPIBackend) ExportVMWithBar(vmxPath, destPath string, bar *mpb.Bar) error {
	return errWSAPI
}
func (w *WorkstationAPIBackend) ImportVM(srcPath, destVmxPath string) error { return errWSAPI }
func (w *WorkstationAPIBackend) WarmEncryptionCache(_ []string)             {}

var _ core.Hypervisor = (*WorkstationAPIBackend)(nil)
