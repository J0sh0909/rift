package hyperv

import (
	"fmt"

	"github.com/J0sh0909/rift/internal/core"
	"github.com/vbauerster/mpb/v8"
)

type HyperVAPIBackend struct{}

var errHVAPI = fmt.Errorf("hyper-v API backend not implemented")

func (h *HyperVAPIBackend) GetPowerState() ([]core.VM, error)           { return nil, errHVAPI }
func (h *HyperVAPIBackend) EnsureVMwareRunning() error                  { return nil }
func (h *HyperVAPIBackend) StartVM(vmxPath string) error                { return errHVAPI }
func (h *HyperVAPIBackend) StopVM(vmxPath string, mode ...string) error { return errHVAPI }
func (h *HyperVAPIBackend) SuspendVM(vmxPath string) error              { return errHVAPI }
func (h *HyperVAPIBackend) ResetVM(vmxPath string) error                { return errHVAPI }
func (h *HyperVAPIBackend) RunGuestCommand(vmxPath, user, pass, interpreter, script, adminUser, adminPass string) (string, error) {
	return "", errHVAPI
}
func (h *HyperVAPIBackend) RunGuestProgram(vmxPath, user, pass, adminUser, adminPass, program string, args ...string) (string, error) {
	return "", errHVAPI
}
func (h *HyperVAPIBackend) CopyFileFromGuest(vmxPath, user, pass, adminUser, adminPass, guestPath, hostPath string) error {
	return errHVAPI
}
func (h *HyperVAPIBackend) DeleteFileInGuest(vmxPath, user, pass, adminUser, adminPass, guestPath string) error {
	return errHVAPI
}
func (h *HyperVAPIBackend) ListGuestProcesses(vmxPath, user, pass, adminUser, adminPass string) error {
	return errHVAPI
}
func (h *HyperVAPIBackend) CreateSnapshot(vmxPath, name string) error      { return errHVAPI }
func (h *HyperVAPIBackend) RevertToSnapshot(vmxPath, name string) error    { return errHVAPI }
func (h *HyperVAPIBackend) DeleteSnapshot(vmxPath, name string) error      { return errHVAPI }
func (h *HyperVAPIBackend) ListSnapshots(vmxPath string) ([]string, error) { return nil, errHVAPI }
func (h *HyperVAPIBackend) FindOvftool() (string, error)                   { return "", errHVAPI }
func (h *HyperVAPIBackend) ExportVM(vmxPath, destPath string) error        { return errHVAPI }
func (h *HyperVAPIBackend) ExportVMWithBar(vmxPath, destPath string, bar *mpb.Bar) error {
	return errHVAPI
}
func (h *HyperVAPIBackend) ImportVM(srcPath, destVmxPath string) error { return errHVAPI }
func (h *HyperVAPIBackend) WarmEncryptionCache(_ []string)             {}

var _ core.Hypervisor = (*HyperVAPIBackend)(nil)
