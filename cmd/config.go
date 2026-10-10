package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/J0sh0909/rift/internal/core"
	"github.com/spf13/cobra"
)

var (
	coresFlag             int
	socketsFlag           int
	ramFlag               int
	nicIndexFlag          int
	nicTypeFlag           string
	regenMacFlag          bool
	addNicFlag            bool
	removeNicFlag         bool
	vnetFlag              string
	diskSizeFlag          int
	diskTypeFlag          int
	diskControllerFlag    string
	diskSlotFlag          int
	deleteFilesFlag       bool
	isoFlag               string
	cdromControllerFlag   string
	cdromSlotFlag         int
	bootConnectFlag       bool
	noBootConnectFlag     bool
	accel3dFlag           string
	gfxMemFlag            int
	osSetFlag             string
	tpmEncryptionPassFlag string
	tpmEncryptAllFlag     bool
)

// configure runs a VMX edit over targets: VMware Workstation VMs only, one
// at a time, and (when powerKey is set) only while the VM is powered off.
func configure(verb string, args []string, powerKey, action string, edit func(core.Target) result) {
	targets := resolveTargets(args, needTargets)
	p := plan{verb: verb, sequential: true}
	report(p, run(targets, p, func(t core.Target) result {
		if r, ok := vmxOnly(t); !ok {
			return r
		}
		if powerKey != "" {
			if r, ok := requireOff(t, powerKey, action); !ok {
				return r
			}
		}
		return edit(t)
	}))
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Modify VM settings",
}

var configCpuCmd = &cobra.Command{
	Use:   "cpu [vm-names...]",
	Short: "Change CPU settings",
	Run: func(cmd *cobra.Command, args []string) {
		if coresFlag == 0 || socketsFlag == 0 {
			fail(core.ErrCPUConfig, "both --cores and --sockets are required")
			return
		}
		host, _ := core.DetectHostResources()
		if err := core.ValidateCPU(host, socketsFlag, coresFlag); err != nil {
			fail(core.ErrCPUConfig, "%s", err)
			return
		}
		totalCPUs := coresFlag * socketsFlag
		configure("config cpu", args, "cpu", "change CPU", func(t core.Target) result {
			if err := core.SetVMXKey(t.Path, "numvcpus", fmt.Sprintf("%d", totalCPUs)); err != nil {
				return failed(t, core.ErrCPUConfig, "%s", err)
			}
			if err := core.SetVMXKey(t.Path, "cpuid.coresPerSocket", fmt.Sprintf("%d", coresFlag)); err != nil {
				return failed(t, core.ErrCPUConfig, "%s", err)
			}
			return success(t, "CPU: %d vCPUs (%d sockets x %d cores)", totalCPUs, socketsFlag, coresFlag)
		})
	},
}

// RAM: GB input, stored as MB in the VMX.
var configRamCmd = &cobra.Command{
	Use:   "ram [vm-names...]",
	Short: "Change RAM size (in GB)",
	Run: func(cmd *cobra.Command, args []string) {
		if ramFlag == 0 {
			fail(core.ErrRAMConfig, "--size is required")
			return
		}
		host, _ := core.DetectHostResources()
		if err := core.ValidateRAM(host, ramFlag); err != nil {
			fail(core.ErrRAMConfig, "%s", err)
			return
		}
		memMB := core.GBtoMB(ramFlag)
		configure("config ram", args, "ram", "change RAM", func(t core.Target) result {
			if err := core.SetVMXKey(t.Path, "memsize", fmt.Sprintf("%d", memMB)); err != nil {
				return failed(t, core.ErrRAMConfig, "%s", err)
			}
			return success(t, "RAM: %d GB (%d MB)", ramFlag, memMB)
		})
	},
}

// Display: gfxmem takes MB, stored as KB.
var configDisplayCmd = &cobra.Command{
	Use:   "display [vm-names...]",
	Short: "Change display settings",
	Run: func(cmd *cobra.Command, args []string) {
		if accel3dFlag == "" && gfxMemFlag == 0 {
			fail(core.ErrDisplayConfig, "specify --accel3d (on/off) and/or --gfxmem (MB)")
			return
		}
		if gfxMemFlag > 0 {
			host, _ := core.DetectHostResources()
			if err := core.ValidateGfxMem(host, gfxMemFlag); err != nil {
				fail(core.ErrDisplayConfig, "%s", err)
				return
			}
		}
		configure("config display", args, "display", "change display", func(t core.Target) result {
			var changes []string
			if accel3dFlag != "" {
				val := "FALSE"
				if strings.EqualFold(accel3dFlag, "on") {
					val = "TRUE"
				}
				if err := core.SetVMXKey(t.Path, "mks.enable3d", val); err != nil {
					return failed(t, core.ErrDisplayConfig, "%s", err)
				}
				changes = append(changes, "3D Acceleration: "+accel3dFlag)
			}
			if gfxMemFlag > 0 {
				kb := core.MBtoKB(gfxMemFlag)
				if err := core.SetVMXKey(t.Path, "svga.graphicsMemoryKB", fmt.Sprintf("%d", kb)); err != nil {
					return failed(t, core.ErrDisplayConfig, "%s", err)
				}
				changes = append(changes, fmt.Sprintf("Graphics Memory: %d MB (%d KB)", gfxMemFlag, kb))
			}
			return success(t, "%s", strings.Join(changes, ", "))
		})
	},
}

var configOsCmd = &cobra.Command{
	Use:   "os [vm-names...]",
	Short: "Get or set the guestOS VMX key",
	Run: func(cmd *cobra.Command, args []string) {
		if osSetFlag != "" {
			configure("config os", args, "", "", func(t core.Target) result {
				if t.Running {
					return skipped(t, core.ErrOSConfig, "must be powered off to change guestOS")
				}
				if err := core.SetVMXKey(t.Path, "guestOS", osSetFlag); err != nil {
					return failed(t, core.ErrOSConfig, "%s", err)
				}
				return success(t, "guestOS: %s", osSetFlag)
			})
			return
		}
		// Reading works on every backend: VMware's VMX key, VirtualBox's
		// ostype, Hyper-V's integration services OS name.
		targets := resolveTargets(args, needTargets)
		p := plan{verb: "config os"}
		report(p, run(targets, p, func(t core.Target) result {
			guestOS := guestOSOf(t)
			if guestOS == "" {
				return success(t, "guestOS: (not set) - %s", osHint(t))
			}
			return success(t, "guestOS: %s", guestOS)
		}))
	},
}

var configTpmCmd = &cobra.Command{
	Use:   "tpm [vm-names...]",
	Short: "Show TPM status for a VM",
	Run: func(cmd *cobra.Command, args []string) {
		configure("config tpm", args, "", "", func(t core.Target) result {
			data, err := core.ParseVMXKeys(t.Path)
			if err != nil {
				return failed(t, core.ErrTPMConfig, "%s", err)
			}
			if !strings.EqualFold(data["vtpm.present"], "TRUE") {
				return success(t, "TPM: not present")
			}
			encType := "select files"
			if strings.EqualFold(data["encryption.encryptedvmx"], "TRUE") {
				encType = "full VM"
			}
			return success(t, "TPM: present (encryption: %s)", encType)
		})
	},
}

var configTpmAddCmd = &cobra.Command{
	Use:   "add [vm-names...]",
	Short: "Add a virtual TPM to a VM (requires encryption)",
	Run: func(cmd *cobra.Command, args []string) {
		if tpmEncryptionPassFlag == "" {
			fail(core.ErrTPMConfig, "--encryption-pass is required (VMware requires encryption for TPM)")
			return
		}
		configure("config tpm add", args, "tpm", "add a TPM", func(t core.Target) result {
			keys := [][2]string{
				{"managedvm.autoAddVTPM", "software"},
				{"vtpm.present", "TRUE"},
			}
			if tpmEncryptAllFlag {
				keys = append(keys, [2]string{"encryption.encryptedvmx", "TRUE"})
			}
			for _, kv := range keys {
				if err := core.SetVMXKey(t.Path, kv[0], kv[1]); err != nil {
					return failed(t, core.ErrTPMConfig, "%s", err)
				}
			}
			encDesc := "select files"
			if tpmEncryptAllFlag {
				encDesc = "full VM"
			}
			return success(t, "TPM added (encryption: %s, password set via --encryption-pass)", encDesc)
		})
	},
}

var configTpmRemoveCmd = &cobra.Command{
	Use:   "remove [vm-names...]",
	Short: "Remove the virtual TPM from a VM",
	Run: func(cmd *cobra.Command, args []string) {
		configure("config tpm remove", args, "tpm", "remove the TPM", func(t core.Target) result {
			for _, key := range []string{"managedvm.autoAddVTPM", "vtpm.present", "encryption.encryptedvmx"} {
				_ = core.RemoveVMXKey(t.Path, key)
			}
			return success(t, "TPM removed")
		})
	},
}

var configNicCmd = &cobra.Command{
	Use:   "nic [vm-names...]",
	Short: "Change NIC settings",
	Run: func(cmd *cobra.Command, args []string) {
		if !addNicFlag && !removeNicFlag && !regenMacFlag && nicTypeFlag == "" {
			fail(core.ErrNetConfig, "specify an action: --type, --regen-mac, --add, or --remove")
			return
		}
		if addNicFlag && nicTypeFlag == "" {
			fail(core.ErrNetConfig, "--type is required when adding a NIC")
			return
		}
		switch {
		case addNicFlag:
			configure("config nic", args, "nic-add", "add a NIC", func(t core.Target) result {
				index, err := core.AddNIC(t.Path, nicTypeFlag, vnetFlag)
				if err != nil {
					return failed(t, core.ErrNetConfig, "%s", err)
				}
				return success(t, "added NIC %d (%s)", index, nicTypeFlag)
			})
		case removeNicFlag:
			configure("config nic", args, "nic-remove", "remove a NIC", func(t core.Target) result {
				if err := core.RemoveNIC(t.Path, nicIndexFlag); err != nil {
					return failed(t, core.ErrNetConfig, "%s", err)
				}
				return success(t, "removed NIC %d", nicIndexFlag)
			})
		case regenMacFlag:
			configure("config nic", args, "nic-regen", "regenerate the MAC", func(t core.Target) result {
				if err := core.RegenMAC(t.Path, nicIndexFlag); err != nil {
					return failed(t, core.ErrNetConfig, "%s", err)
				}
				return success(t, "NIC %d MAC will regenerate on next boot", nicIndexFlag)
			})
		default:
			configure("config nic", args, "nic-type", "change the NIC type", func(t core.Target) result {
				if err := core.SetNICType(t.Path, nicIndexFlag, nicTypeFlag, vnetFlag); err != nil {
					return failed(t, core.ErrNetConfig, "%s", err)
				}
				return success(t, "NIC %d changed to %s", nicIndexFlag, nicTypeFlag)
			})
		}
	},
}

var configDiskCmd = &cobra.Command{
	Use:   "disk",
	Short: "Manage virtual disks",
}

var configDiskAddCmd = &cobra.Command{
	Use:   "add [vm-names...]",
	Short: "Add a new disk",
	Run: func(cmd *cobra.Command, args []string) {
		if diskSizeFlag == 0 || diskControllerFlag == "" {
			fail(core.ErrDiskConfig, "--size and --controller are required")
			return
		}
		s := loadSettings()
		var host core.HostResources
		if diskTypeFlag&2 != 0 { // pre-allocated: needs the space now
			host, _ = core.DetectHostResources()
		}
		configure("config disk add", args, "disk-add", "add a disk", func(t core.Target) result {
			if diskTypeFlag&2 != 0 {
				if err := core.ValidateDiskSpace(host, t.Path, diskSizeFlag); err != nil {
					return failed(t, core.ErrDiskConfig, "%s", err)
				}
			}
			slot, err := core.CreateDisk(s.VdiskPath, t.Path, diskControllerFlag, diskSizeFlag, diskTypeFlag)
			if err != nil {
				return failed(t, core.ErrDiskConfig, "%s", err)
			}
			return success(t, "added disk at %s (%dGB)", slot, diskSizeFlag)
		})
	},
}

var configDiskRemoveCmd = &cobra.Command{
	Use:   "remove [vm-names...]",
	Short: "Remove a disk",
	Run: func(cmd *cobra.Command, args []string) {
		if diskControllerFlag == "" {
			fail(core.ErrDiskConfig, "--controller is required")
			return
		}
		configure("config disk remove", args, "disk-remove", "remove a disk", func(t core.Target) result {
			if err := core.RemoveDisk(t.Path, diskControllerFlag, diskSlotFlag, deleteFilesFlag); err != nil {
				return failed(t, core.ErrDiskConfig, "%s", err)
			}
			return success(t, "removed disk %s:%d", diskControllerFlag, diskSlotFlag)
		})
	},
}

var configDiskExpandCmd = &cobra.Command{
	Use:   "expand [vm-names...]",
	Short: "Expand a disk",
	Run: func(cmd *cobra.Command, args []string) {
		if diskSizeFlag == 0 || diskControllerFlag == "" {
			fail(core.ErrDiskConfig, "--size and --controller are required")
			return
		}
		s := loadSettings()
		configure("config disk expand", args, "disk-expand", "expand a disk", func(t core.Target) result {
			if err := core.ExpandDisk(s.VdiskPath, t.Path, diskControllerFlag, diskSlotFlag, diskSizeFlag); err != nil {
				return failed(t, core.ErrDiskConfig, "%s", err)
			}
			return success(t, "expanded %s:%d to %dGB", diskControllerFlag, diskSlotFlag, diskSizeFlag)
		})
	},
}

var configDiskDefragCmd = &cobra.Command{
	Use:   "defrag [vm-names...]",
	Short: "Defragment a disk",
	Run: func(cmd *cobra.Command, args []string) {
		if diskControllerFlag == "" {
			fail(core.ErrDiskConfig, "--controller is required")
			return
		}
		s := loadSettings()
		configure("config disk defrag", args, "disk-defrag", "defragment a disk", func(t core.Target) result {
			if err := core.DefragDisk(s.VdiskPath, t.Path, diskControllerFlag, diskSlotFlag); err != nil {
				return failed(t, core.ErrDiskConfig, "%s", err)
			}
			return success(t, "defragmented %s:%d", diskControllerFlag, diskSlotFlag)
		})
	},
}

var configDiskCompactCmd = &cobra.Command{
	Use:   "compact [vm-names...]",
	Short: "Compact a disk",
	Run: func(cmd *cobra.Command, args []string) {
		if diskControllerFlag == "" {
			fail(core.ErrDiskConfig, "--controller is required")
			return
		}
		s := loadSettings()
		configure("config disk compact", args, "disk-compact", "compact a disk", func(t core.Target) result {
			if err := core.CompactDisk(s.VdiskPath, t.Path, diskControllerFlag, diskSlotFlag); err != nil {
				return failed(t, core.ErrDiskConfig, "%s", err)
			}
			return success(t, "compacted %s:%d", diskControllerFlag, diskSlotFlag)
		})
	},
}

var configCdromCmd = &cobra.Command{
	Use:   "cdrom",
	Short: "Manage CD/DVD drives",
}

var configCdromMountCmd = &cobra.Command{
	Use:   "mount [vm-names...]",
	Short: "Mount an ISO image",
	Run: func(cmd *cobra.Command, args []string) {
		if isoFlag == "" || cdromControllerFlag == "" {
			fail(core.ErrCDVDConfig, "--iso and --controller are required")
			return
		}
		s := loadSettings()
		isoPath := filepath.Join(s.IsoDirectory, isoFlag)
		if _, err := os.Stat(isoPath); os.IsNotExist(err) {
			fail(core.ErrInvalidPath, "ISO '%s' not found in %s", isoFlag, s.IsoDirectory)
			return
		}
		configure("config cdrom mount", args, "", "", func(t core.Target) result {
			if err := core.MountISO(t.Path, cdromControllerFlag, cdromSlotFlag, isoPath); err != nil {
				return failed(t, core.ErrCDVDConfig, "%s", err)
			}
			return success(t, "mounted %s on %s:%d", isoFlag, cdromControllerFlag, cdromSlotFlag)
		})
	},
}

var configCdromUnmountCmd = &cobra.Command{
	Use:   "unmount [vm-names...]",
	Short: "Unmount ISO and set to auto detect",
	Run: func(cmd *cobra.Command, args []string) {
		if cdromControllerFlag == "" {
			fail(core.ErrCDVDConfig, "--controller is required")
			return
		}
		configure("config cdrom unmount", args, "", "", func(t core.Target) result {
			if err := core.UnmountISO(t.Path, cdromControllerFlag, cdromSlotFlag); err != nil {
				return failed(t, core.ErrCDVDConfig, "%s", err)
			}
			return success(t, "unmounted %s:%d", cdromControllerFlag, cdromSlotFlag)
		})
	},
}

var configCdromBootCmd = &cobra.Command{
	Use:   "boot [vm-names...]",
	Short: "Set connect at power on",
	Run: func(cmd *cobra.Command, args []string) {
		if cdromControllerFlag == "" {
			fail(core.ErrCDVDConfig, "--controller is required")
			return
		}
		if !bootConnectFlag && !noBootConnectFlag {
			fail(core.ErrCDVDConfig, "--on or --off is required")
			return
		}
		connect := bootConnectFlag
		state := "on"
		if !connect {
			state = "off"
		}
		configure("config cdrom boot", args, "", "", func(t core.Target) result {
			if err := core.SetCDROMBoot(t.Path, cdromControllerFlag, cdromSlotFlag, connect); err != nil {
				return failed(t, core.ErrCDVDConfig, "%s", err)
			}
			return success(t, "%s:%d connect at boot: %s", cdromControllerFlag, cdromSlotFlag, state)
		})
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configCpuCmd, configRamCmd, configNicCmd, configDiskCmd, configCdromCmd,
		configDisplayCmd, configOsCmd, configTpmCmd)
	configTpmCmd.AddCommand(configTpmAddCmd, configTpmRemoveCmd)
	configDiskCmd.AddCommand(configDiskAddCmd, configDiskRemoveCmd, configDiskExpandCmd, configDiskDefragCmd, configDiskCompactCmd)
	configCdromCmd.AddCommand(configCdromMountCmd, configCdromUnmountCmd, configCdromBootCmd)

	addFolderFlag(configCpuCmd, configRamCmd, configNicCmd, configDisplayCmd, configOsCmd,
		configTpmCmd, configTpmAddCmd, configTpmRemoveCmd,
		configDiskAddCmd, configDiskRemoveCmd, configDiskExpandCmd, configDiskDefragCmd, configDiskCompactCmd,
		configCdromMountCmd, configCdromUnmountCmd, configCdromBootCmd)

	configCpuCmd.Flags().IntVarP(&coresFlag, "cores", "c", 0, "Cores per socket")
	configCpuCmd.Flags().IntVarP(&socketsFlag, "sockets", "S", 0, "Number of sockets")

	configRamCmd.Flags().IntVarP(&ramFlag, "size", "m", 0, "RAM size in GB")

	configNicCmd.Flags().IntVarP(&nicIndexFlag, "index", "i", 0, "NIC index")
	configNicCmd.Flags().StringVarP(&nicTypeFlag, "type", "t", "", "NIC type (bridged, nat, hostonly, custom)")
	configNicCmd.Flags().StringVarP(&vnetFlag, "vnet", "v", "", "Virtual network name (for custom type)")
	configNicCmd.Flags().BoolVarP(&regenMacFlag, "regen-mac", "r", false, "Regenerate MAC address")
	configNicCmd.Flags().BoolVarP(&addNicFlag, "add", "a", false, "Add a new NIC")
	configNicCmd.Flags().BoolVarP(&removeNicFlag, "remove", "R", false, "Remove a NIC")

	configDiskAddCmd.Flags().IntVarP(&diskSizeFlag, "size", "s", 0, "Disk size in GB")
	configDiskAddCmd.Flags().IntVarP(&diskTypeFlag, "type", "t", 0, "Disk type bitmask: 1=split, 2=pre-allocated (combine: 0=single+growable, 1=split+growable, 2=single+pre-allocated, 3=split+pre-allocated)")
	configDiskAddCmd.Flags().StringVarP(&diskControllerFlag, "controller", "c", "", "Controller (scsi0, sata0, nvme0, ide0)")

	configDiskRemoveCmd.Flags().StringVarP(&diskControllerFlag, "controller", "c", "", "Controller")
	configDiskRemoveCmd.Flags().IntVarP(&diskSlotFlag, "slot", "S", 0, "Disk slot number")
	configDiskRemoveCmd.Flags().BoolVarP(&deleteFilesFlag, "delete-files", "D", false, "Also delete VMDK files")

	configDiskExpandCmd.Flags().IntVarP(&diskSizeFlag, "size", "s", 0, "New disk size in GB")
	configDiskExpandCmd.Flags().StringVarP(&diskControllerFlag, "controller", "c", "", "Controller")
	configDiskExpandCmd.Flags().IntVarP(&diskSlotFlag, "slot", "S", 0, "Disk slot number")

	configDiskDefragCmd.Flags().StringVarP(&diskControllerFlag, "controller", "c", "", "Controller")
	configDiskDefragCmd.Flags().IntVarP(&diskSlotFlag, "slot", "S", 0, "Disk slot number")

	configDiskCompactCmd.Flags().StringVarP(&diskControllerFlag, "controller", "c", "", "Controller")
	configDiskCompactCmd.Flags().IntVarP(&diskSlotFlag, "slot", "S", 0, "Disk slot number")

	configCdromMountCmd.Flags().StringVarP(&isoFlag, "iso", "i", "", "ISO filename from ISO directory")
	configCdromMountCmd.Flags().StringVarP(&cdromControllerFlag, "controller", "c", "", "Controller (e.g. sata0)")
	configCdromMountCmd.Flags().IntVarP(&cdromSlotFlag, "slot", "S", 0, "Slot number")

	configCdromUnmountCmd.Flags().StringVarP(&cdromControllerFlag, "controller", "c", "", "Controller")
	configCdromUnmountCmd.Flags().IntVarP(&cdromSlotFlag, "slot", "S", 0, "Slot number")

	configCdromBootCmd.Flags().StringVarP(&cdromControllerFlag, "controller", "c", "", "Controller")
	configCdromBootCmd.Flags().IntVarP(&cdromSlotFlag, "slot", "S", 0, "Slot number")
	configCdromBootCmd.Flags().BoolVar(&bootConnectFlag, "on", false, "Connect at power on")
	configCdromBootCmd.Flags().BoolVar(&noBootConnectFlag, "off", false, "Don't connect at power on")

	configDisplayCmd.Flags().StringVarP(&accel3dFlag, "accel3d", "a", "", "3D acceleration (on/off)")
	configDisplayCmd.Flags().IntVarP(&gfxMemFlag, "gfxmem", "g", 0, "Graphics memory in MB")

	configOsCmd.Flags().StringVarP(&osSetFlag, "set", "s", "", "guestOS value to write into the VMX")

	configTpmAddCmd.Flags().StringVar(&tpmEncryptionPassFlag, "encryption-pass", "", "VM encryption password (required)")
	configTpmAddCmd.Flags().BoolVar(&tpmEncryptAllFlag, "encrypt-all", false, "Encrypt all VM files (default: select files only)")
}
