package vmware

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRedactArgs(t *testing.T) {
	in := []string{"-vp", "encpw", "-T", "ws", "-gu", "runner", "-gp", "guestpw", "listProcessesInGuest", "x.vmx"}
	got := redactArgs(in)
	want := []string{"-vp", "***", "-T", "ws", "-gu", "runner", "-gp", "***", "listProcessesInGuest", "x.vmx"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("redactArgs = %v", got)
	}
	if in[1] != "encpw" {
		t.Error("redactArgs must not modify its input")
	}
}

func TestParseInventory(t *testing.T) {
	inv := `.encoding = "UTF-8"
vmlist10.config = "C:\VMs\db\db.vmx"
vmlist10.DisplayName = "db"
vmlist10.ParentID = "folder1"
vmlist2.config = "C:\VMs\web\web.vmx"
vmlist2.DisplayName = "web"
vmlist2.ParentID = "folder1"
vmlist1.config = "folder1"
vmlist1.Type = "2"
vmlist1.DisplayName = "DEV"
vmlist1.ItemID = "folder1"
vmlist3.config = "C:\VMs\loose\loose.vmx"
vmlist3.DisplayName = "loose"
`
	path := filepath.Join(t.TempDir(), "inventory.vmls")
	if err := os.WriteFile(path, []byte(inv), 0o644); err != nil {
		t.Fatal(err)
	}
	// Parse several times: order must be stable (vmlist index order).
	for i := 0; i < 5; i++ {
		vms, err := wsParseInventory(path)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, vm := range vms {
			got = append(got, vm.Name+"/"+vm.Folder)
		}
		want := []string{"web/DEV", "loose/Ungrouped", "db/DEV"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("wsParseInventory order = %v, want %v", got, want)
		}
	}
}
