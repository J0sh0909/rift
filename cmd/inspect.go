package cmd

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/J0sh0909/rift/internal/aws"
	"github.com/J0sh0909/rift/internal/core"
	"github.com/J0sh0909/rift/internal/vbox"
	"github.com/spf13/cobra"
)

var (
	netFlag     bool
	specsFlag   bool
	diskFlag    bool
	cdromFlag   bool
	displayFlag bool
)

// ---------------------------------------------------------------------------
// list
// ---------------------------------------------------------------------------

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List VMs on this node by hypervisor and folder",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		inv := nodeInventory()
		folders := core.ParseFolders(folderFlag)
		byBackend := map[string][]core.VM{}
		for _, t := range inv.All() {
			if len(folders) == 0 || core.MatchFolder(t.Folder, folders) {
				byBackend[t.Backend] = append(byBackend[t.Backend], t.VM)
			}
		}

		backends := inv.Backends()
		multi := len(backends) > 1
		total := 0
		for _, b := range backends {
			vms := byBackend[b]
			label := core.BackendLabel(b)
			if len(vms) == 0 {
				if multi {
					fmt.Printf("No %s VMs found\n\n", label)
				} else {
					fmt.Println("No VMs found")
				}
				continue
			}
			if multi {
				fmt.Println(label)
			}
			printFolderTree(vms)
			total += len(vms)
			if multi {
				fmt.Println()
			}
		}
		if multi && total == 0 {
			fmt.Println("No VMs found")
		}
	},
}

// printFolderTree prints VMs grouped by folder (folders sorted, VMs in
// inventory order).
func printFolderTree(vms []core.VM) {
	folders := map[string][]core.VM{}
	for _, vm := range vms {
		folders[vm.Folder] = append(folders[vm.Folder], vm)
	}
	names := sortedKeys(folders)
	for i, folder := range names {
		folderPrefix, childPrefix := "├──", "│   "
		if i == len(names)-1 {
			folderPrefix, childPrefix = "└──", "    "
		}
		fmt.Printf("%s %s\n", folderPrefix, folder)
		for j, vm := range folders[folder] {
			vmPrefix := childPrefix + "├──"
			if j == len(folders[folder])-1 {
				vmPrefix = childPrefix + "└──"
			}
			fmt.Printf("%s %s [%s]\n", vmPrefix, vm.Name, onOff(vm.Running))
		}
	}
}

func onOff(running bool) string {
	if running {
		return "ON"
	}
	return "OFF"
}

// ---------------------------------------------------------------------------
// overview
// ---------------------------------------------------------------------------

var overviewCmd = &cobra.Command{
	Use:   "overview",
	Short: "Show VMs across all detected hypervisors and AWS",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		// AWS may work without .env, so a settings error is reported but
		// doesn't stop the AWS section.
		s, settingsErr := core.LoadSettings()
		if settingsErr != nil {
			fail(core.ErrConfig, "%s", settingsErr)
			noteServiceAccount()
		}

		// AWS is a network call: run it alongside the local hypervisors.
		var (
			wg       sync.WaitGroup
			awsInsts []aws.EC2Instance
			awsErr   error
		)
		if s.AWSRegion != "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				backend, err := aws.NewAWSBackend(s.AWSRegion)
				if err != nil {
					awsErr = err
					return
				}
				awsInsts, awsErr = backend.ListInstances(false)
			}()
		}

		var vms []core.Target
		var backends []string
		if names := core.NodeBackends(s, hvFlag); settingsErr == nil && len(names) > 0 {
			inv, err := core.LoadInventory(s, names, "")
			if err != nil {
				fail(core.ErrHypervisorInit, "%s", err)
				noteServiceAccount()
			} else {
				vms, backends = inv.All(), inv.Backends()
				for _, w := range inv.Warnings() {
					fail(core.ErrHypervisorInit, "%s", w)
				}
				noteInventoryProblems(inv)
			}
		}
		wg.Wait()

		fmt.Printf("Node: %s\n\n", s.Node())
		total := 0
		for _, b := range backends {
			var section []core.Target
			for _, t := range vms {
				if t.Backend == b {
					section = append(section, t)
				}
			}
			if len(section) == 0 {
				continue
			}
			fmt.Println(core.BackendLabel(b))
			fmt.Println(strings.Repeat("-", 52))
			for _, t := range section {
				state := "off"
				if t.Running {
					state = "running"
				}
				fmt.Printf("  %-30s %-12s %s\n", t.Name, t.Folder, state)
			}
			total += len(section)
			fmt.Println()
		}

		if awsErr != nil {
			core.LogError(core.ErrAWS, "", "AWS: %s", awsErr)
		}
		if len(awsInsts) > 0 {
			fmt.Println("AWS EC2")
			fmt.Println(strings.Repeat("-", 52))
			for _, i := range awsInsts {
				name := i.Name
				if name == "" {
					name = i.ID
				}
				fmt.Printf("  %-30s %-12s %s\n", name, s.AWSRegion, i.State)
			}
			total += len(awsInsts)
			fmt.Println()
		}

		if total == 0 {
			fmt.Println("No VMs detected across any provider.")
		} else {
			fmt.Printf("Total: %d VM(s)\n", total)
		}
	},
}

// ---------------------------------------------------------------------------
// info
// ---------------------------------------------------------------------------

var infoCmd = &cobra.Command{
	Use:   "info [vm-names...]",
	Short: "Show detailed VM info",
	Run: func(cmd *cobra.Command, args []string) {
		s := loadSettings()
		targets := resolveTargets(args, allIfNone)
		core.SortTargets(targets)
		pvnNames := core.LoadPVNNames(s.VmInventory)
		for i, t := range targets {
			fmt.Printf("%s [%s]\n", t.Name, onOff(t.Running))
			switch t.Backend {
			case "workstation":
				printVMXInfo(t, pvnNames)
			case "vbox":
				printVBoxInfo(t)
			default:
				fmt.Printf("└── %s · folder %s\n", core.BackendLabel(t.Backend), t.Folder)
			}
			if i < len(targets)-1 {
				fmt.Println()
			}
		}
	},
}

// printVMXInfo prints the hardware sections of a VMware VM.
func printVMXInfo(t core.Target, pvnNames map[string]string) {
	showAll := !netFlag && !specsFlag && !diskFlag && !cdromFlag && !displayFlag
	hasPriorSection := false
	section := func(title string) string {
		if hasPriorSection {
			fmt.Printf("└── %s\n", title)
			return "    "
		}
		return ""
	}
	branch := func(indent string, k, n int) string {
		if k == n-1 {
			return indent + "└──"
		}
		return indent + "├──"
	}

	if showAll || specsFlag {
		specs, err := core.ParseVMXSpecs(t.Path)
		if err != nil {
			core.LogError(core.ErrVMXRead, t.Name, "error reading specs")
		} else {
			fmt.Printf("├── CPU: %s vCPUs (%s sockets x %s cores)\n", specs.CPUCount, specs.Sockets, specs.CoresPerSocket)
			fmt.Printf("├── RAM: %s MB\n", specs.MemoryMB)
			fmt.Printf("├── Nested Virtualization: %s\n", specs.NestedVirt)
			fmt.Printf("├── Performance Counters: %s\n", specs.PerfCounters)
		}
		hasPriorSection = true
	}

	if showAll || netFlag {
		nics, err := core.ParseVMXNetworking(t.Path, pvnNames)
		if err != nil {
			core.LogError(core.ErrVMXRead, t.Name, "error reading NICs")
		} else {
			indent := section("Network")
			for k, nic := range nics {
				fmt.Printf("%s NIC %s (%s)  MAC: %s\n", branch(indent, k, len(nics)), nic.Index, nic.Type, nic.MAC)
			}
		}
		hasPriorSection = true
	}

	if showAll || diskFlag {
		disks, err := core.ParseVMXDisks(t.Path)
		if err != nil {
			core.LogError(core.ErrVMXRead, t.Name, "error reading disks")
		} else {
			indent := section("Disks")
			for k, d := range disks {
				fmt.Printf("%s %s%s:%s [%s] %s (%s)\n", branch(indent, k, len(disks)), d.Controller, d.Index, d.Slot, d.SizeGB, d.FileName, d.DiskType)
			}
		}
		hasPriorSection = true
	}

	if showAll || cdromFlag {
		drives, err := core.ParseVMXCDDrives(t.Path)
		if err != nil {
			core.LogError(core.ErrVMXRead, t.Name, "error reading CD/DVD drives")
		} else if len(drives) > 0 {
			indent := section("CD/DVD")
			for k, d := range drives {
				conn := "connect at boot: yes"
				if d.StartConnected == "FALSE" {
					conn = "connect at boot: no"
				}
				fmt.Printf("%s %s:%s [%s] %s (%s)\n", branch(indent, k, len(drives)), d.Controller, d.Slot, d.DeviceType, d.FileName, conn)
			}
		}
		hasPriorSection = true
	}

	if showAll || displayFlag {
		disp, err := core.ParseVMXDisplay(t.Path)
		if err != nil {
			core.LogError(core.ErrVMXRead, t.Name, "error reading display")
		} else {
			indent := section("Display")
			fmt.Printf("%s├── 3D Acceleration: %s\n", indent, disp.Accelerated3D)
			fmt.Printf("%s└── Graphics Memory: %s MB\n", indent, disp.GraphicsMemoryMB)
		}
	}
}

// printVBoxInfo prints CPU, RAM, NICs and disks of a VirtualBox VM.
func printVBoxInfo(t core.Target) {
	vb, ok := t.HV.(*vbox.VBoxBackend)
	if !ok {
		return
	}
	info, err := vb.GetVMInfo(t.Path)
	if err != nil {
		core.LogError(core.ErrConfig, t.Name, "%s", err)
		return
	}
	fmt.Printf("├── VirtualBox · folder %s · OS %s\n", t.Folder, info.OSType)
	fmt.Printf("├── CPU: %d vCPUs\n", info.CPUs)
	fmt.Printf("├── RAM: %d MB\n", info.MemoryMB)
	fmt.Println("└── Network / Disks")
	for _, nic := range info.NICs {
		fmt.Printf("    ├── NIC %d (%s, %s)\n", nic.Index, nic.Type, nic.NICType)
	}
	for k, d := range info.Disks {
		prefix := "├──"
		if k == len(info.Disks)-1 {
			prefix = "└──"
		}
		fmt.Printf("    %s %s\n", prefix, d)
	}
}

// ---------------------------------------------------------------------------
// networks / isos / hwinfo / errors
// ---------------------------------------------------------------------------

var networksCmd = &cobra.Command{
	Use:   "networks",
	Short: "List available virtual networks and LAN segments",
	Run: func(cmd *cobra.Command, args []string) {
		s := loadSettings()
		networks, err := core.LoadVirtualNetworks(s.NetmapPath)
		exitOnErr(err)
		fmt.Println("Virtual Networks:")
		for _, n := range networks {
			fmt.Printf("  %s - %s (%s)\n", n.Device, n.Name, n.Type)
		}
		if pvnNames := core.LoadPVNNames(s.VmInventory); len(pvnNames) > 0 {
			fmt.Println("\nLAN Segments:")
			for _, name := range pvnNames {
				fmt.Printf("  %s\n", name)
			}
		}
	},
}

var isosCmd = &cobra.Command{
	Use:   "isos",
	Short: "List available ISO images",
	Run: func(cmd *cobra.Command, args []string) {
		isos, err := core.ListISOs(loadSettings().IsoDirectory)
		exitOnErr(err)
		if len(isos) == 0 {
			fmt.Println("No ISO files found")
			return
		}
		fmt.Println("Available ISOs:")
		for _, iso := range isos {
			fmt.Printf("  %s\n", iso)
		}
	},
}

var hwinfoCmd = &cobra.Command{
	Use:   "hwinfo",
	Short: "Show detected host hardware resources",
	Run: func(cmd *cobra.Command, args []string) {
		host, err := core.DetectHostResources()
		if err != nil {
			fail(core.ErrConfig, "%s", err)
			return
		}
		fmt.Printf("CPU: %d cores, %d logical processors\n", host.CPUCores, host.CPUThreads)
		fmt.Printf("RAM: %d GB\n", host.TotalRAMGB)
		if len(host.FreeDiskGB) > 0 {
			fmt.Println("Disk:")
			drives := make([]string, 0, len(host.FreeDiskGB))
			for d := range host.FreeDiskGB {
				drives = append(drives, d)
			}
			sort.Strings(drives)
			for _, d := range drives {
				fmt.Printf("  %s %d GB free\n", d, host.FreeDiskGB[d])
			}
		}
	},
}

var errorsCmd = &cobra.Command{
	Use:   "errors",
	Short: "List all error codes and their descriptions",
	Run: func(cmd *cobra.Command, args []string) {
		categories := []struct{ prefix, label string }{
			{"VM1", "Power Operations"},
			{"VM2", "Config Operations"},
			{"VM3", "Exec/Guest Operations"},
			{"VM4", "Snapshot Operations"},
			{"VM5", "Archive Operations"},
			{"VM6", "Environment/Settings/Targets"},
			{"VM7", "File/VMX Operations"},
			{"VM8", "AWS / Hypervisor Backends"},
			{"VM9", "Migration Operations"},
		}
		byPrefix := map[string][]core.ErrorRef{}
		for _, e := range core.ErrorCodes {
			byPrefix[e.Code[:3]] = append(byPrefix[e.Code[:3]], e)
		}
		for i, cat := range categories {
			if i > 0 {
				fmt.Println()
			}
			fmt.Printf("%sxx  %s\n", cat.prefix, cat.label)
			for _, e := range byPrefix[cat.prefix] {
				fmt.Printf("  %-6s  %s\n", e.Code, e.Desc)
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(listCmd, overviewCmd, infoCmd, networksCmd, isosCmd, hwinfoCmd, errorsCmd)
	addFolderFlag(listCmd, infoCmd)

	infoCmd.Flags().BoolVarP(&netFlag, "net", "n", false, "Show NIC details only")
	infoCmd.Flags().BoolVarP(&specsFlag, "specs", "s", false, "Show CPU/RAM details only")
	infoCmd.Flags().BoolVarP(&diskFlag, "disk", "d", false, "Show disk details only")
	infoCmd.Flags().BoolVarP(&cdromFlag, "cdrom", "C", false, "Show CD/DVD details only")
	infoCmd.Flags().BoolVarP(&displayFlag, "display", "D", false, "Show display details only")
}
