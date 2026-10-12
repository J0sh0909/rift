package core

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// ValidateVMName
// ---------------------------------------------------------------------------

func TestValidateVMName_Valid(t *testing.T) {
	valid := []string{"my-vm", "VM_01", "a", "A-Z_0-9", "win11-dev"}
	for _, name := range valid {
		if err := ValidateVMName(name); err != nil {
			t.Errorf("ValidateVMName(%q) unexpected error: %s", name, err)
		}
	}
}

func TestValidateVMName_Empty(t *testing.T) {
	if err := ValidateVMName(""); err == nil {
		t.Error("ValidateVMName(\"\") should return error")
	}
}

func TestValidateVMName_TooLong(t *testing.T) {
	long := ""
	for i := 0; i < 81; i++ {
		long += "a"
	}
	if err := ValidateVMName(long); err == nil {
		t.Error("ValidateVMName(81 chars) should return error")
	}
}

func TestValidateVMName_InvalidChars(t *testing.T) {
	invalid := []string{"my vm", "vm@host", "name.with.dots", "vm/path", "vm\\path"}
	for _, name := range invalid {
		if err := ValidateVMName(name); err == nil {
			t.Errorf("ValidateVMName(%q) should return error for invalid char", name)
		}
	}
}

// ---------------------------------------------------------------------------
// DefaultInterpreter
// ---------------------------------------------------------------------------

func TestDefaultInterpreter_Windows(t *testing.T) {
	interp, ok := DefaultInterpreter("windows10-64")
	if !ok {
		t.Fatal("DefaultInterpreter(\"windows10-64\") should return ok=true")
	}
	if interp != `C:\Windows\System32\cmd.exe` {
		t.Errorf("got %q, want cmd.exe path", interp)
	}
}

func TestDefaultInterpreter_Linux(t *testing.T) {
	cases := []struct {
		guestOS string
		want    string
	}{
		{"ubuntu-64", "/bin/bash"},
		{"debian10-64", "/bin/bash"},
		{"centos-64", "/bin/bash"},
		{"freebsd-64", "/bin/sh"},
		{"alpine-64", "/bin/sh"},
	}
	for _, tc := range cases {
		interp, ok := DefaultInterpreter(tc.guestOS)
		if !ok {
			t.Errorf("DefaultInterpreter(%q) ok=false, want true", tc.guestOS)
			continue
		}
		if interp != tc.want {
			t.Errorf("DefaultInterpreter(%q) = %q, want %q", tc.guestOS, interp, tc.want)
		}
	}
}

func TestDefaultInterpreter_Unknown(t *testing.T) {
	_, ok := DefaultInterpreter("")
	if ok {
		t.Error("DefaultInterpreter(\"\") should return ok=false")
	}
}

// ---------------------------------------------------------------------------
// ValidateFormat
// ---------------------------------------------------------------------------

func TestValidateFormat_Valid(t *testing.T) {
	cases := []struct{ input, want string }{
		{"ovf", "ovf"},
		{"OVF", "ovf"},
		{"ova", "ova"},
		{"OVA", "ova"},
	}
	for _, tc := range cases {
		got, err := ValidateFormat(tc.input)
		if err != nil {
			t.Errorf("ValidateFormat(%q) unexpected error: %s", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ValidateFormat(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestValidateFormat_Invalid(t *testing.T) {
	invalid := []string{"vmdk", "qcow2", "", "zip"}
	for _, f := range invalid {
		if _, err := ValidateFormat(f); err == nil {
			t.Errorf("ValidateFormat(%q) should return error", f)
		}
	}
}

// ---------------------------------------------------------------------------
// ParseVMXKeys
// ---------------------------------------------------------------------------

func TestParseVMXKeys(t *testing.T) {
	dir := t.TempDir()
	vmx := filepath.Join(dir, "test.vmx")

	content := `.encoding = "windows-1252"
displayName = "TestVM"
guestOS = "windows10-64"
memsize = "4096"
numvcpus = "2"
`
	if err := os.WriteFile(vmx, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	data, err := ParseVMXKeys(vmx)
	if err != nil {
		t.Fatal(err)
	}

	checks := map[string]string{
		".encoding":   "windows-1252",
		"displayname": "TestVM",
		"guestos":     "windows10-64",
		"memsize":     "4096",
		"numvcpus":    "2",
	}
	for key, want := range checks {
		got, ok := data[key]
		if !ok {
			t.Errorf("ParseVMXKeys missing key %q", key)
			continue
		}
		if got != want {
			t.Errorf("ParseVMXKeys[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestSetVMXKey_Update(t *testing.T) {
	dir := t.TempDir()
	vmx := filepath.Join(dir, "test.vmx")
	if err := os.WriteFile(vmx, []byte("memsize = \"4096\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetVMXKey(vmx, "memsize", "8192"); err != nil {
		t.Fatal(err)
	}

	data, err := ParseVMXKeys(vmx)
	if err != nil {
		t.Fatal(err)
	}
	if data["memsize"] != "8192" {
		t.Errorf("SetVMXKey update: got %q, want \"8192\"", data["memsize"])
	}
}

func TestSetVMXKey_Add(t *testing.T) {
	dir := t.TempDir()
	vmx := filepath.Join(dir, "test.vmx")
	if err := os.WriteFile(vmx, []byte("memsize = \"4096\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetVMXKey(vmx, "numvcpus", "4"); err != nil {
		t.Fatal(err)
	}

	data, err := ParseVMXKeys(vmx)
	if err != nil {
		t.Fatal(err)
	}
	if data["numvcpus"] != "4" {
		t.Errorf("SetVMXKey add: got %q, want \"4\"", data["numvcpus"])
	}
}

func TestRemoveVMXKey(t *testing.T) {
	dir := t.TempDir()
	vmx := filepath.Join(dir, "test.vmx")
	if err := os.WriteFile(vmx, []byte("memsize = \"4096\"\nnumvcpus = \"2\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := RemoveVMXKey(vmx, "numvcpus"); err != nil {
		t.Fatal(err)
	}

	data, err := ParseVMXKeys(vmx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := data["numvcpus"]; ok {
		t.Error("RemoveVMXKey: key should be removed")
	}
	if data["memsize"] != "4096" {
		t.Error("RemoveVMXKey: other keys should be preserved")
	}
}

// ---------------------------------------------------------------------------
// ValidateRAM
// ---------------------------------------------------------------------------

func TestValidateRAM(t *testing.T) {
	host := HostResources{TotalRAMGB: 16}
	for _, mb := range []int{4, 492, 1340, 4096, 16384} {
		if err := ValidateRAM(host, mb); err != nil {
			t.Errorf("ValidateRAM(%d MB) unexpected error: %s", mb, err)
		}
	}
	for _, mb := range []int{0, -4, 1341, 16388} {
		if err := ValidateRAM(host, mb); err == nil {
			t.Errorf("ValidateRAM(%d MB) should return error", mb)
		}
	}
	// Unknown host capacity skips only the capacity check.
	if err := ValidateRAM(HostResources{}, 65536); err != nil {
		t.Errorf("ValidateRAM with unknown host capacity: %s", err)
	}
}

// ---------------------------------------------------------------------------
// GBtoMB / MBtoKB
// ---------------------------------------------------------------------------

func TestGBtoMB(t *testing.T) {
	if got := GBtoMB(4); got != 4096 {
		t.Errorf("GBtoMB(4) = %d, want 4096", got)
	}
}

func TestMBtoKB(t *testing.T) {
	if got := MBtoKB(256); got != 262144 {
		t.Errorf("MBtoKB(256) = %d, want 262144", got)
	}
}

// ---------------------------------------------------------------------------
// NIC type mapping - SetNICType
// ---------------------------------------------------------------------------

func TestSetNICType_Bridged(t *testing.T) {
	dir := t.TempDir()
	vmx := filepath.Join(dir, "test.vmx")
	content := `ethernet0.present = "TRUE"
ethernet0.connectionType = "nat"
`
	if err := os.WriteFile(vmx, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetNICType(vmx, 0, "bridged", ""); err != nil {
		t.Fatal(err)
	}

	data, _ := ParseVMXKeys(vmx)
	// Bridged removes connectionType
	if _, ok := data["ethernet0.connectiontype"]; ok {
		t.Error("bridged should remove connectionType key")
	}
}

func TestSetNICType_NAT(t *testing.T) {
	dir := t.TempDir()
	vmx := filepath.Join(dir, "test.vmx")
	content := `ethernet0.present = "TRUE"
`
	if err := os.WriteFile(vmx, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetNICType(vmx, 0, "nat", ""); err != nil {
		t.Fatal(err)
	}

	data, _ := ParseVMXKeys(vmx)
	if data["ethernet0.connectiontype"] != "nat" {
		t.Errorf("got %q, want \"nat\"", data["ethernet0.connectiontype"])
	}
}

func TestSetNICType_Custom(t *testing.T) {
	dir := t.TempDir()
	vmx := filepath.Join(dir, "test.vmx")
	content := `ethernet0.present = "TRUE"
`
	if err := os.WriteFile(vmx, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetNICType(vmx, 0, "custom", "VMnet2"); err != nil {
		t.Fatal(err)
	}

	data, _ := ParseVMXKeys(vmx)
	if data["ethernet0.connectiontype"] != "custom" {
		t.Errorf("connectionType got %q, want \"custom\"", data["ethernet0.connectiontype"])
	}
	if data["ethernet0.vnet"] != "VMnet2" {
		t.Errorf("vnet got %q, want \"VMnet2\"", data["ethernet0.vnet"])
	}
}

func TestSetNICType_CustomNoVnet(t *testing.T) {
	dir := t.TempDir()
	vmx := filepath.Join(dir, "test.vmx")
	content := `ethernet0.present = "TRUE"
`
	if err := os.WriteFile(vmx, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetNICType(vmx, 0, "custom", ""); err == nil {
		t.Error("custom without --vnet should error")
	}
}

func TestSetNICType_NonexistentNIC(t *testing.T) {
	dir := t.TempDir()
	vmx := filepath.Join(dir, "test.vmx")
	if err := os.WriteFile(vmx, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetNICType(vmx, 0, "nat", ""); err == nil {
		t.Error("setting type on nonexistent NIC should error")
	}
}

func TestSetNICType_UnknownType(t *testing.T) {
	dir := t.TempDir()
	vmx := filepath.Join(dir, "test.vmx")
	content := `ethernet0.present = "TRUE"
`
	if err := os.WriteFile(vmx, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetNICType(vmx, 0, "invalid", ""); err == nil {
		t.Error("unknown NIC type should error")
	}
}
