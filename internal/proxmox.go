package internal

import (
	"fmt"

	"github.com/vbauerster/mpb/v8"
)

// ProxmoxBackend is a stub Hypervisor implementation for Proxmox VE.
// All methods return ErrNotImplemented until Proxmox support is added (step 2).
type ProxmoxBackend struct {
	s Settings
}

var errNotImpl = fmt.Errorf("not implemented for proxmox backend")

func (p *ProxmoxBackend) GetPowerState() ([]VM, error)                { return nil, errNotImpl }
func (p *ProxmoxBackend) EnsureVMwareRunning() error                  { return nil }
func (p *ProxmoxBackend) StartVM(vmxPath string) error                { return errNotImpl }
func (p *ProxmoxBackend) StopVM(vmxPath string, mode ...string) error { return errNotImpl }
func (p *ProxmoxBackend) SuspendVM(vmxPath string) error              { return errNotImpl }
func (p *ProxmoxBackend) ResetVM(vmxPath string) error                { return errNotImpl }

func (p *ProxmoxBackend) RunGuestCommand(vmxPath, user, pass, interpreter, script string) (string, error) {
	return "", errNotImpl
}
func (p *ProxmoxBackend) RunGuestProgram(vmxPath, user, pass, program string, args ...string) (string, error) {
	return "", errNotImpl
}
func (p *ProxmoxBackend) CopyFileFromGuest(vmxPath, user, pass, guestPath, hostPath string) error {
	return errNotImpl
}
func (p *ProxmoxBackend) DeleteFileInGuest(vmxPath, user, pass, guestPath string) error {
	return errNotImpl
}

func (p *ProxmoxBackend) CreateSnapshot(vmxPath, name string) error   { return errNotImpl }
func (p *ProxmoxBackend) RevertToSnapshot(vmxPath, name string) error { return errNotImpl }
func (p *ProxmoxBackend) DeleteSnapshot(vmxPath, name string) error   { return errNotImpl }
func (p *ProxmoxBackend) ListSnapshots(vmxPath string) ([]string, error) {
	return nil, errNotImpl
}

func (p *ProxmoxBackend) FindOvftool() (string, error)            { return "", errNotImpl }
func (p *ProxmoxBackend) ExportVM(vmxPath, destPath string) error { return errNotImpl }
func (p *ProxmoxBackend) ExportVMWithBar(vmxPath, destPath string, bar *mpb.Bar) error {
	return errNotImpl
}
func (p *ProxmoxBackend) ImportVM(srcPath, destVmxPath string) error { return errNotImpl }
