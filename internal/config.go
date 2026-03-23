package internal

import (
	"fmt"
	"os"
	"path/filepath"
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
		VmrunPath:    os.Getenv("VMRUN_PATH"),
		VmDirectory:  os.Getenv("VM_DIRECTORY"),
		VmInventory:  os.Getenv("INVENTORY_PATH"),
		NetmapPath:   os.Getenv("NETMAP_PATH"),
		IsoDirectory: os.Getenv("ISO_DIRECTORY"),
		VdiskPath:    os.Getenv("VDISK_PATH"),
		ArchivePath:  os.Getenv("ARCHIVE_PATH"),
		LogPath:      os.Getenv("LOG_PATH"),
		Hypervisor:   os.Getenv("HYPERVISOR"),
		DefaultUser:    os.Getenv("VM_DEFAULT_USER"),
		DefaultPass:    os.Getenv("VM_DEFAULT_PASS"),
		EncryptionPass: os.Getenv("VM_ENCRYPTION_PASS"),
		AWSRegion:      os.Getenv("AWS_REGION"),
		AWSKeyDir:      os.Getenv("AWS_KEY_DIR"),
		RiftStatePath:  os.Getenv("RIFT_STATE_PATH"),
	}

	if s.VmrunPath == "" || s.VmDirectory == "" || s.VmInventory == "" || s.NetmapPath == "" || s.IsoDirectory == "" || s.VdiskPath == "" {
		return Settings{}, fmt.Errorf("all environment variables must be set in .env (VMRUN_PATH, VM_DIRECTORY, INVENTORY_PATH, NETMAP_PATH, ISO_DIRECTORY, VDISK_PATH)")
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
