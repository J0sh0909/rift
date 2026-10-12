package core

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

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

// HypervisorFactory is a function that creates a Hypervisor from Settings.
// Backends register themselves via RegisterBackend.
type HypervisorFactory func(s Settings) (Hypervisor, error)

var backendFactories = map[string]HypervisorFactory{}

// RegisterBackend registers a backend factory by name.
func RegisterBackend(name string, factory HypervisorFactory) {
	backendFactories[name] = factory
}

// backendLabels maps registered backend names to display names.
var backendLabels = map[string]string{
	"workstation": "VMware Workstation",
	"vbox":        "VirtualBox",
	"hyperv":      "Hyper-V",
	"proxmox":     "Proxmox",
}

// BackendLabel returns the human-readable name of a backend.
func BackendLabel(name string) string {
	if label, ok := backendLabels[name]; ok {
		return label
	}
	return name
}

// NewHypervisor creates a single Hypervisor from settings, with auto-detection.
// If hvFlag is non-empty, it overrides both the .env HYPERVISOR and auto-detection.
// Commands that act on targets use LoadInventory instead, which spans every
// backend on the node; NewHypervisor remains for single-backend operations.
func NewHypervisor(s Settings, hvFlag string) (Hypervisor, error) {
	backend := s.Hypervisor
	if hvFlag != "" {
		backend = hvFlag
	}
	if backend != "" {
		return CreateBackend(backend, s)
	}

	detected := DetectHypervisors(s)
	switch len(detected) {
	case 0:
		return nil, fmt.Errorf("no hypervisor configured - check .env")
	case 1:
		return CreateBackend(detected[0], s)
	}

	// Several hypervisors are installed: use the only one that has VMs.
	var withVMs []string
	var chosen Hypervisor
	for _, name := range detected {
		h, err := CreateBackend(name, s)
		if err != nil {
			LogError(ErrHypervisorInit, name, "%s", err)
			continue
		}
		vms, err := h.GetPowerState()
		if err != nil {
			LogError(ErrHypervisorInit, name, "%s", err)
			continue
		}
		if len(vms) > 0 {
			withVMs = append(withVMs, name)
			chosen = h
		}
	}
	switch len(withVMs) {
	case 0:
		return nil, fmt.Errorf("multiple hypervisors detected (%s), but none have VMs configured - use --hv flag or set HYPERVISOR in .env", strings.Join(detected, ", "))
	case 1:
		LogInfo("Implicitly selected hypervisor", "%s (only one with VMs)", withVMs[0])
		return chosen, nil
	default:
		return nil, fmt.Errorf("multiple hypervisors detected (%s) with VMs configured - use --hv flag or set HYPERVISOR in .env", strings.Join(withVMs, ", "))
	}
}

// DetectHypervisors returns the names of the hypervisor backends installed on
// this node, based on .env paths and PATH lookups.
func DetectHypervisors(s Settings) []string {
	var detected []string

	if s.VmrunPath != "" {
		if _, err := os.Stat(s.VmrunPath); err == nil {
			detected = append(detected, "workstation")
		}
	}

	if s.VBoxManagePath != "" {
		if _, err := os.Stat(s.VBoxManagePath); err == nil {
			detected = append(detected, "vbox")
		}
	} else if _, err := exec.LookPath("VBoxManage"); err == nil {
		detected = append(detected, "vbox")
	}

	// Hyper-V: only when explicitly enabled via HYPERV_ENABLED=true in .env
	if s.HyperVEnabled {
		detected = append(detected, "hyperv")
	}

	return detected
}

// NodeBackends returns the backends a node-wide command should span. An
// explicit --hv narrows it to that backend; otherwise it is HYPERVISOR from
// .env followed by every configured backend (VirtualBox also when it is on
// PATH). Unlike DetectHypervisors, a configured path that doesn't exist is
// kept, so LoadInventory reports it instead of the backend vanishing.
func NodeBackends(s Settings, hvFlag string) []string {
	if hvFlag != "" {
		return []string{hvFlag}
	}
	var names []string
	add := func(name string) {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	if s.Hypervisor != "" {
		add(s.Hypervisor)
	}
	if s.VmrunPath != "" {
		add("workstation")
	}
	if s.VBoxManagePath != "" {
		add("vbox")
	} else if _, err := exec.LookPath("VBoxManage"); err == nil {
		add("vbox")
	}
	if s.HyperVEnabled {
		add("hyperv")
	}
	return names
}

// AutoDetectedBackend reports whether NodeBackends included name only
// because it was found on PATH (VirtualBox) - nothing in .env or --hv asked
// for it, so its failures are warnings rather than errors.
func AutoDetectedBackend(s Settings, hvFlag, name string) bool {
	return name == "vbox" && hvFlag == "" && s.Hypervisor != "vbox" && s.VBoxManagePath == ""
}

// CreateBackend creates a backend by name from settings without auto-detection.
func CreateBackend(name string, s Settings) (Hypervisor, error) {
	factory, ok := backendFactories[name]
	if !ok {
		return nil, fmt.Errorf("unknown hypervisor backend %q", name)
	}
	return factory(s)
}
