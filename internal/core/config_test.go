package core

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEnv(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_PATH", path)
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
