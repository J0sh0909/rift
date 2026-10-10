package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/J0sh0909/rift/internal/core"
	"github.com/J0sh0909/rift/internal/vbox"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// VBox flags
// ---------------------------------------------------------------------------

var (
	vboxHardFlag bool
	vboxYesFlag  bool
)

// ---------------------------------------------------------------------------
// Lazy VBox backend
// ---------------------------------------------------------------------------

var vboxBackend *vbox.VBoxBackend

func requireVBox() {
	if vboxBackend != nil {
		return
	}
	var err error
	vboxBackend, err = newVBox()
	if err != nil {
		core.LogError(core.ErrConfig, "", "%s", err)
		exit(1)
	}
}

// newVBox opens VirtualBox using VBOX_MANAGE_PATH from .env when set
// (VBoxManage is often not on PATH on Windows), else PATH.
func newVBox() (*vbox.VBoxBackend, error) {
	_, _ = core.LoadSettings() // loads .env into the environment; .env is optional here
	return vbox.NewVBoxBackend(os.Getenv("VBOX_MANAGE_PATH"))
}

// ---------------------------------------------------------------------------
// rift vbox
// ---------------------------------------------------------------------------

var vboxCmd = &cobra.Command{
	Use:   "vbox",
	Short: "Manage VirtualBox VMs",
}

// ---------------------------------------------------------------------------
// rift vbox list
// ---------------------------------------------------------------------------

var vboxListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all VirtualBox VMs",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		requireVBox()
		vms, err := vboxBackend.ListVMs()
		if err != nil {
			core.LogError(core.ErrConfig, "", "%s", err)
			exit(1)
		}
		if len(vms) == 0 {
			fmt.Println("No VirtualBox VMs found.")
			return
		}
		fmt.Printf("%-30s %s\n", "NAME", "STATE")
		fmt.Println(strings.Repeat("-", 45))
		for _, vm := range vms {
			fmt.Printf("%-30s %s\n", vm.Name, vm.State)
		}
	},
}

// ---------------------------------------------------------------------------
// rift vbox start
// ---------------------------------------------------------------------------

var vboxStartCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Start a VirtualBox VM (headless)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requireVBox()
		if err := vboxBackend.DirectStartVM(args[0]); err != nil {
			core.LogError(core.ErrStartFailed, args[0], "%s", err)
			exit(1)
		}
		fmt.Printf("%s → started\n", args[0])
	},
}

// ---------------------------------------------------------------------------
// rift vbox stop
// ---------------------------------------------------------------------------

var vboxStopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "Stop a VirtualBox VM",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requireVBox()
		if err := vboxBackend.DirectStopVM(args[0], vboxHardFlag); err != nil {
			core.LogError(core.ErrStopFailed, args[0], "%s", err)
			exit(1)
		}
		fmt.Printf("%s → stopped\n", args[0])
	},
}

// ---------------------------------------------------------------------------
// rift vbox info
// ---------------------------------------------------------------------------

var vboxInfoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Show VirtualBox VM details",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requireVBox()
		info, err := vboxBackend.GetVMInfo(args[0])
		if err != nil {
			core.LogError(core.ErrConfig, args[0], "%s", err)
			exit(1)
		}
		fmt.Printf("Name:     %s\n", info.Name)
		fmt.Printf("UUID:     %s\n", info.UUID)
		fmt.Printf("OS Type:  %s\n", info.OSType)
		fmt.Printf("State:    %s\n", info.State)
		fmt.Printf("CPUs:     %d\n", info.CPUs)
		fmt.Printf("Memory:   %d MB\n", info.MemoryMB)
		if len(info.NICs) > 0 {
			fmt.Println("NICs:")
			for _, nic := range info.NICs {
				fmt.Printf("  NIC %d: %s\n", nic.Index, nic.Type)
			}
		}
		if len(info.Disks) > 0 {
			fmt.Println("Disks:")
			for _, d := range info.Disks {
				fmt.Printf("  %s\n", d)
			}
		}
	},
}

// ---------------------------------------------------------------------------
// rift vbox snapshot
// ---------------------------------------------------------------------------

var vboxSnapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Manage VirtualBox snapshots",
}

var vboxSnapCreateCmd = &cobra.Command{
	Use:   "create <vm-name> <snapshot-name>",
	Short: "Take a snapshot",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		requireVBox()
		if err := vboxBackend.SnapshotCreate(args[0], args[1]); err != nil {
			core.LogError(core.ErrSnapCreate, args[0], "%s", err)
			exit(1)
		}
		fmt.Printf("%s → snapshot '%s' created\n", args[0], args[1])
	},
}

var vboxSnapListCmd = &cobra.Command{
	Use:   "list <vm-name>",
	Short: "List snapshots",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requireVBox()
		snaps, err := vboxBackend.SnapshotList(args[0])
		if err != nil {
			core.LogError(core.ErrSnapshot, args[0], "%s", err)
			exit(1)
		}
		if len(snaps) == 0 {
			fmt.Printf("%s → no snapshots\n", args[0])
			return
		}
		for _, s := range snaps {
			fmt.Printf("  %s\n", s)
		}
	},
}

var vboxSnapRevertCmd = &cobra.Command{
	Use:   "revert <vm-name> <snapshot-name>",
	Short: "Restore a snapshot",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		requireVBox()
		if err := vboxBackend.SnapshotRevert(args[0], args[1]); err != nil {
			core.LogError(core.ErrSnapRevert, args[0], "%s", err)
			exit(1)
		}
		fmt.Printf("%s → reverted to '%s'\n", args[0], args[1])
	},
}

var vboxSnapDeleteCmd = &cobra.Command{
	Use:   "delete <vm-name> <snapshot-name>",
	Short: "Delete a snapshot",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		requireVBox()
		if err := vboxBackend.SnapshotDelete(args[0], args[1]); err != nil {
			core.LogError(core.ErrSnapDelete, args[0], "%s", err)
			exit(1)
		}
		fmt.Printf("%s → snapshot '%s' deleted\n", args[0], args[1])
	},
}

// ---------------------------------------------------------------------------
// rift vbox delete
// ---------------------------------------------------------------------------

var vboxDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a VirtualBox VM",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requireVBox()
		if !vboxYesFlag {
			fmt.Fprintf(os.Stderr, "error: --yes is required to confirm deletion\n")
			exit(1)
		}
		if err := vboxBackend.DeleteVM(args[0]); err != nil {
			core.LogError(core.ErrConfig, args[0], "%s", err)
			exit(1)
		}
		fmt.Printf("%s → deleted\n", args[0])
	},
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func init() {
	rootCmd.AddCommand(vboxCmd)
	vboxCmd.AddCommand(vboxListCmd)
	vboxCmd.AddCommand(vboxStartCmd)
	vboxCmd.AddCommand(vboxStopCmd)
	vboxCmd.AddCommand(vboxInfoCmd)
	vboxCmd.AddCommand(vboxSnapshotCmd)
	vboxCmd.AddCommand(vboxDeleteCmd)

	vboxSnapshotCmd.AddCommand(vboxSnapCreateCmd)
	vboxSnapshotCmd.AddCommand(vboxSnapListCmd)
	vboxSnapshotCmd.AddCommand(vboxSnapRevertCmd)
	vboxSnapshotCmd.AddCommand(vboxSnapDeleteCmd)

	vboxStopCmd.Flags().BoolVarP(&vboxHardFlag, "hard", "H", false, "Force power off")
	vboxDeleteCmd.Flags().BoolVarP(&vboxYesFlag, "yes", "y", false, "Confirm deletion")
}
