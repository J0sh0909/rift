package vbox

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/J0sh0909/rift/internal/core"
)

func TestParseListVMs(t *testing.T) {
	out := `"web 1" {11111111-2222-3333-4444-555555555555}
"<inaccessible>" {99999999-2222-3333-4444-555555555555}
"db" {aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee}
`
	got := parseListVMs(out)
	want := []VBoxVM{
		{Name: "web 1", UUID: "11111111-2222-3333-4444-555555555555"},
		{Name: "db", UUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseListVMs = %+v", got)
	}
}

func TestFolderFromGroups(t *testing.T) {
	cases := map[string]string{
		"/":          core.UngroupedFolder,
		"":           core.UngroupedFolder,
		"/DEV":       "DEV",
		"/LAB/DEV":   "DEV",
		"/DEV,/PROD": "DEV",
	}
	for in, want := range cases {
		if got := folderFromGroups(in); got != want {
			t.Errorf("folderFromGroups(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseGuestIPs(t *testing.T) {
	v6 := `Name: /VirtualBox/GuestInfo/Net/1/V4/IP, value: 192.168.56.101, timestamp: 1700000000000000000, flags:
Name: /VirtualBox/GuestInfo/Net/0/V4/IP, value: 10.0.2.15, timestamp: 1700000000000000000, flags:
Name: /VirtualBox/GuestInfo/Net/0/V4/Netmask, value: 255.255.255.0, timestamp: 1, flags:
Name: /VirtualBox/GuestInfo/OS/Product, value: Linux, timestamp: 1, flags:`
	if got := parseGuestIPs(v6); !reflect.DeepEqual(got, []string{"10.0.2.15", "192.168.56.101"}) {
		t.Errorf("VirtualBox 6 format: %v", got)
	}
	v7 := `/VirtualBox/GuestInfo/Net/0/V4/IP = '10.0.2.15' @ 2024-05-01T10:00:00.000000000Z
/VirtualBox/GuestInfo/Net/0/V4/Broadcast = '10.0.2.255' @ 2024-05-01T10:00:00.000000000Z`
	if got := parseGuestIPs(v7); !reflect.DeepEqual(got, []string{"10.0.2.15"}) {
		t.Errorf("VirtualBox 7 format: %v", got)
	}
}

func TestDiskAttachments(t *testing.T) {
	data := map[string]string{
		`"SATA Controller-1-0"`:           `C:\VMs\x\data.vdi`,
		`"SATA Controller-0-0"`:           `C:\VMs\x\boot.vmdk`,
		`"SATA Controller-ImageUUID-0-0"`: "1234",
		`"IDE-1-0"`:                       `C:\isos\ubuntu.iso`,
		`"IDE-0-0"`:                       "emptydrive",
		"memory":                          "2048",
	}
	got := diskAttachments(data)
	want := []string{`C:\VMs\x\boot.vmdk`, `C:\VMs\x\data.vdi`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("diskAttachments = %v, want %v", got, want)
	}
}

func TestVBoxErrorText(t *testing.T) {
	stderr := "some guest stderr\nVBoxManage: error: The guest execution service is not ready (yet)\nVBoxManage: error: Details: code VBOX_E_IPRT_ERROR\n"
	got := vboxErrorText(stderr)
	if !strings.Contains(got, "not ready") || !strings.Contains(got, "Details") {
		t.Errorf("vboxErrorText = %q", got)
	}
	if vboxErrorText("guest wrote this\n") != "" {
		t.Error("guest stderr must not be treated as a VBoxManage error")
	}
}

// fakeVBoxManage writes a shell script standing in for VBoxManage. It logs
// its arguments (one per line, then "=====") and replies to the subcommands the
// backend uses.
func fakeVBoxManage(t *testing.T) (bin, argLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake VBoxManage is a shell script")
	}
	dir := t.TempDir()
	argLog = filepath.Join(dir, "args.log")
	bin = filepath.Join(dir, "VBoxManage")
	script := `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a" >> '` + argLog + `'; done
echo ===== >> '` + argLog + `'
case "$1 $2" in
"list vms")
  echo '"web-1" {11111111-1111-1111-1111-111111111111}'
  echo '"db-1" {22222222-2222-2222-2222-222222222222}'
  ;;
"showvminfo 11111111-1111-1111-1111-111111111111")
  echo 'name="web-1"'; echo 'groups="/LAB/DEV"'; echo 'ostype="Ubuntu_64"'; echo 'VMState="running"'
  ;;
"showvminfo 22222222-2222-2222-2222-222222222222")
  echo 'name="db-1"'; echo 'groups="/"'; echo 'ostype="Windows2019_64"'; echo 'VMState="poweroff"'
  ;;
"guestcontrol 11111111-1111-1111-1111-111111111111")
  # record the password file contents to prove it was readable
  while [ $# -gt 0 ]; do
    case "$1" in --passwordfile) cat "$2" >> '` + argLog + `.pw' ;; --) shift; break ;; esac
    shift
  done
  case "$*" in
  *sleep*) sleep 5 ;;
  *fail*) echo "boom" >&2; exit 4 ;;
  *noauth*) echo "VBoxManage: error: The specified user was not able to logon on guest" >&2; exit 1 ;;
  *) echo "ran: $*" ;;
  esac
  ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argLog
}

func lastInvocation(t *testing.T, argLog string) []string {
	t.Helper()
	data, err := os.ReadFile(argLog)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSuffix(string(data), "=====\n"), "=====\n")
	return strings.Split(strings.TrimSuffix(calls[len(calls)-1], "\n"), "\n")
}

func TestGetPowerStateWithFake(t *testing.T) {
	bin, _ := fakeVBoxManage(t)
	v, err := NewVBoxBackend(bin)
	if err != nil {
		t.Fatal(err)
	}
	vms, err := v.GetPowerState()
	if err != nil {
		t.Fatal(err)
	}
	want := []core.VM{
		{Name: "web-1", Path: "11111111-1111-1111-1111-111111111111", Folder: "DEV", Running: true, OS: "Ubuntu_64"},
		{Name: "db-1", Path: "22222222-2222-2222-2222-222222222222", Folder: core.UngroupedFolder, OS: "Windows2019_64"},
	}
	if !reflect.DeepEqual(vms, want) {
		t.Errorf("GetPowerState = %+v", vms)
	}
}

func TestExecInGuestWithFake(t *testing.T) {
	bin, argLog := fakeVBoxManage(t)
	v, _ := NewVBoxBackend(bin)
	vm := core.VM{Path: "11111111-1111-1111-1111-111111111111"}

	res, err := v.ExecInGuest(vm, core.ExecRequest{User: "runner", Pass: "pw'1", Interpreter: "/bin/bash", Script: "ip a"})
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Output, "ran: /bin/bash -c ip a") {
		t.Fatalf("unix exec: %+v %v", res, err)
	}
	args := lastInvocation(t, argLog)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "pw'1") || !strings.Contains(joined, "--passwordfile") {
		t.Errorf("password must go through --passwordfile, args: %v", args)
	}
	if !strings.Contains(joined, "--exe /bin/bash --wait-stdout --wait-stderr -- /bin/bash -c ip a") {
		t.Errorf("unexpected args: %v", args)
	}
	if pw, _ := os.ReadFile(argLog + ".pw"); string(pw) != "pw'1" {
		t.Errorf("password file contents = %q", pw)
	}

	res, err = v.ExecInGuest(vm, core.ExecRequest{User: "u", Pass: "p", Windows: true, Script: "ipconfig /all"})
	if err != nil || !strings.Contains(res.Output, "ran: cmd.exe /c ipconfig /all") {
		t.Fatalf("windows exec: %+v %v", res, err)
	}
	if joined := strings.Join(lastInvocation(t, argLog), " "); !strings.Contains(joined, `--unquoted-args`) {
		t.Errorf("windows exec should pass --unquoted-args: %s", joined)
	}

	res, err = v.ExecInGuest(vm, core.ExecRequest{User: "u", Pass: "p", Script: "fail"})
	if err != nil || res.ExitCode != 4 || !strings.Contains(res.Output, "boom") {
		t.Errorf("guest failure should surface exit code and output: %+v %v", res, err)
	}

	if _, err = v.ExecInGuest(vm, core.ExecRequest{User: "u", Pass: "p", Script: "noauth"}); err == nil || !strings.Contains(err.Error(), "logon") {
		t.Errorf("VBoxManage errors should be transport errors, got %v", err)
	}

	start := time.Now()
	if _, err = v.ExecInGuest(vm, core.ExecRequest{User: "u", Pass: "p", Script: "sleep", Timeout: 200 * time.Millisecond}); err == nil {
		t.Error("expected timeout")
	}
	if time.Since(start) > 3*time.Second {
		t.Error("timeout not enforced")
	}
}

func TestRunGuestProgramWithFake(t *testing.T) {
	bin, argLog := fakeVBoxManage(t)
	v, _ := NewVBoxBackend(bin)
	if _, err := v.RunGuestProgram("11111111-1111-1111-1111-111111111111", "u", "p", "", "", core.WindowsShell, "/c net user runner"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lastInvocation(t, argLog), " ")
	if !strings.Contains(joined, `--exe C:\Windows\System32\cmd.exe --wait-stdout --wait-stderr --unquoted-args -- cmd.exe /c net user runner`) {
		t.Errorf("unexpected args: %s", joined)
	}
}
