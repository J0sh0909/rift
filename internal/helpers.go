package internal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type VM struct {
	Name    string
	Path    string
	Folder  string
	Running bool
}

type NIC struct {
	Index      string
	Type       string // connection type: bridged, nat, hostonly, custom, pvn
	VirtualDev string // virtual device: e1000, e1000e, vmxnet3
	MAC        string
}

type VNetwork struct {
	Device string
	Name   string
	Type   string
}

type VMSpecs struct {
	CPUCount       string
	CoresPerSocket string
	Sockets        string
	NestedVirt     string
	PerfCounters   string
	MemoryMB       string
}

type Disk struct {
	Controller string
	Index      string
	Slot       string
	FileName   string
	SizeGB     string
	DiskType   string
	Adapter    string
}

type CDDrive struct {
	Controller     string
	Slot           string
	DeviceType     string
	FileName       string
	StartConnected string
}

type DisplaySettings struct {
	Accelerated3D    string
	GraphicsMemoryMB string
}

var RequiresPowerOff = map[string]bool{
	"cpu":          true,
	"ram":          true,
	"nestedvirt":   true,
	"perfcounters": true,
	"nic-add":      true,
	"nic-remove":   true,
	"nic-type":     false,
	"nic-regen":    true,
	"disk-add":     true,
	"disk-remove":  true,
	"disk-expand":  true,
	"disk-defrag":  true,
	"disk-compact": true,
	"display":      true,
	"tpm":          true,
	"revert":       true,
	"export":       true,
}

// ---------------------------------------------------------------------------
// Target Resolution — eliminates repeated LoadSettings/GetPowerState/filter
// ---------------------------------------------------------------------------

// ResolveTargets gets power state via hv and resolves VM targets
// from either a folder flag or a list of VM names. Returns targets
// and any error. Prints "not found" messages for missing VMs.
func ResolveTargets(hv Hypervisor, folderFlag string, args []string) ([]VM, error) {
	vms, err := hv.GetPowerState()
	if err != nil {
		return nil, err
	}

	var targets []VM

	if folderFlag != "" {
		for _, v := range vms {
			if strings.EqualFold(v.Folder, folderFlag) {
				targets = append(targets, v)
			}
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("no VMs found in folder '%s'", folderFlag)
		}
	} else {
		for _, name := range args {
			found := false
			for _, v := range vms {
				if strings.EqualFold(v.Name, name) {
					targets = append(targets, v)
					found = true
					break
				}
			}
			if !found {
				fmt.Printf("VM '%s' not found\n", name)
			}
		}
	}

	return targets, nil
}

// ResolveAllVMs gets all VMs via hv (for info with no args).
func ResolveAllVMs(hv Hypervisor, folderFlag string, args []string) ([]VM, error) {
	vms, err := hv.GetPowerState()
	if err != nil {
		return nil, err
	}

	var targets []VM

	if folderFlag != "" {
		for _, v := range vms {
			if strings.EqualFold(v.Folder, folderFlag) {
				targets = append(targets, v)
			}
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("no VMs found in folder '%s'", folderFlag)
		}
	} else if len(args) > 0 {
		for _, name := range args {
			found := false
			for _, v := range vms {
				if strings.EqualFold(v.Name, name) {
					targets = append(targets, v)
					found = true
					break
				}
			}
			if !found {
				fmt.Printf("VM '%s' not found\n", name)
			}
		}
	} else {
		targets = vms
	}

	return targets, nil
}

// ---------------------------------------------------------------------------
// Hardware Validation
// ---------------------------------------------------------------------------

// ValidateRAM checks that requested RAM doesn't exceed host capacity.
func ValidateRAM(host HostResources, requestedGB int) error {
	if host.TotalRAMGB > 0 && requestedGB > host.TotalRAMGB {
		return fmt.Errorf("requested %d GB RAM exceeds host capacity of %d GB", requestedGB, host.TotalRAMGB)
	}
	return nil
}

// ValidateCPU checks that requested vCPUs don't exceed host threads.
func ValidateCPU(host HostResources, sockets, cores int) error {
	total := sockets * cores
	if host.CPUThreads > 0 && total > host.CPUThreads {
		return fmt.Errorf("requested %d vCPUs (%d sockets × %d cores) exceeds host's %d logical processors", total, sockets, cores, host.CPUThreads)
	}
	return nil
}

// ValidateGfxMem checks that requested graphics memory doesn't exceed host RAM.
func ValidateGfxMem(host HostResources, requestedMB int) error {
	if host.TotalRAMGB > 0 && requestedMB > host.TotalRAMGB*1024/2 {
		return fmt.Errorf("requested %d MB graphics memory exceeds half of host RAM (%d GB)", requestedMB, host.TotalRAMGB)
	}
	return nil
}

// ValidateDiskSpace checks free space on the drive or mount point containing the VM.
func ValidateDiskSpace(host HostResources, vmxPath string, requestedGB int) error {
	if len(host.FreeDiskGB) == 0 {
		return nil
	}
	if runtime.GOOS == "windows" {
		// Extract drive letter from vmx path (e.g., "C:" from "C:\Users\...")
		if len(vmxPath) >= 2 && vmxPath[1] == ':' {
			drive := strings.ToUpper(vmxPath[:2])
			if free, ok := host.FreeDiskGB[drive]; ok {
				if requestedGB > free {
					return fmt.Errorf("requested %d GB disk exceeds free space on %s (%d GB free)", requestedGB, drive, free)
				}
			}
		}
	} else {
		// Find the longest mount point that is a prefix of vmxPath.
		best := ""
		for mp := range host.FreeDiskGB {
			if strings.HasPrefix(vmxPath, mp) && len(mp) > len(best) {
				best = mp
			}
		}
		if best != "" {
			if free := host.FreeDiskGB[best]; requestedGB > free {
				return fmt.Errorf("requested %d GB disk exceeds free space on %s (%d GB free)", requestedGB, best, free)
			}
		}
	}
	return nil
}

func GetGuestOS(vmxPath string) string {
	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return ""
	}
	return data["guestos"]
}

func DefaultInterpreter(guestOS string) (string, bool) {
	lower := strings.ToLower(guestOS)

	// Ordered longest-prefix-first so a more specific prefix is never shadowed
	// by a shorter one that shares the same leading characters.
	prefixes := []struct {
		prefix      string
		interpreter string
	}{
		// 13
		{"vmware-photon", "/bin/bash"},
		// 12
		{"bottlerocket", "/bin/sh"},
		// 11
		{"openindiana", "/bin/sh"},
		{"amazonlinux", "/bin/bash"},
		{"oraclelinux", "/bin/bash"},
		{"clear-linux", "/bin/bash"},
		// 10
		{"elementary", "/bin/bash"},
		{"scientific", "/bin/bash"},
		// 9
		{"dragonfly", "/bin/sh"},
		{"endeavour", "/bin/bash"},
		{"calculate", "/bin/bash"},
		{"slackware", "/bin/bash"},
		{"buildroot", "/bin/sh"},
		{"blackarch", "/bin/bash"},
		// 8
		{"opensuse", "/bin/bash"},
		{"opnsense", "/bin/sh"},
		{"mikrotik", "/bin/sh"},
		// 7
		{"windows", `C:\Windows\System32\cmd.exe`},
		{"freebsd", "/bin/sh"},
		{"openbsd", "/bin/sh"},
		{"solaris", "/bin/sh"},
		{"smartos", "/bin/sh"},
		{"clearos", "/bin/bash"},
		{"manjaro", "/bin/bash"},
		{"flatcar", "/bin/bash"},
		{"rancher", "/bin/bash"},
		{"busybox", "/bin/sh"},
		{"pfsense", "/bin/sh"},
		{"proxmox", "/bin/bash"},
		{"truenas", "/bin/sh"},
		{"openwrt", "/bin/sh"},
		// 6
		{"darwin", "/bin/zsh"},
		{"netbsd", "/bin/sh"},
		{"ubuntu", "/bin/bash"},
		{"debian", "/bin/bash"},
		{"parrot", "/bin/bash"},
		{"deepin", "/bin/bash"},
		{"centos", "/bin/bash"},
		{"fedora", "/bin/bash"},
		{"garuda", "/bin/bash"},
		{"gentoo", "/bin/bash"},
		{"funtoo", "/bin/bash"},
		{"alpine", "/bin/sh"},
		{"coreos", "/bin/bash"},
		{"whonix", "/bin/bash"},
		{"pentoo", "/bin/bash"},
		{"dd-wrt", "/bin/sh"},
		{"unraid", "/bin/bash"},
		{"xcp-ng", "/bin/bash"},
		// 5
		{"zorin", "/bin/bash"},
		{"rocky", "/bin/bash"},
		{"nixos", "/bin/sh"},
		{"puppy", "/bin/sh"},
		{"talos", "/bin/sh"},
		{"tails", "/bin/bash"},
		{"qubes", "/bin/bash"},
		// 4
		{"mint", "/bin/bash"},
		{"kali", "/bin/bash"},
		{"rhel", "/bin/bash"},
		{"alma", "/bin/bash"},
		{"suse", "/bin/bash"},
		{"sles", "/bin/bash"},
		{"arch", "/bin/bash"},
		{"void", "/bin/sh"},
		{"guix", "/bin/sh"},
		{"esxi", "/bin/sh"},
		{"vyos", "/bin/bash"},
		{"tiny", "/bin/sh"},
		// 3
		{"pop", "/bin/bash"},
		// 2
		{"mx", "/bin/bash"},
	}

	for _, p := range prefixes {
		if strings.HasPrefix(lower, p.prefix) {
			return p.interpreter, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// VMX parsing
// ---------------------------------------------------------------------------

func ParseVMXKeys(vmxPath string) (map[string]string, error) {
	content, err := os.ReadFile(vmxPath)
	if err != nil {
		return nil, fmt.Errorf("reading VMX file: %w", err)
	}
	data := make(map[string]string)
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, " = ", 2)
		if len(parts) != 2 {
			continue
		}
		data[strings.ToLower(parts[0])] = strings.Trim(parts[1], "\"")
	}
	return data, nil
}

func SetVMXKey(vmxPath string, key string, value string) error {
	content, err := os.ReadFile(vmxPath)
	if err != nil {
		return fmt.Errorf("reading VMX file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	found := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		parts := strings.SplitN(trimmed, " = ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], key) {
			lines[i] = fmt.Sprintf("%s = \"%s\"", key, value)
			found = true
			break
		}
	}

	if !found {
		lines = append(lines, fmt.Sprintf("%s = \"%s\"", key, value))
	}

	return os.WriteFile(vmxPath, []byte(strings.Join(lines, "\n")), 0644)
}

func RemoveVMXKey(vmxPath string, key string) error {
	content, err := os.ReadFile(vmxPath)
	if err != nil {
		return fmt.Errorf("reading VMX file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		parts := strings.SplitN(trimmed, " = ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], key) {
			continue
		}
		newLines = append(newLines, line)
	}

	return os.WriteFile(vmxPath, []byte(strings.Join(newLines, "\n")), 0644)
}

// RemoveVMXPrefix removes all keys starting with the given prefix.
func RemoveVMXPrefix(vmxPath string, prefix string) error {
	content, err := os.ReadFile(vmxPath)
	if err != nil {
		return fmt.Errorf("reading VMX file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			continue
		}
		newLines = append(newLines, line)
	}

	return os.WriteFile(vmxPath, []byte(strings.Join(newLines, "\n")), 0644)
}

// ---------------------------------------------------------------------------
// VM Specs
// ---------------------------------------------------------------------------

func ParseVMXSpecs(vmxPath string) (VMSpecs, error) {
	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return VMSpecs{}, err
	}

	cpuCount := data["numvcpus"]
	if cpuCount == "" {
		cpuCount = "1"
	}
	coresPerSocket := data["cpuid.corespersocket"]
	if coresPerSocket == "" {
		coresPerSocket = "1"
	}

	sockets := "1"
	cpuInt := 1
	coresInt := 1
	fmt.Sscanf(cpuCount, "%d", &cpuInt)
	fmt.Sscanf(coresPerSocket, "%d", &coresInt)
	if coresInt > 0 {
		sockets = fmt.Sprintf("%d", cpuInt/coresInt)
	}

	nestedVirt := data["vhv.enable"]
	if nestedVirt == "" {
		nestedVirt = "FALSE"
	}

	perfCounters := data["vpmc.enable"]
	if perfCounters == "" {
		perfCounters = "FALSE"
	}

	memoryMB := data["memsize"]
	if memoryMB == "" {
		memoryMB = "0"
	}

	return VMSpecs{
		CPUCount:       cpuCount,
		CoresPerSocket: coresPerSocket,
		Sockets:        sockets,
		NestedVirt:     nestedVirt,
		PerfCounters:   perfCounters,
		MemoryMB:       memoryMB,
	}, nil
}

// ---------------------------------------------------------------------------
// VM Discovery & Power State
// ---------------------------------------------------------------------------

func DiscoverVMs(vmDirectory string) ([]VM, error) {
	var vms []VM
	err := filepath.Walk(vmDirectory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".vmx" {
			vmName := strings.TrimSuffix(info.Name(), ".vmx")
			relPath, _ := filepath.Rel(vmDirectory, path)
			folder := filepath.Dir(relPath)
			vms = append(vms, VM{Name: vmName, Path: path, Folder: folder})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning VM directory: %w", err)
	}
	return vms, nil
}

// ---------------------------------------------------------------------------
// OVF Tool
// ---------------------------------------------------------------------------

// ArchiveEntry represents a discovered VM archive under the archive root.
type ArchiveEntry struct {
	Format    string // "OVF" or "OVA"
	Folder    string // project folder, empty if standalone
	VMName    string
	Version   string // timestamped label (no extension)
	Path      string // version dir (OVF) or .ova file (OVA)
	SizeBytes int64
}

// ScanArchives recursively scans archivePath/OVF and archivePath/OVA.
func ScanArchives(archivePath string) ([]ArchiveEntry, error) {
	var entries []ArchiveEntry
	seen := make(map[string]bool)

	for _, format := range []string{"OVF", "OVA"} {
		base := filepath.Join(archivePath, format)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if format == "OVF" && !info.IsDir() && ext == ".ovf" {
				versionDir := filepath.Dir(path)
				if seen[versionDir] {
					return nil
				}
				seen[versionDir] = true
				relVersion, _ := filepath.Rel(base, versionDir)
				parts := strings.Split(relVersion, string(filepath.Separator))
				var folder, vmname string
				switch len(parts) {
				case 2:
					vmname = parts[0]
				case 3:
					folder, vmname = parts[0], parts[1]
				default:
					return nil
				}
				size, _ := dirSize(versionDir)
				entries = append(entries, ArchiveEntry{
					Format: "OVF", Folder: folder, VMName: vmname,
					Version: filepath.Base(versionDir), Path: versionDir, SizeBytes: size,
				})
			} else if format == "OVA" && !info.IsDir() && ext == ".ova" {
				relFile, _ := filepath.Rel(base, path)
				dirParts := strings.Split(filepath.Dir(relFile), string(filepath.Separator))
				var folder, vmname string
				switch len(dirParts) {
				case 1:
					if dirParts[0] == "." {
						return nil
					}
					vmname = dirParts[0]
				case 2:
					folder, vmname = dirParts[0], dirParts[1]
				default:
					return nil
				}
				version := strings.TrimSuffix(filepath.Base(path), ".ova")
				entries = append(entries, ArchiveEntry{
					Format: "OVA", Folder: folder, VMName: vmname,
					Version: version, Path: path, SizeBytes: info.Size(),
				})
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scanning %s archives: %w", format, err)
		}
	}
	return entries, nil
}

func dirSize(path string) (int64, error) {
	var total int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// ---------------------------------------------------------------------------
// Networking
// ---------------------------------------------------------------------------

func ParseVMXNetworking(vmxPath string, pvnNames map[string]string) ([]NIC, error) {
	content, err := os.ReadFile(vmxPath)
	if err != nil {
		return nil, fmt.Errorf("reading VMX file: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	nicData := make(map[string]map[string]string)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ethernet") {
			continue
		}
		parts := strings.SplitN(line, " = ", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		value := strings.Trim(parts[1], "\"")

		dotIndex := strings.Index(key, ".")
		if dotIndex == -1 {
			continue
		}
		num := key[8:dotIndex]
		prop := key[dotIndex+1:]

		if nicData[num] == nil {
			nicData[num] = make(map[string]string)
		}
		nicData[num][strings.ToLower(prop)] = value
	}

	var nics []NIC
	for num, data := range nicData {
		if data["present"] != "TRUE" {
			continue
		}
		mac := data["generatedaddress"]
		if mac == "" {
			mac = data["address"]
		}
		connType := data["connectiontype"]
		switch connType {
		case "":
			if data["linkstatepropagation.enable"] == "TRUE" {
				connType = "bridged, physical"
			} else {
				connType = "bridged"
			}
		case "custom":
			if vnet := data["vnet"]; vnet != "" {
				connType = "custom (" + vnet + ")"
			}
		case "pvn":
			segName := "LAN segment"
			if pvnNames != nil {
				if name, ok := pvnNames[data["pvnid"]]; ok {
					segName = "LAN segment (" + name + ")"
				}
			}
			connType = segName
		}
		nics = append(nics, NIC{
			Index:      num,
			Type:       connType,
			VirtualDev: data["virtualdev"],
			MAC:        mac,
		})
	}
	sort.Slice(nics, func(i, j int) bool {
		return nics[i].Index < nics[j].Index
	})
	return nics, nil
}

func LoadPVNNames(inventoryPath string) map[string]string {
	prefsPath := filepath.Dir(inventoryPath) + string(os.PathSeparator) + "preferences.ini"
	content, err := os.ReadFile(prefsPath)
	if err != nil {
		return nil
	}

	names := make(map[string]string)
	entries := make(map[string]map[string]string)

	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "pref.namedPVNs") {
			continue
		}
		parts := strings.SplitN(line, " = ", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		value := strings.Trim(parts[1], "\"")

		after := strings.TrimPrefix(key, "pref.namedPVNs")
		dotIndex := strings.Index(after, ".")
		if dotIndex == -1 {
			continue
		}
		num := after[:dotIndex]
		prop := after[dotIndex+1:]

		if entries[num] == nil {
			entries[num] = make(map[string]string)
		}
		entries[num][prop] = value
	}

	for _, entry := range entries {
		if entry["pvnID"] != "" && entry["name"] != "" {
			names[entry["pvnID"]] = entry["name"]
		}
	}

	return names
}

func LoadVirtualNetworks(netmapPath string) ([]VNetwork, error) {
	content, err := os.ReadFile(netmapPath)
	if err != nil {
		return nil, fmt.Errorf("reading netmap.conf: %w", err)
	}

	entries := make(map[string]map[string]string)
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		parts := strings.SplitN(line, " = ", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		value := strings.Trim(parts[1], "\"")

		dotIndex := strings.Index(key, ".")
		if dotIndex == -1 {
			continue
		}
		num := key[7:dotIndex]
		prop := key[dotIndex+1:]

		if entries[num] == nil {
			entries[num] = make(map[string]string)
		}
		entries[num][prop] = value
	}

	var networks []VNetwork
	for _, entry := range entries {
		netType := "custom"
		switch entry["name"] {
		case "Bridged":
			netType = "bridged"
		case "HostOnly":
			netType = "hostonly"
		case "NAT":
			netType = "nat"
		}
		networks = append(networks, VNetwork{
			Device: entry["device"],
			Name:   entry["name"],
			Type:   netType,
		})
	}

	sort.Slice(networks, func(i, j int) bool {
		numI := 0
		numJ := 0
		fmt.Sscanf(networks[i].Device, "vmnet%d", &numI)
		fmt.Sscanf(networks[j].Device, "vmnet%d", &numJ)
		return numI < numJ
	})

	return networks, nil
}

// ---------------------------------------------------------------------------
// NIC Operations
// ---------------------------------------------------------------------------

func SetNICType(vmxPath string, index int, nicType string, vnet string) error {
	prefix := fmt.Sprintf("ethernet%d", index)

	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return err
	}

	if data[prefix+".present"] != "TRUE" {
		return fmt.Errorf("NIC %d does not exist", index)
	}

	// Common cleanup for all types
	keysToRemove := []string{
		prefix + ".vnet",
		prefix + ".pvnID",
		prefix + ".displayName",
		prefix + ".linkStatePropagation.enable",
	}
	for _, k := range keysToRemove {
		RemoveVMXKey(vmxPath, k)
	}

	switch nicType {
	case "bridged":
		RemoveVMXKey(vmxPath, prefix+".connectionType")
	case "nat":
		SetVMXKey(vmxPath, prefix+".connectionType", "nat")
	case "hostonly":
		SetVMXKey(vmxPath, prefix+".connectionType", "hostonly")
	case "custom":
		if vnet == "" {
			return fmt.Errorf("--vnet is required for custom type (e.g. VMnet2)")
		}
		SetVMXKey(vmxPath, prefix+".connectionType", "custom")
		SetVMXKey(vmxPath, prefix+".vnet", vnet)
		SetVMXKey(vmxPath, prefix+".displayName", vnet)
	default:
		return fmt.Errorf("unknown NIC type: %s (use bridged, nat, hostonly, custom)", nicType)
	}

	return nil
}

func AddNIC(vmxPath string, nicType string, vnet string) (int, error) {
	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return -1, err
	}

	index := 0
	for {
		if data[fmt.Sprintf("ethernet%d.present", index)] != "TRUE" {
			break
		}
		index++
	}

	prefix := fmt.Sprintf("ethernet%d", index)
	SetVMXKey(vmxPath, prefix+".present", "TRUE")
	SetVMXKey(vmxPath, prefix+".virtualDev", "e1000e")
	SetVMXKey(vmxPath, prefix+".addressType", "generated")

	switch nicType {
	case "bridged":
		// No connectionType needed
	case "nat":
		SetVMXKey(vmxPath, prefix+".connectionType", "nat")
	case "hostonly":
		SetVMXKey(vmxPath, prefix+".connectionType", "hostonly")
	case "custom":
		if vnet == "" {
			return -1, fmt.Errorf("--vnet is required for custom type")
		}
		SetVMXKey(vmxPath, prefix+".connectionType", "custom")
		SetVMXKey(vmxPath, prefix+".vnet", vnet)
		SetVMXKey(vmxPath, prefix+".displayName", vnet)
	default:
		return -1, fmt.Errorf("unknown NIC type: %s", nicType)
	}

	return index, nil
}

func RemoveNIC(vmxPath string, index int) error {
	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return err
	}

	prefix := fmt.Sprintf("ethernet%d", index)
	if data[prefix+".present"] != "TRUE" {
		return fmt.Errorf("NIC %d does not exist", index)
	}

	return RemoveVMXPrefix(vmxPath, prefix+".")
}

func RegenMAC(vmxPath string, index int) error {
	prefix := fmt.Sprintf("ethernet%d", index)

	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return err
	}

	if data[prefix+".present"] != "TRUE" {
		return fmt.Errorf("NIC %d does not exist", index)
	}

	RemoveVMXKey(vmxPath, prefix+".generatedAddress")
	RemoveVMXKey(vmxPath, prefix+".generatedAddressOffset")
	RemoveVMXKey(vmxPath, prefix+".address")
	SetVMXKey(vmxPath, prefix+".addressType", "generated")

	return nil
}

// --------------------------------------------------------------------------
// Disk Operations — unified pattern
// --------------------------------------------------------------------------

func ParseVMXDisks(vmxPath string) ([]Disk, error) {
	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return nil, err
	}

	var disks []Disk
	controllers := []string{"scsi", "sata", "nvme", "ide"}

	for _, ctrl := range controllers {
		for i := 0; i < 4; i++ {
			for j := 0; j < 16; j++ {
				prefix := fmt.Sprintf("%s%d:%d", ctrl, i, j)
				if data[prefix+".present"] != "TRUE" {
					continue
				}
				if data[prefix+".devicetype"] != "" && data[prefix+".devicetype"] != "disk" {
					continue
				}
				fileName := data[prefix+".filename"]
				if !strings.HasSuffix(fileName, ".vmdk") {
					continue
				}

				disk := Disk{
					Controller: ctrl,
					Index:      fmt.Sprintf("%d", i),
					Slot:       fmt.Sprintf("%d", j),
					FileName:   fileName,
				}

				vmxDir := filepath.Dir(vmxPath)
				vmdkPath := filepath.Join(vmxDir, fileName)
				diskType, sizeGB, adapter := ParseVMDKDescriptor(vmdkPath)
				disk.DiskType = diskType
				disk.SizeGB = sizeGB
				disk.Adapter = adapter

				disks = append(disks, disk)
			}
		}
	}

	return disks, nil
}

func ParseVMDKDescriptor(vmdkPath string) (diskType string, sizeGB string, adapter string) {
	f, err := os.Open(vmdkPath)
	if err != nil {
		return "unknown", "unknown", "unknown"
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return "unknown", "unknown", "unknown"
	}

	lines := strings.Split(string(buf[:n]), "\n")
	var totalSectors int64

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "createType=") {
			raw := strings.Trim(strings.TrimPrefix(line, "createType="), "\"")
			switch raw {
			case "monolithicSparse":
				diskType = "single file, growable"
			case "monolithicFlat":
				diskType = "single file, pre-allocated"
			case "twoGbMaxExtentSparse":
				diskType = "split files, growable"
			case "twoGbMaxExtentFlat":
				diskType = "split files, pre-allocated"
			default:
				diskType = raw
			}
		}

		if strings.HasPrefix(line, "RW ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				var sectors int64
				fmt.Sscanf(parts[1], "%d", &sectors)
				totalSectors += sectors
			}
		}

		if strings.HasPrefix(line, "ddb.adapterType") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				adapter = strings.TrimSpace(strings.Trim(parts[1], " \""))
			}
		}
	}

	if totalSectors > 0 {
		gb := float64(totalSectors) * 512 / 1024 / 1024 / 1024
		sizeGB = fmt.Sprintf("%.1f GB", gb)
	} else {
		sizeGB = "unknown"
	}

	if adapter == "" {
		adapter = "unknown"
	}

	return diskType, sizeGB, adapter
}

// resolveDiskPath looks up the VMDK filename for a controller:slot from the VMX.
func resolveDiskPath(vmxPath string, controller string, slot int) (string, error) {
	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return "", err
	}

	prefix := fmt.Sprintf("%s:%d", controller, slot)
	fileName := data[prefix+".filename"]
	if fileName == "" {
		return "", fmt.Errorf("disk %s does not exist", prefix)
	}

	vmxDir := filepath.Dir(vmxPath)
	return filepath.Join(vmxDir, fileName), nil
}

// RunVdiskManager is the unified wrapper for vmware-vdiskmanager operations.
// It resolves the disk path from VMX, then runs vdiskmanager with the given args.
func RunVdiskManager(vdiskPath string, vmxPath string, controller string, slot int, args ...string) error {
	diskPath, err := resolveDiskPath(vmxPath, controller, slot)
	if err != nil {
		return err
	}

	fullArgs := append(args, diskPath)
	cmd := exec.Command(vdiskPath, fullArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("vdiskmanager %v failed: %w\nOutput: %s", args, err, output)
	}

	return nil
}

func CreateDisk(vdiskPath string, vmxPath string, controller string, sizeGB int, diskType int) (string, error) {
	vmxDir := filepath.Dir(vmxPath)
	vmName := strings.TrimSuffix(filepath.Base(vmxPath), ".vmx")

	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return "", err
	}

	slot := -1
	for j := 0; j < 16; j++ {
		prefix := fmt.Sprintf("%s:%d", controller, j)
		if data[prefix+".present"] != "TRUE" {
			slot = j
			break
		}
	}
	if slot == -1 {
		return "", fmt.Errorf("no available slot on %s", controller)
	}

	fileName := fmt.Sprintf("%s-%s-%d.vmdk", vmName, controller, slot)
	diskPath := filepath.Join(vmxDir, fileName)

	adapter := "lsilogic"
	if strings.HasPrefix(controller, "ide") {
		adapter = "ide"
	}

	cmd := exec.Command(vdiskPath, "-c",
		"-s", fmt.Sprintf("%dGB", sizeGB),
		"-a", adapter,
		"-t", fmt.Sprintf("%d", diskType),
		diskPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("creating disk: %w\nOutput: %s", err, output)
	}

	prefix := fmt.Sprintf("%s:%d", controller, slot)
	SetVMXKey(vmxPath, prefix+".present", "TRUE")
	SetVMXKey(vmxPath, prefix+".fileName", fileName)

	ctrlPrefix := strings.Split(controller, ":")[0]
	if data[ctrlPrefix+".present"] != "TRUE" {
		SetVMXKey(vmxPath, ctrlPrefix+".present", "TRUE")
	}

	return fmt.Sprintf("%s:%d", controller, slot), nil
}

func RemoveDisk(vmxPath string, controller string, slot int, deleteFiles bool) error {
	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return err
	}

	prefix := fmt.Sprintf("%s:%d", controller, slot)
	if data[prefix+".present"] != "TRUE" {
		return fmt.Errorf("disk %s does not exist", prefix)
	}

	// Count total disks across all controllers
	totalDisks := 0
	for _, ctrl := range []string{"scsi", "sata", "nvme", "ide"} {
		for i := 0; i < 4; i++ {
			for j := 0; j < 16; j++ {
				p := fmt.Sprintf("%s%d:%d", ctrl, i, j)
				if data[p+".present"] == "TRUE" && strings.HasSuffix(data[p+".filename"], ".vmdk") {
					totalDisks++
				}
			}
		}
	}
	if totalDisks <= 1 {
		return fmt.Errorf("cannot remove the only disk on the VM")
	}

	fileName := data[prefix+".filename"]

	// Remove all keys with this prefix from VMX
	err = RemoveVMXPrefix(vmxPath, prefix+".")
	if err != nil {
		return err
	}

	// Delete VMDK files if requested
	if deleteFiles && fileName != "" {
		vmxDir := filepath.Dir(vmxPath)
		baseName := strings.TrimSuffix(fileName, ".vmdk")
		filepath.Walk(vmxDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			name := info.Name()
			if strings.HasPrefix(name, baseName) && strings.HasSuffix(name, ".vmdk") {
				os.Remove(path)
			}
			return nil
		})
	}

	return nil
}

// ExpandDisk expands a disk to a new size using the unified vdiskmanager wrapper.
func ExpandDisk(vdiskPath string, vmxPath string, controller string, slot int, newSizeGB int) error {
	return RunVdiskManager(vdiskPath, vmxPath, controller, slot, "-x", fmt.Sprintf("%dGB", newSizeGB))
}

// DefragDisk defragments a disk using the unified vdiskmanager wrapper.
func DefragDisk(vdiskPath string, vmxPath string, controller string, slot int) error {
	return RunVdiskManager(vdiskPath, vmxPath, controller, slot, "-d")
}

// CompactDisk compacts a disk using the unified vdiskmanager wrapper.
func CompactDisk(vdiskPath string, vmxPath string, controller string, slot int) error {
	return RunVdiskManager(vdiskPath, vmxPath, controller, slot, "-k")
}

// ---------------------------------------------------------------------------
// CD/DVD Operations
// ---------------------------------------------------------------------------

func ListISOs(isoDirectory string) ([]string, error) {
	var isos []string
	err := filepath.Walk(isoDirectory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".iso") {
			isos = append(isos, info.Name())
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning ISO directory: %w", err)
	}
	return isos, nil
}

func ParseVMXCDDrives(vmxPath string) ([]CDDrive, error) {
	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return nil, err
	}

	var drives []CDDrive
	controllers := []string{"sata", "ide"}

	for _, ctrl := range controllers {
		for i := 0; i < 4; i++ {
			for j := 0; j < 4; j++ {
				prefix := fmt.Sprintf("%s%d:%d", ctrl, i, j)
				if data[prefix+".present"] != "TRUE" {
					continue
				}
				devType := data[prefix+".devicetype"]
				if devType != "cdrom-image" && devType != "cdrom-raw" {
					continue
				}

				startConn := data[prefix+".startconnected"]
				if startConn == "" {
					startConn = "TRUE"
				}

				typeName := "auto detect"
				if devType == "cdrom-image" {
					typeName = "ISO image"
				}

				drives = append(drives, CDDrive{
					Controller:     fmt.Sprintf("%s%d", ctrl, i),
					Slot:           fmt.Sprintf("%d", j),
					DeviceType:     typeName,
					FileName:       data[prefix+".filename"],
					StartConnected: startConn,
				})
			}
		}
	}

	return drives, nil
}

func MountISO(vmxPath string, controller string, slot int, isoPath string) error {
	prefix := fmt.Sprintf("%s:%d", controller, slot)

	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return err
	}

	if data[prefix+".present"] != "TRUE" {
		SetVMXKey(vmxPath, prefix+".present", "TRUE")
	}

	SetVMXKey(vmxPath, prefix+".deviceType", "cdrom-image")
	SetVMXKey(vmxPath, prefix+".fileName", isoPath)
	SetVMXKey(vmxPath, prefix+".startConnected", "TRUE")

	return nil
}

func UnmountISO(vmxPath string, controller string, slot int) error {
	prefix := fmt.Sprintf("%s:%d", controller, slot)

	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return err
	}

	if data[prefix+".present"] != "TRUE" {
		return fmt.Errorf("CD/DVD %s:%d does not exist", controller, slot)
	}

	SetVMXKey(vmxPath, prefix+".deviceType", "cdrom-raw")
	SetVMXKey(vmxPath, prefix+".fileName", "auto detect")
	RemoveVMXKey(vmxPath, prefix+".startConnected")
	SetVMXKey(vmxPath, prefix+".autodetect", "TRUE")

	return nil
}

func SetCDROMBoot(vmxPath string, controller string, slot int, connect bool) error {
	prefix := fmt.Sprintf("%s:%d", controller, slot)

	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return err
	}

	if data[prefix+".present"] != "TRUE" {
		return fmt.Errorf("CD/DVD %s:%d does not exist", controller, slot)
	}

	val := "FALSE"
	if connect {
		val = "TRUE"
	}
	return SetVMXKey(vmxPath, prefix+".startConnected", val)
}

// ---------------------------------------------------------------------------
// Display
// ---------------------------------------------------------------------------

func ParseVMXDisplay(vmxPath string) (DisplaySettings, error) {
	data, err := ParseVMXKeys(vmxPath)
	if err != nil {
		return DisplaySettings{}, err
	}

	accel := data["mks.enable3d"]
	if accel == "" {
		accel = "FALSE"
	}

	gfxKB := data["svga.graphicsmemorykb"]
	gfxMB := "0"
	if gfxKB != "" {
		var kb int
		fmt.Sscanf(gfxKB, "%d", &kb)
		gfxMB = fmt.Sprintf("%d", kb/1024)
	}

	return DisplaySettings{
		Accelerated3D:    accel,
		GraphicsMemoryMB: gfxMB,
	}, nil
}
