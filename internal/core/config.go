package core

import (
	"bytes"
	"errors"
	"fmt"
	"os"
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

// errNoHypervisor is LoadSettings' error for a .env that configures nothing.
var errNoHypervisor = errors.New("no hypervisor configured in .env - set at least one of: VMRUN_PATH, VBOX_MANAGE_PATH, HYPERV_ENABLED, PROXMOX_HOST, or AWS_REGION")

// SettingsErrorCode returns the error code for a LoadSettings error: a
// missing setting when the .env configures nothing, otherwise a .env that
// could not be read or parsed.
func SettingsErrorCode(err error) string {
	if errors.Is(err, errNoHypervisor) {
		return ErrMissingSetting
	}
	return ErrEnvNotFound
}

func LoadSettings() (Settings, error) {
	envFile := ".env"
	if p := os.Getenv("ENV_PATH"); p != "" {
		envFile = p
	}
	if err := loadEnvFile(envFile); err != nil {
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
		return Settings{}, errNoHypervisor
	}

	// Best-effort: initialize file logging if LOG_PATH is set.
	// Archive folders are created by "archive export" when needed, not
	// here: a folder rift can't reach must not break unrelated commands.
	_ = InitLogging(s.LogPath)

	return s, nil
}

// loadEnvFile sets the variables of the .env file at path, parsed by
// godotenv. The GitHub Actions runner exports every line of <runner dir>\.env
// into each job verbatim (no quote, comment or $ handling), and on the nodes
// that file is rift's .env. godotenv.Load would keep those raw copies (a
// quoted path keeps its quotes); godotenv.Overload would replace them but
// also override variables set on purpose and expand '$' in an unquoted
// password ("Summer$2024" -> "Summer"). So a variable already in the
// environment is replaced only when it is the runner's raw copy of the line
// and the raw text is visibly mangled by quotes, padding or a comment.
func loadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data = bytes.TrimPrefix(data, []byte("\uFEFF"))
	parsed, err := godotenv.UnmarshalBytes(data)
	if err != nil {
		return err
	}

	// The runner's view of the file: key and value split at the first '=',
	// neither trimmed, later lines win.
	raw := map[string]string{}
	commentOnly := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		value := line[i+1:]
		raw[line[:i]] = value
		// godotenv reads "KEY=   # comment" as the value "# comment".
		key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[:i]), "export "))
		padded := strings.HasPrefix(value, " ") || strings.HasPrefix(value, "\t")
		commentOnly[key] = padded && strings.HasPrefix(strings.TrimSpace(value), "#")
	}

	for k, v := range parsed {
		if commentOnly[k] {
			v = ""
		}
		cur, set := os.LookupEnv(k)
		if set {
			r, fromRunner := raw[k]
			if !fromRunner || cur != r || v == cur || !runnerMangled(cur) {
				continue
			}
		}
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}

// runnerMangled reports whether a raw .env value differs from what a .env
// parser makes of it: padded, quoted, or followed by an inline comment.
func runnerMangled(r string) bool {
	return r != strings.TrimSpace(r) || strings.HasPrefix(r, `"`) || strings.HasPrefix(r, "'") ||
		strings.Contains(r, " #") || strings.Contains(r, "\t#")
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
