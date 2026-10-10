package core

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Target is a VM bound to the backend that manages it.
type Target struct {
	VM
	HV Hypervisor
}

type inventoryBackend struct {
	name string
	hv   Hypervisor
	vms  []VM
	err  error
}

// Inventory is the set of VMs across every hypervisor backend on this node.
// It lets folder and name targeting span VMware, VirtualBox and Hyper-V at
// once, so "rift start -f DEV" reaches every DEV VM the node manages.
type Inventory struct {
	backends  []*inventoryBackend
	preferred string
}

// LoadInventory opens the named backends and lists their VMs in parallel.
// A backend that fails to open or list is recorded as a warning; an error is
// returned only when none of them could be listed. preferred names the
// backend that wins when a VM name exists on several backends (may be empty).
func LoadInventory(s Settings, names []string, preferred string) (*Inventory, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("no hypervisor configured - check .env")
	}
	inv := &Inventory{preferred: preferred}
	var wg sync.WaitGroup
	for _, name := range names {
		b := &inventoryBackend{name: name}
		inv.backends = append(inv.backends, b)
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.hv, b.err = CreateBackend(b.name, s)
			if b.err != nil {
				return
			}
			vms, err := b.hv.GetPowerState()
			if err != nil {
				b.err = err
				return
			}
			for i := range vms {
				vms[i].Backend = b.name
				if vms[i].Folder == "" {
					vms[i].Folder = UngroupedFolder
				}
			}
			b.vms = vms
		}()
	}
	wg.Wait()

	var failures []string
	for _, b := range inv.backends {
		if b.err == nil {
			return inv, nil
		}
		failures = append(failures, fmt.Sprintf("%s: %s", b.name, b.err))
	}
	return nil, fmt.Errorf("no hypervisor backend could list VMs (%s)", strings.Join(failures, "; "))
}

// Backends returns the names of the backends that listed successfully.
func (inv *Inventory) Backends() []string {
	var names []string
	for _, b := range inv.backends {
		if b.err == nil {
			names = append(names, b.name)
		}
	}
	return names
}

// Warnings returns one message per backend that could not be listed.
func (inv *Inventory) Warnings() []string {
	var out []string
	for _, b := range inv.backends {
		if b.err != nil {
			out = append(out, fmt.Sprintf("skipping %s: %s", BackendLabel(b.name), b.err))
		}
	}
	return out
}

// Backend returns the hypervisor for name if it listed successfully.
func (inv *Inventory) Backend(name string) (Hypervisor, bool) {
	for _, b := range inv.backends {
		if b.name == name && b.err == nil {
			return b.hv, true
		}
	}
	return nil, false
}

// All returns every VM, grouped by backend in inventory order.
func (inv *Inventory) All() []Target {
	var out []Target
	for _, b := range inv.backends {
		for _, vm := range b.vms {
			out = append(out, Target{VM: vm, HV: b.hv})
		}
	}
	return out
}

// InFolders returns the VMs whose folder matches any of folders.
func (inv *Inventory) InFolders(folders []string) []Target {
	var out []Target
	for _, t := range inv.All() {
		if MatchFolder(t.Folder, folders) {
			out = append(out, t)
		}
	}
	return out
}

// Resolution is the outcome of resolving VM names against an Inventory.
type Resolution struct {
	Targets   []Target
	Missing   []string
	Ambiguous map[string][]string // VM name -> backends that have it
}

// ByName resolves VM names (case-insensitive). A name found on one backend
// resolves to it; a name found on several resolves to the preferred backend
// if that is one of them, and is reported as ambiguous otherwise. Within a
// backend the first VM in inventory order wins, as before.
func (inv *Inventory) ByName(names []string) Resolution {
	res := Resolution{Ambiguous: map[string][]string{}}
	seen := map[string]bool{}
	for _, name := range names {
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true

		var matches []Target
		for _, b := range inv.backends {
			for _, vm := range b.vms {
				if strings.EqualFold(vm.Name, name) {
					matches = append(matches, Target{VM: vm, HV: b.hv})
					break
				}
			}
		}

		switch len(matches) {
		case 0:
			res.Missing = append(res.Missing, name)
		case 1:
			res.Targets = append(res.Targets, matches[0])
		default:
			picked := false
			for _, m := range matches {
				if m.Backend == inv.preferred {
					res.Targets = append(res.Targets, m)
					picked = true
					break
				}
			}
			if !picked {
				for _, m := range matches {
					res.Ambiguous[name] = append(res.Ambiguous[name], m.Backend)
				}
			}
		}
	}
	return res
}

// ParseFolders splits a --folder value such as "DEV" or "DEV,TEST".
func ParseFolders(v string) []string {
	var out []string
	for _, f := range strings.Split(v, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// MatchFolder reports whether folder equals any of folders, ignoring case.
func MatchFolder(folder string, folders []string) bool {
	for _, f := range folders {
		if strings.EqualFold(folder, f) {
			return true
		}
	}
	return false
}

// SortTargets orders targets by VM name, then backend.
func SortTargets(ts []Target) {
	sort.SliceStable(ts, func(i, j int) bool {
		a, b := strings.ToLower(ts[i].Name), strings.ToLower(ts[j].Name)
		if a != b {
			return a < b
		}
		return ts[i].Backend < ts[j].Backend
	})
}
