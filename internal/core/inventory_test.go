package core

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vbauerster/mpb/v8"
)

// fakeHV is a minimal Hypervisor for inventory tests.
type fakeHV struct {
	vms []VM
	err error
}

func (f *fakeHV) GetPowerState() ([]VM, error) {
	out := make([]VM, len(f.vms))
	copy(out, f.vms)
	return out, f.err
}
func (f *fakeHV) EnsureVMwareRunning() error                     { return nil }
func (f *fakeHV) StartVM(string) error                           { return nil }
func (f *fakeHV) StopVM(string, ...string) error                 { return nil }
func (f *fakeHV) SuspendVM(string) error                         { return nil }
func (f *fakeHV) ResetVM(string) error                           { return nil }
func (f *fakeHV) CreateSnapshot(string, string) error            { return nil }
func (f *fakeHV) RevertToSnapshot(string, string) error          { return nil }
func (f *fakeHV) DeleteSnapshot(string, string) error            { return nil }
func (f *fakeHV) ListSnapshots(string) ([]string, error)         { return nil, nil }
func (f *fakeHV) FindOvftool() (string, error)                   { return "", nil }
func (f *fakeHV) ExportVM(string, string) error                  { return nil }
func (f *fakeHV) ExportVMWithBar(string, string, *mpb.Bar) error { return nil }
func (f *fakeHV) ImportVM(string, string) error                  { return nil }
func (f *fakeHV) WarmEncryptionCache([]string)                   {}
func (f *fakeHV) RunGuestCommand(_, _, _, _, _, _, _ string) (string, error) {
	return "", nil
}
func (f *fakeHV) RunGuestProgram(_, _, _, _, _, _ string, _ ...string) (string, error) {
	return "", nil
}
func (f *fakeHV) CopyFileFromGuest(_, _, _, _, _, _, _ string) error { return nil }
func (f *fakeHV) DeleteFileInGuest(_, _, _, _, _, _ string) error    { return nil }
func (f *fakeHV) ListGuestProcesses(_, _, _, _, _ string) error      { return nil }

// registerFake registers a fake backend under name for the duration of a test.
func registerFake(t *testing.T, name string, hv *fakeHV, createErr error) {
	t.Helper()
	RegisterBackend(name, func(Settings) (Hypervisor, error) {
		if createErr != nil {
			return nil, createErr
		}
		return hv, nil
	})
	t.Cleanup(func() { delete(backendFactories, name) })
}

func names(ts []Target) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name+"@"+t.Backend)
	}
	return out
}

func loadFakes(t *testing.T, preferred string) *Inventory {
	t.Helper()
	registerFake(t, "fa", &fakeHV{vms: []VM{
		{Name: "web-1", Path: "/a/web-1.vmx", Folder: "DEV", Running: true},
		{Name: "db-1", Path: "/a/db-1.vmx", Folder: "PROD"},
		{Name: "shared", Path: "/a/shared.vmx", Folder: "DEV"},
	}}, nil)
	registerFake(t, "fb", &fakeHV{vms: []VM{
		{Name: "web-2", Path: "uuid-2", Folder: "dev"},
		{Name: "loose", Path: "uuid-3"}, // no folder -> Ungrouped
		{Name: "SHARED", Path: "uuid-4", Folder: "TEST"},
	}}, nil)
	inv, err := LoadInventory(Settings{}, []string{"fa", "fb"}, preferred)
	if err != nil {
		t.Fatalf("LoadInventory: %v", err)
	}
	return inv
}

func TestInventory_FolderSpansBackends(t *testing.T) {
	inv := loadFakes(t, "")
	got := names(inv.InFolders([]string{"DEV"}))
	want := []string{"web-1@fa", "shared@fa", "web-2@fb"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("InFolders(DEV) = %v, want %v", got, want)
	}
}

func TestInventory_MultipleFoldersAndUngrouped(t *testing.T) {
	inv := loadFakes(t, "")
	got := names(inv.InFolders(ParseFolders(" prod , Ungrouped ,")))
	want := []string{"db-1@fa", "loose@fb"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("InFolders = %v, want %v", got, want)
	}
	if len(inv.InFolders([]string{"NOPE"})) != 0 {
		t.Error("unknown folder should match nothing")
	}
}

func TestInventory_TargetsCarryBackend(t *testing.T) {
	inv := loadFakes(t, "")
	for _, tg := range inv.All() {
		if tg.HV == nil || tg.Backend == "" {
			t.Fatalf("target %s missing backend binding", tg.Name)
		}
	}
}

func TestInventory_ByName(t *testing.T) {
	inv := loadFakes(t, "")
	res := inv.ByName([]string{"WEB-2", "db-1", "missing", "web-2"})
	if got, want := names(res.Targets), []string{"web-2@fb", "db-1@fa"}; !reflect.DeepEqual(got, want) {
		t.Errorf("targets = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(res.Missing, []string{"missing"}) {
		t.Errorf("missing = %v", res.Missing)
	}
	if len(res.Ambiguous) != 0 {
		t.Errorf("ambiguous = %v", res.Ambiguous)
	}
}

func TestInventory_ByNameAmbiguous(t *testing.T) {
	inv := loadFakes(t, "")
	res := inv.ByName([]string{"shared"})
	if len(res.Targets) != 0 {
		t.Errorf("ambiguous name resolved to %v", names(res.Targets))
	}
	if !reflect.DeepEqual(res.Ambiguous["shared"], []string{"fa", "fb"}) {
		t.Errorf("ambiguous = %v", res.Ambiguous)
	}
}

func TestInventory_ByNamePreferredWins(t *testing.T) {
	inv := loadFakes(t, "fb")
	res := inv.ByName([]string{"shared"})
	if got := names(res.Targets); !reflect.DeepEqual(got, []string{"SHARED@fb"}) {
		t.Errorf("targets = %v", got)
	}
}

func TestInventory_PartialFailure(t *testing.T) {
	registerFake(t, "ok", &fakeHV{vms: []VM{{Name: "a", Folder: "DEV"}}}, nil)
	registerFake(t, "broken", &fakeHV{err: errors.New("vmrun exploded")}, nil)
	registerFake(t, "absent", nil, errors.New("not installed"))

	inv, err := LoadInventory(Settings{}, []string{"broken", "ok", "absent"}, "")
	if err != nil {
		t.Fatalf("LoadInventory: %v", err)
	}
	if got := inv.Backends(); !reflect.DeepEqual(got, []string{"ok"}) {
		t.Errorf("Backends = %v", got)
	}
	var failed []string
	for _, f := range inv.Failures() {
		failed = append(failed, f.Backend)
	}
	if !reflect.DeepEqual(failed, []string{"broken", "absent"}) {
		t.Errorf("Failures = %v", inv.Failures())
	}
	if w := fmt.Sprint(inv.Failures()); !strings.Contains(w, "vmrun exploded") || !strings.Contains(w, "not installed") {
		t.Errorf("failures = %q", w)
	}
	if _, ok := inv.Backend("broken"); ok {
		t.Error("failed backend should not be returned")
	}
}

// Every backend failing still yields an inventory, so each failure can be
// reported on its own; only naming no backend at all is an error.
func TestInventory_AllFail(t *testing.T) {
	registerFake(t, "broken", &fakeHV{err: errors.New("boom")}, nil)
	inv, err := LoadInventory(Settings{}, []string{"broken"}, "")
	if err != nil {
		t.Fatalf("LoadInventory: %v", err)
	}
	if len(inv.Backends()) != 0 {
		t.Errorf("Backends = %v", inv.Backends())
	}
	if f := inv.Failures(); len(f) != 1 || f[0].Error() != "broken: boom" {
		t.Errorf("Failures = %v", f)
	}
	if _, err := LoadInventory(Settings{}, nil, ""); err == nil {
		t.Fatal("expected error with no backends")
	}
}

// A configured executable that doesn't exist is reported by its .env key,
// before the backend is even created.
func TestInventory_ConfiguredPathMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	inv, err := LoadInventory(Settings{VmrunPath: missing, VBoxManagePath: missing}, []string{"workstation", "vbox"}, "")
	if err != nil {
		t.Fatalf("LoadInventory: %v", err)
	}
	f := inv.Failures()
	if len(f) != 2 || !strings.HasPrefix(f[0].Error(), "VMware Workstation: VMRUN_PATH: ") ||
		!strings.HasPrefix(f[1].Error(), "VirtualBox: VBOX_MANAGE_PATH: ") || !errors.Is(f[0].Err, fs.ErrNotExist) {
		t.Errorf("Failures = %v", f)
	}

	inv, _ = LoadInventory(Settings{}, []string{"workstation"}, "")
	if f := inv.Failures(); len(f) != 1 || !strings.Contains(f[0].Error(), "VMRUN_PATH is not set") {
		t.Errorf("Failures = %v", f)
	}
}

func TestNodeBackendsKeepsMissingPaths(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no VBoxManage on PATH
	missing := filepath.Join(t.TempDir(), "missing")
	s := Settings{Hypervisor: "workstation", VmrunPath: missing, VBoxManagePath: missing}
	if got := NodeBackends(s, ""); !reflect.DeepEqual(got, []string{"workstation", "vbox"}) {
		t.Errorf("NodeBackends = %v", got)
	}
	if got := NodeBackends(Settings{}, ""); len(got) != 0 {
		t.Errorf("nothing configured: NodeBackends = %v", got)
	}
}

func TestAutoDetectedBackend(t *testing.T) {
	for _, c := range []struct {
		s      Settings
		hv     string
		name   string
		expect bool
	}{
		{Settings{}, "", "vbox", true},
		{Settings{VBoxManagePath: "/x/VBoxManage"}, "", "vbox", false},
		{Settings{Hypervisor: "vbox"}, "", "vbox", false},
		{Settings{}, "vbox", "vbox", false},
		{Settings{}, "", "workstation", false},
	} {
		if got := AutoDetectedBackend(c.s, c.hv, c.name); got != c.expect {
			t.Errorf("AutoDetectedBackend(%+v, %q, %q) = %v", c.s, c.hv, c.name, got)
		}
	}
}

func TestNodeBackends(t *testing.T) {
	if got := NodeBackends(Settings{Hypervisor: "vbox"}, "hyperv"); !reflect.DeepEqual(got, []string{"hyperv"}) {
		t.Errorf("--hv should narrow to one backend, got %v", got)
	}
	got := NodeBackends(Settings{Hypervisor: "hyperv", HyperVEnabled: true}, "")
	if len(got) == 0 || got[0] != "hyperv" {
		t.Errorf("HYPERVISOR should come first, got %v", got)
	}
	count := 0
	for _, n := range got {
		if n == "hyperv" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("HYPERVISOR listed %d times in %v", count, got)
	}
}

func TestSortTargets(t *testing.T) {
	ts := []Target{
		{VM: VM{Name: "b", Backend: "x"}},
		{VM: VM{Name: "A", Backend: "y"}},
		{VM: VM{Name: "a", Backend: "x"}},
	}
	SortTargets(ts)
	if got := names(ts); !reflect.DeepEqual(got, []string{"a@x", "A@y", "b@x"}) {
		t.Errorf("SortTargets = %v", got)
	}
}
