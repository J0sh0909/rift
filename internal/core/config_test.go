package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joho/godotenv"
)

// writeEnv writes a .env file and points ENV_PATH at it. LoadSettings sets
// the file's keys in the process environment, so each starts out unset and
// is restored when the test ends.
func writeEnv(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_PATH", path)
	keys, err := godotenv.Unmarshal(content)
	if err != nil {
		t.Fatal(err)
	}
	for k := range keys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestLoadSettingsDoesNotCreateArchiveDirs(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "archive")
	writeEnv(t, "HYPERVISOR=vbox\nARCHIVE_PATH="+archive+"\n")
	if _, err := LoadSettings(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Errorf("LoadSettings must not create %s (err=%v)", archive, err)
	}
}

func TestLoadSettingsFileWinsOverPreloadedRawValue(t *testing.T) {
	writeEnv(t, "HYPERV_ENABLED=\"true\"  # enable\nRIFT_NODE=omen # display name\n")
	// What the Actions runner puts in the environment: the raw line value.
	t.Setenv("HYPERV_ENABLED", `"true"  # enable`)
	t.Setenv("RIFT_NODE", "omen # display name")
	s, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !s.HyperVEnabled || s.NodeName != "omen" {
		t.Errorf("got HyperVEnabled=%v NodeName=%q", s.HyperVEnabled, s.NodeName)
	}
}

// godotenv expands "$2024" to nothing; the runner's raw copy of a plain
// unquoted value is already right and must be kept.
func TestLoadSettingsKeepsRunnerCopyOfDollarValue(t *testing.T) {
	writeEnv(t, "HYPERVISOR=vbox\nVM_DEFAULT_PASS=Summer$2024\n")
	t.Setenv("VM_DEFAULT_PASS", "Summer$2024")
	s, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.DefaultPass != "Summer$2024" {
		t.Errorf("DefaultPass = %q, want %q", s.DefaultPass, "Summer$2024")
	}
}

func TestLoadSettingsDeliberateEnvWins(t *testing.T) {
	writeEnv(t, "HYPERVISOR=workstation\nRIFT_NODE=\"omen\"\n")
	t.Setenv("HYPERVISOR", "vbox")
	t.Setenv("RIFT_NODE", "other")
	s, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.Hypervisor != "vbox" || s.NodeName != "other" {
		t.Errorf("got Hypervisor=%q NodeName=%q, want the environment's vbox/other", s.Hypervisor, s.NodeName)
	}
}

func TestLoadSettingsCommentOnlyValueIsEmpty(t *testing.T) {
	const value = "      # default ~/.rift/known_hosts"
	for _, preset := range []bool{false, true} {
		writeEnv(t, "HYPERVISOR=vbox\nVM_SSH_KNOWN_HOSTS="+value+"\n")
		if preset {
			t.Setenv("VM_SSH_KNOWN_HOSTS", value) // the runner's raw copy
		}
		s, err := LoadSettings()
		if err != nil {
			t.Fatal(err)
		}
		if s.SSHKnownHosts != "" {
			t.Errorf("preset=%v: SSHKnownHosts = %q, want empty", preset, s.SSHKnownHosts)
		}
	}
}

func TestLoadSettingsSingleQuotedPathIsLiteral(t *testing.T) {
	const path = `C:\Program Files\Oracle\VirtualBox\VBoxManage.exe`
	for _, preset := range []bool{false, true} {
		writeEnv(t, "VBOX_MANAGE_PATH='"+path+"'\n")
		if preset {
			t.Setenv("VBOX_MANAGE_PATH", "'"+path+"'")
		}
		s, err := LoadSettings()
		if err != nil {
			t.Fatal(err)
		}
		if s.VBoxManagePath != path {
			t.Errorf("preset=%v: VBoxManagePath = %q, want %q", preset, s.VBoxManagePath, path)
		}
	}
}

func TestSettingsErrorCode(t *testing.T) {
	t.Setenv("ENV_PATH", filepath.Join(t.TempDir(), "missing.env"))
	_, err := LoadSettings()
	if err == nil {
		t.Fatal("expected an error for a missing .env")
	}
	if got := SettingsErrorCode(err); got != ErrEnvNotFound {
		t.Errorf("missing .env: code %s, want %s", got, ErrEnvNotFound)
	}

	// Empty values keep variables from the test's environment out.
	writeEnv(t, "# nothing configured\nVMRUN_PATH=\nVBOX_MANAGE_PATH=\nHYPERV_ENABLED=\nPROXMOX_HOST=\nAWS_REGION=\nAWS_KEY_DIR=\nHYPERVISOR=\n")
	_, err = LoadSettings()
	if err == nil {
		t.Fatal("expected an error for a .env that configures nothing")
	}
	if got := SettingsErrorCode(err); got != ErrMissingSetting {
		t.Errorf("empty config: code %s, want %s", got, ErrMissingSetting)
	}
}
