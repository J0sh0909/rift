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
// --hv), once per process. A backend that fails is reported (see
// reportInventory) while the others are still acted on; rift exits only when
// none can be listed.
func nodeInventory() *core.Inventory {
	nodeInvOnce.Do(func() {
		s := loadSettings()
		preferred := hvFlag
		if preferred == "" {
			preferred = s.Hypervisor
		}
		inv, err := core.LoadInventory(s, core.NodeBackends(s, hvFlag), preferred)
		if err != nil {
			fail(core.ErrMissingSetting, "%s", err)
			exit(1)
		}
		reportInventory(s, inv)
		if len(inv.Backends()) == 0 {
			fail(core.ErrHypervisorInit, "no hypervisor backend could list VMs on node %s", s.Node())
			exit(1)
		}
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

// reportInventory reports each backend that could not be listed. A backend
// .env or --hv asked for fails the run: every result would be incomplete
// (e.g. "start -f DEV" would silently skip its DEV VMs), though the
// reachable backends are still acted on. One merely found on PATH is only a
// warning. Running as a service account, a backend that lists no VMs is
// reported too, since that account can't see the user's VMs.
func reportInventory(s core.Settings, inv *core.Inventory) {
	counted := false
	for _, f := range inv.Failures() {
		if core.AutoDetectedBackend(s, hvFlag, f.Backend) {
			warn("skipping %s", f)
			continue
		}
		fail(core.ErrHypervisorInit, "%s", f)
		counted = true
	}
	if counted {
		noteServiceAccount()
	}

	sid := currentSID()
	account, isService := serviceAccounts[sid]
	if !isService {
		return
	}
	if listedNoVMs(inv, "vbox") {
		msg := fmt.Sprintf("VirtualBox lists no VMs for %s: VirtualBox registers VMs per Windows user", account)
		if core.AutoDetectedBackend(s, hvFlag, "vbox") {
			warn("%s", msg)
		} else {
			fail(core.ErrHypervisorInit, "%s", msg)
		}
		if sid == sidSystem {
			printServiceNote("rift is running as " + account + ", which doesn't see the VirtualBox VMs your Windows user registered. " + runAsUserHint)
		} else {
			noteServiceAccount()
		}
	}
	if sid != sidSystem && listedNoVMs(inv, "hyperv") {
		fail(core.ErrHypervisorInit, "Hyper-V lists no VMs for %s: it can't manage Hyper-V without Hyper-V Administrators membership", account)
		noteServiceAccount()
	}
}

// listedNoVMs reports whether backend listed successfully but has no VMs.
func listedNoVMs(inv *core.Inventory, backend string) bool {
	if _, ok := inv.Backend(backend); !ok {
		return false
	}
	for _, t := range inv.All() {
		if t.Backend == backend {
			return false
		}
	}
	return true
}

// warn reports a problem that doesn't fail the run.
func warn(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	fmt.Fprintln(os.Stderr, "warning: "+msg)
	pipelineNote("**warning:** " + msg)
}

// Well-known SIDs of the built-in Windows service accounts. The GitHub
// runner service runs as NETWORK SERVICE unless configured otherwise. Names
// come from here: user.Current reports the machine account (WORKGROUP\PC$)
// for NETWORK SERVICE, and SIDs don't change with the Windows language.
const (
	sidSystem         = "S-1-5-18"
	sidLocalService   = "S-1-5-19"
	sidNetworkService = "S-1-5-20"
)

var serviceAccounts = map[string]string{
	sidSystem:         `NT AUTHORITY\SYSTEM`,
	sidLocalService:   `NT AUTHORITY\LOCAL SERVICE`,
	sidNetworkService: `NT AUTHORITY\NETWORK SERVICE`,
}

// currentSID returns the SID of the account rift runs as ("" if unknown).
// A variable so tests can pretend to be a service account.
var currentSID = func() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Uid
}

const runAsUserHint = "Run the runner service as the Windows user that owns the VMs: " +
	"services.msc > actions.runner.* > Properties > Log On > This account, then restart the service."

var serviceNoteOnce sync.Once

// noteServiceAccount explains, once per run, failures caused by running as
// LOCAL SERVICE or NETWORK SERVICE. Such an account can't read a user's
// profile, can't see the VirtualBox VMs a user registered and can't manage
// Hyper-V. No-op for any other account; SYSTEM can read profiles and manage
// Hyper-V (reportInventory covers its VirtualBox blind spot).
func noteServiceAccount() {
	sid := currentSID()
	if sid != sidLocalService && sid != sidNetworkService {
		return
	}
	who := serviceAccounts[sid]
	if sid == sidNetworkService {
		who += " (the runner service's default account)"
	}
	printServiceNote("rift is running as " + who + ". It can't read your user profile (VMware's inventory, ~\\.aws credentials " +
		"and anything else under C:\\Users\\<you>), can't see the VirtualBox VMs your Windows user registered, " +
		"and can't manage Hyper-V. " + runAsUserHint)
}

// printServiceNote prints the first service account note of the run.
func printServiceNote(msg string) {
	serviceNoteOnce.Do(func() {
		fmt.Fprintln(os.Stderr, "note: "+msg)
		pipelineNote("**note:** " + msg)
	})
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
