package hyperv

import (
	"encoding/base64"
	"encoding/binary"
	"reflect"
	"testing"
	"unicode/utf16"

	"github.com/J0sh0909/rift/internal/core"
)

func TestParseVMList(t *testing.T) {
	out := `[{"Name":"web-1","Id":"0f1e2d3c-1111-2222-3333-444455556666","State":2,"Group":"DEV","Notes":""},` +
		`{"Name":"db-1","Id":"aaaaaaaa-1111-2222-3333-444455556666","State":3,"Group":"DEV","Notes":"owner: josh\r\nrift-folder: PROD\r\nrift-os: ubuntu-64"},` +
		`{"Name":"loose","Id":"","State":3,"Group":"","Notes":null},` +
		`null]`
	got, err := parseVMList(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []core.VM{
		{Name: "web-1", Path: "0f1e2d3c-1111-2222-3333-444455556666", Folder: "DEV", Running: true},
		{Name: "db-1", Path: "aaaaaaaa-1111-2222-3333-444455556666", Folder: "PROD", OS: "ubuntu-64"},
		{Name: "loose", Path: "loose", Folder: core.UngroupedFolder},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseVMList =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseVMListShapes(t *testing.T) {
	// Windows PowerShell 5.1 can emit a bare object for a single VM.
	got, err := parseVMList(`{"Name":"solo","Id":"x","State":2,"Group":"","Notes":""}`)
	if err != nil || len(got) != 1 || got[0].Name != "solo" || !got[0].Running {
		t.Errorf("single object: %+v %v", got, err)
	}
	for _, empty := range []string{"", "  ", "[]", "null"} {
		if got, err := parseVMList(empty); err != nil || len(got) != 0 {
			t.Errorf("parseVMList(%q) = %+v, %v", empty, got, err)
		}
	}
	if _, err := parseVMList("WARNING: something"); err == nil {
		t.Error("garbage should be an error")
	}
}

func TestParseIPList(t *testing.T) {
	out := `[{"Id":"id-1","Name":"web-1","IP":"192.168.1.20"},{"Id":"id-1","Name":"web-1","IP":"fe80::1"},` +
		`{"Id":"id-1","Name":"web-1","IP":"169.254.1.1"},{"Id":"id-2","Name":"db-1","IP":"10.0.0.5"}]`
	got, err := parseIPList(out)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["id-1"], []string{"192.168.1.20"}) || !reflect.DeepEqual(got["db-1"], []string{"10.0.0.5"}) {
		t.Errorf("parseIPList = %v", got)
	}
}

func TestEncodeCommand(t *testing.T) {
	script := "Write-Output 'héllo' # ünïcode"
	raw, err := base64.StdEncoding.DecodeString(encodeCommand(script))
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	if got := string(utf16.Decode(u)); got != script {
		t.Errorf("round trip = %q", got)
	}
}

func TestPSErrorText(t *testing.T) {
	stderr := "Start-VM : The operation cannot be performed while the virtual machine is in its current state.\r\n" +
		"At line:12 char:15\r\n+ Get-RiftVM | Start-VM\r\n+              ~~~~~~~~\r\n" +
		"    + CategoryInfo          : InvalidOperation\r\n"
	got := psErrorText(stderr)
	if got != "Start-VM : The operation cannot be performed while the virtual machine is in its current state." {
		t.Errorf("psErrorText = %q", got)
	}
}
