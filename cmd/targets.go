package cmd

import (
	"fmt"
	"os"
	"os/user"
	"strings"
	"sync"

	"github.com/J0sh0909/rift/internal/core"
)

var (
	nodeInv     *core.Inventory
	nodeInvOnce sync.Once
)

// nodeInventory lists VMs on every hypervisor backend of this node (or only
// --hv), once per process. Backends that fail are reported as warnings; rift
// exits only when none can be listed.
func nodeInventory() *core.Inventory {
	nodeInvOnce.Do(func() {
		s := loadSettings()
		preferred := hvFlag
		if preferred == "" {
			preferred = s.Hypervisor
		}
		inv, err := core.LoadInventory(s, core.NodeBackends(s, hvFlag), preferred)
		if err != nil {
			fail(core.ErrHypervisorInit, "%s", err)
			noteServiceAccount()
			exit(1)
		}
		// A backend that can't be listed makes every result incomplete
		// (e.g. "start -f DEV" would silently skip its DEV VMs), so it fails
		// the run while the reachable backends are still acted on.
		for _, w := range inv.Warnings() {
			fail(core.ErrHypervisorInit, "%s", w)
		}
		noteInventoryProblems(inv)
		if pipeline.enabled {
			pipelineBackends(inv)
		}
		nodeInv = inv
	})
	return nodeInv
}

// targetMode says what an empty selection (no --folder, no names) means.
type targetMode int

const (
	needTargets targetMode = iota // it's an error
	allIfNone                     // every VM on the node (info, ip)
)

// resolveTargets turns --folder or VM names into targets across every
// hypervisor on this node. Folders and names that this node doesn't have are
// errors when run by hand, but only notices in pipeline mode, where the same
// command fans out to every node and most nodes won't host every VM.
func resolveTargets(names []string, mode targetMode) []core.Target {
	inv := nodeInventory()
	node := settings.Node()

	if folderFlag != "" {
		folders := core.ParseFolders(folderFlag)
		targets := inv.InFolders(folders)
		if len(targets) == 0 {
			notFound("no VMs in folder %s on node %s (%s)", quoteList(folders), node, backendList(inv))
		}
		return targets
	}

	if len(names) == 0 {
		if mode == allIfNone {
			return inv.All()
		}
		fail(core.ErrNotFound, "specify one or more VM names or --folder")
		return nil
	}

	res := inv.ByName(names)
	for _, name := range res.Missing {
		notFound("VM '%s' not found on node %s (%s)", name, node, backendList(inv))
	}
	for _, name := range sortedKeys(res.Ambiguous) {
		fail(core.ErrAmbiguous, "VM '%s' exists on several hypervisors (%s) - use --hv to pick one", name, strings.Join(res.Ambiguous[name], ", "))
	}
	return res.Targets
}

// notFound reports a missing VM or folder: a failure normally, a notice in
// pipeline mode.
func notFound(format string, a ...any) {
	if pipeline.enabled {
		msg := fmt.Sprintf(format, a...)
		fmt.Println("notice: " + msg)
		pipelineNote("_" + msg + "_")
		return
	}
	fail(core.ErrNotFound, format, a...)
}

func backendList(inv *core.Inventory) string {
	var labels []string
	for _, b := range inv.Backends() {
		labels = append(labels, core.BackendLabel(b))
	}
	return strings.Join(labels, ", ")
}

func quoteList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = "'" + s + "'"
	}
	return strings.Join(q, ", ")
}

// Well-known SIDs of the low-privilege Windows service accounts. The GitHub
// runner service runs as NETWORK SERVICE unless configured otherwise.
var serviceAccountSIDs = map[string]bool{
	"S-1-5-19": true, // LOCAL SERVICE
	"S-1-5-20": true, // NETWORK SERVICE
}

var serviceNoteOnce sync.Once

// noteServiceAccount explains, once per run, failures caused by running as a
// low-privilege service account. Such an account can't read a user's profile
// folders (paths under C:\Users\<name>), can't see the VirtualBox VMs a user
// registered, and can't manage Hyper-V without Hyper-V Administrators
// membership. No-op for any other account.
func noteServiceAccount() {
	u, err := user.Current()
	if err != nil || !serviceAccountSIDs[u.Uid] {
		return
	}
	serviceNoteOnce.Do(func() {
		msg := fmt.Sprintf("rift is running as %s, the runner service's account. It cannot read folders in your user profile (C:\\Users\\...), "+
			"cannot see VirtualBox VMs registered by your Windows user, and needs Hyper-V Administrators membership for Hyper-V. "+
			"Run the runner service as the Windows user that owns the VMs: services.msc > actions.runner.* > Log On > This account.", u.Username)
		fmt.Fprintln(os.Stderr, "note: "+msg)
		pipelineNote("**note:** " + msg)
	})
}

// noteInventoryProblems points at the service account when backends failed
// or VirtualBox listed nothing (its VMs are registered per Windows user).
func noteInventoryProblems(inv *core.Inventory) {
	if len(inv.Warnings()) > 0 {
		noteServiceAccount()
		return
	}
	if _, ok := inv.Backend("vbox"); ok {
		for _, t := range inv.All() {
			if t.Backend == "vbox" {
				return
			}
		}
		noteServiceAccount()
	}
}

// guestOSOf returns the guest OS identifier, asking the backend when the
// listing didn't include it (VMware reads the VMX, Hyper-V queries KVP).
func guestOSOf(t core.Target) string {
	if t.OS != "" {
		return t.OS
	}
	if r, ok := t.HV.(core.GuestOSReporter); ok {
		return r.GuestOS(t.VM)
	}
	return ""
}

// guestFamilyOf classifies the guest as Windows or Unix from the reported OS,
// falling back to the "-w-" / "-u-" naming convention.
func guestFamilyOf(t core.Target, osID string) string {
	if f := core.GuestFamily(osID); f != "" {
		return f
	}
	return core.FamilyFromName(t.Name)
}

// osHint tells the user how to declare a guest OS rift couldn't detect.
func osHint(t core.Target) string {
	switch t.Backend {
	case "workstation":
		return fmt.Sprintf(`use "rift config os %s --set <guestOS>"`, t.Name)
	case "hyperv":
		return `add a "rift-os: windows" or "rift-os: linux" line to the VM notes`
	}
	return "start the VM with Guest Additions installed"
}

// requireOff skips a running VM for operations that need it powered off.
// key is the core.RequiresPowerOff entry; action describes it for the user.
func requireOff(t core.Target, key, action string) (result, bool) {
	if t.Running && core.RequiresPowerOff[key] {
		return skipped(t, core.ErrPower, "must be powered off to %s", action), false
	}
	return result{}, true
}

// vmxOnly skips VMs that are not on VMware Workstation, for commands that
// edit .vmx files directly.
func vmxOnly(t core.Target) (result, bool) {
	if t.Backend != "workstation" {
		return skipped(t, core.ErrUnsupported, "not supported on %s (VMX settings are VMware Workstation only)", core.BackendLabel(t.Backend)), false
	}
	return result{}, true
}
