package internal

import (
	"fmt"

	"github.com/vbauerster/mpb/v8"
)

// Hypervisor abstracts VM management operations across different backends.
type Hypervisor interface {
	// GetPowerState returns all VMs with their running state.
	GetPowerState() ([]VM, error)

	// EnsureVMwareRunning ensures the hypervisor application is running before
	// VMs are started. No-op on non-Windows platforms and non-Workstation backends.
	EnsureVMwareRunning() error

	// Power operations
	StartVM(vmxPath string) error
	StopVM(vmxPath string, mode ...string) error
	SuspendVM(vmxPath string) error
	ResetVM(vmxPath string) error

	// Guest operations
	// adminUser/adminPass are optional fallback credentials for hostname-prefixed
	// auth retry on Windows guests. Pass empty strings to skip the retry.
	RunGuestCommand(vmxPath, user, pass, interpreter, script, adminUser, adminPass string) (string, error)
	RunGuestProgram(vmxPath, user, pass, adminUser, adminPass, program string, args ...string) (string, error)
	CopyFileFromGuest(vmxPath, user, pass, adminUser, adminPass, guestPath, hostPath string) error
	DeleteFileInGuest(vmxPath, user, pass, adminUser, adminPass, guestPath string) error
	ListGuestProcesses(vmxPath, user, pass, adminUser, adminPass string) error

	// Snapshot operations
	CreateSnapshot(vmxPath, name string) error
	RevertToSnapshot(vmxPath, name string) error
	DeleteSnapshot(vmxPath, name string) error
	ListSnapshots(vmxPath string) ([]string, error)

	// Archive operations
	FindOvftool() (string, error)
	ExportVM(vmxPath, destPath string) error
	ExportVMWithBar(vmxPath, destPath string, bar *mpb.Bar) error
	ImportVM(srcPath, destVmxPath string) error

	// WarmEncryptionCache pre-reads VMX files sequentially so that parallel
	// power operations don't all hit the filesystem at once.
	WarmEncryptionCache(vmxPaths []string)
}

// NewHypervisor creates a Hypervisor from settings.
func NewHypervisor(s Settings) (Hypervisor, error) {
	switch s.Hypervisor {
	case "workstation", "":
		return &WorkstationBackend{s: s}, nil
	case "proxmox":
		return &ProxmoxBackend{s: s}, nil
	default:
		return nil, fmt.Errorf("unknown hypervisor backend %q", s.Hypervisor)
	}
}
