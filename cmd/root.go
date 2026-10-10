// Package cmd implements the rift CLI.
//
// File layout:
//
//	root.go      root command, shared flags, settings
//	targets.go   node inventory and target resolution (--folder / names)
//	engine.go    parallel execution, retries, reporting, exit status
//	pipeline.go  dispatch (CI entry point), step summaries, version
//	power.go     start / stop / suspend / reset / restart
//	guest.go     exec / ip
//	bootstrap.go bootstrap create / verify / reset / revoke
//	snapshot.go  snapshot create / list / revert / delete
//	archive.go   archive export / import / list / delete
//	config.go    config cpu / ram / display / os / tpm / nic / disk / cdrom
//	inspect.go   list / overview / info / networks / isos / hwinfo / errors
package cmd

import (
	"fmt"
	"os"
	"sync"

	"github.com/J0sh0909/rift/internal/core"
	"github.com/spf13/cobra"

	// Blank imports register backend factories via init().
	_ "github.com/J0sh0909/rift/internal/aws"
	_ "github.com/J0sh0909/rift/internal/hyperv"
	_ "github.com/J0sh0909/rift/internal/proxmox"
	_ "github.com/J0sh0909/rift/internal/vbox"
	_ "github.com/J0sh0909/rift/internal/vmware"
)

// Flags shared by several commands.
var (
	folderFlag string // --folder/-f: comma-separated logical folders
	hvFlag     string // --hv: restrict to one hypervisor backend
	vpFlag     string // --vp: VM encryption password
	yesFlag    bool   // --yes/-y: skip confirmation prompts
)

var rootCmd = &cobra.Command{
	Use:   "rift",
	Short: "Cross-hypervisor VM manager (VMware Workstation, VirtualBox, Hyper-V, AWS)",
	Long: `rift manages VMs across every hypervisor installed on this node.

Targets are VM names or a logical folder (-f DEV). A folder spans all
hypervisors on the node: VMware inventory folders, VirtualBox groups, and
Hyper-V VM groups or a "rift-folder: NAME" line in the VM notes. Run through
the CI pipeline ("rift dispatch"), the same command reaches every node.`,
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		finish()
	},
}

// Execute runs the CLI.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		exit(1) // cobra has already printed the error
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&vpFlag, "vp", "", "VM encryption password (overrides VM_ENCRYPTION_PASS from .env)")
	rootCmd.PersistentFlags().StringVar(&hvFlag, "hv", "", "Hypervisor backend: workstation, vbox, hyperv, proxmox (default: every backend on this node)")
}

// addFolderFlag registers --folder/-f on commands that accept targets.
func addFolderFlag(cmds ...*cobra.Command) {
	for _, c := range cmds {
		c.Flags().StringVarP(&folderFlag, "folder", "f", "", "Apply to all VMs in a folder on every hypervisor (comma-separated for several)")
	}
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

var (
	settings     core.Settings
	settingsOnce sync.Once
	settingsErr  error
)

// loadSettings loads .env once (applying --vp) and exits on error.
func loadSettings() core.Settings {
	settingsOnce.Do(func() {
		settings, settingsErr = core.LoadSettings()
		if settingsErr == nil && vpFlag != "" {
			settings.EncryptionPass = vpFlag
		}
	})
	exitOnErr(settingsErr)
	return settings
}

var (
	defaultHV     core.Hypervisor
	defaultHVOnce sync.Once
	defaultHVErr  error
)

// defaultBackend returns the single preferred backend (--hv, HYPERVISOR, or
// the only one detected) for operations that are not per-VM, like importing
// an archive.
func defaultBackend() core.Hypervisor {
	s := loadSettings()
	defaultHVOnce.Do(func() {
		defaultHV, defaultHVErr = core.NewHypervisor(s, hvFlag)
	})
	exitOnErr(defaultHVErr)
	return defaultHV
}

// exitOnErr prints err and exits 1 when err is non-nil.
func exitOnErr(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		exit(1)
	}
}
