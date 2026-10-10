package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Settings struct {
	VmrunPath      string
	VmDirectory    string
	VmInventory    string
	NetmapPath     string
	IsoDirectory   string
	VdiskPath      string
	ArchivePath    string
	LogPath        string
	Hypervisor     string
	DefaultUser    string
	DefaultPass    string
	EncryptionPass string
	AWSRegion      string
	AWSKeyDir      string
	RiftStatePath  string
	QemuImgPath    string

	// VirtualBox
	VBoxManagePath string

	// Hyper-V
	HyperVEnabled bool

	// Proxmox
	ProxmoxHost        string
	ProxmoxUser        string
	ProxmoxTokenID     string
	ProxmoxTokenSecret string

	// Node / pipeline
	NodeName    string // RIFT_NODE: display name of this node (default: hostname)
	MaxParallel int    // RIFT_MAX_PARALLEL: per-backend concurrency cap (0 = default)

	// Guest SSH (used for Hyper-V Linux guests)
	SSHPort       int    // VM_SSH_PORT (default 22)
	SSHKnownHosts string // VM_SSH_KNOWN_HOSTS (default ~/.rift/known_hosts)
}

// HostResources holds detected host hardware limits
type HostResources struct {
	TotalRAMGB int
	CPUCores   int
	CPUThreads int
	FreeDiskGB map[string]int // drive letter (Windows) or mount point (Linux) → free GB
}

func LoadSettings() (Settings, error) {
	envFile := ".env"
	if p := os.Getenv("ENV_PATH"); p != "" {
		envFile = p
	}
	if err := godotenv.Load(envFile); err != nil {
		return Settings{}, fmt.Errorf("loading .env: %w", err)
	}

	s := Settings{
		VmrunPath:      os.Getenv("VMRUN_PATH"),
		VmDirectory:    os.Getenv("VM_DIRECTORY"),
		VmInventory:    os.Getenv("INVENTORY_PATH"),
		NetmapPath:     os.Getenv("NETMAP_PATH"),
		IsoDirectory:   os.Getenv("ISO_DIRECTORY"),
		VdiskPath:      os.Getenv("VDISK_PATH"),
		ArchivePath:    os.Getenv("ARCHIVE_PATH"),
		LogPath:        os.Getenv("LOG_PATH"),
		Hypervisor:     os.Getenv("HYPERVISOR"),
		DefaultUser:    os.Getenv("VM_DEFAULT_USER"),
		DefaultPass:    os.Getenv("VM_DEFAULT_PASS"),
		EncryptionPass: os.Getenv("VM_ENCRYPTION_PASS"),
		AWSRegion:      os.Getenv("AWS_REGION"),
		AWSKeyDir:      os.Getenv("AWS_KEY_DIR"),
		RiftStatePath:  os.Getenv("RIFT_STATE_PATH"),
		QemuImgPath:    os.Getenv("QEMU_IMG_PATH"),

		// VirtualBox
		VBoxManagePath: os.Getenv("VBOX_MANAGE_PATH"),

		// Hyper-V
		HyperVEnabled: strings.EqualFold(os.Getenv("HYPERV_ENABLED"), "true"),

		// Proxmox
		ProxmoxHost:        os.Getenv("PROXMOX_HOST"),
		ProxmoxUser:        os.Getenv("PROXMOX_USER"),
		ProxmoxTokenID:     os.Getenv("PROXMOX_TOKEN_ID"),
		ProxmoxTokenSecret: os.Getenv("PROXMOX_TOKEN_SECRET"),

		NodeName:      os.Getenv("RIFT_NODE"),
		SSHKnownHosts: os.Getenv("VM_SSH_KNOWN_HOSTS"),
	}
	s.MaxParallel, _ = strconv.Atoi(os.Getenv("RIFT_MAX_PARALLEL"))
	s.SSHPort, _ = strconv.Atoi(os.Getenv("VM_SSH_PORT"))

	// Multi-hypervisor: don't error on empty hypervisor-specific fields.
	// Only error if absolutely nothing is configured.
	hasVMware := s.VmrunPath != ""
	hasVBox := s.VBoxManagePath != ""
	hasHyperV := s.HyperVEnabled
	hasProxmox := s.ProxmoxHost != ""
	hasAWS := s.AWSRegion != "" || s.AWSKeyDir != ""
	hasExplicit := s.Hypervisor != ""

	if !hasVMware && !hasVBox && !hasHyperV && !hasProxmox && !hasAWS && !hasExplicit {
		return Settings{}, fmt.Errorf("no hypervisor configured in .env - set at least one of: VMRUN_PATH, VBOX_MANAGE_PATH, HYPERV_ENABLED, PROXMOX_HOST, or AWS_REGION")
	}

	// Best-effort: initialize file logging if LOG_PATH is set.
	_ = InitLogging(s.LogPath)

	if s.ArchivePath != "" {
		for _, sub := range []string{"OVF", "OVA"} {
			dir := filepath.Join(s.ArchivePath, sub)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return Settings{}, fmt.Errorf("creating archive subdirectory %s: %w", dir, err)
			}
		}
	}

	return s, nil
}

// Node returns the display name of this node: RIFT_NODE, else the hostname.
func (s Settings) Node() string {
	if s.NodeName != "" {
		return s.NodeName
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "local"
}

// GBtoMB converts GB to MB (for VMX memsize)
func GBtoMB(gb int) int {
	return gb * 1024
}

// MBtoKB converts MB to KB (for VMX graphicsMemoryKB)
func MBtoKB(mb int) int {
	return mb * 1024
}

// ---------------------------------------------------------------------------
// Input Validators
// ---------------------------------------------------------------------------

// ValidateVMName checks that a VM name contains only alphanumeric characters,
// hyphens, and underscores, and does not exceed 80 characters.
func ValidateVMName(name string) error {
	if name == "" {
		return fmt.Errorf("VM name cannot be empty")
	}
	if len(name) > 80 {
		return fmt.Errorf("VM name exceeds maximum length of 80 characters")
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return fmt.Errorf("VM name contains invalid character %q (only alphanumeric, hyphens, and underscores allowed)", c)
		}
	}
	return nil
}

// ValidateFormat normalizes format to lowercase and checks that it is either
// "ovf" or "ova". Returns the normalized value on success.
func ValidateFormat(format string) (string, error) {
	norm := strings.ToLower(format)
	switch norm {
	case "ovf", "ova":
		return norm, nil
	default:
		return "", fmt.Errorf("invalid format %q: must be \"ovf\" or \"ova\"", format)
	}
}
