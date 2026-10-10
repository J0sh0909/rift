package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/J0sh0909/rift/internal/core"
	"github.com/spf13/cobra"
)

var (
	snapshotNameFlag    string
	snapshotOriginFlag  bool
	snapshotCurrentFlag bool
)

// snapshotPlan runs snapshot operations one VM at a time per hypervisor:
// they are heavy, and concurrent vmrun snapshot calls are unreliable.
var snapshotPlan = plan{verb: "snapshot", sequential: true}

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Manage VM snapshots",
}

var snapshotCreateCmd = &cobra.Command{
	Use:   "create [vm-names...]",
	Short: "Create a snapshot of one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		if snapshotNameFlag != "" {
			if err := core.ValidateVMName(snapshotNameFlag); err != nil {
				fail(core.ErrSnapCreate, "invalid snapshot name: %s", err)
				return
			}
		}
		targets := resolveTargets(args, needTargets)
		report(snapshotPlan, run(targets, snapshotPlan, func(t core.Target) result {
			name := snapshotNameFlag
			if name == "" {
				name = t.Name + "-" + time.Now().Format("20060102-150405")
			}

			if existing, err := t.HV.ListSnapshots(t.Path); err == nil {
				for _, s := range existing {
					if s == name {
						return skipped(t, core.ErrSnapExists, "snapshot %q already exists", name)
					}
				}
			}

			if t.Running {
				if err := t.HV.SuspendVM(t.Path); err != nil {
					return failed(t, core.ErrSuspendFailed, "suspending before snapshot: %s", err)
				}
			}
			if err := t.HV.CreateSnapshot(t.Path, name); err != nil {
				if t.Running {
					_ = t.HV.StartVM(t.Path) // try to resume even if the snapshot failed
				}
				return failed(t, core.ErrSnapCreate, "%s", err)
			}
			if t.Running {
				if err := t.HV.StartVM(t.Path); err != nil {
					return failed(t, core.ErrSnapCreate, "snapshot %q created but failed to resume: %s", name, err)
				}
				return success(t, "snapshot %q created (suspended and resumed)", name)
			}
			return success(t, "snapshot %q created", name)
		}))
	},
}

var snapshotListCmd = &cobra.Command{
	Use:   "list [vm-names...]",
	Short: "List snapshots for one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		targets := resolveTargets(args, needTargets)
		p := plan{verb: "snapshot list", render: renderSnapshotTrees}
		report(p, run(targets, p, func(t core.Target) result {
			snapshots, err := t.HV.ListSnapshots(t.Path)
			if err != nil {
				return failed(t, core.ErrSnapshot, "%s", err)
			}
			if len(snapshots) == 0 {
				return success(t, "no snapshots")
			}
			r := success(t, "%d snapshot(s)", len(snapshots))
			var b strings.Builder
			for i, s := range snapshots {
				prefix := "├──"
				if i == len(snapshots)-1 {
					prefix = "└──"
				}
				fmt.Fprintf(&b, "  %s %s\n", prefix, s)
			}
			r.output = b.String()
			return r
		}))
	},
}

// renderSnapshotTrees prints each VM followed by its snapshot tree.
func renderSnapshotTrees(rs []result) {
	multi := spansBackends(rs)
	for _, r := range rs {
		name := targetLabel(r.t, multi)
		if r.status != statusOK {
			printStatus(r, name)
			continue
		}
		fmt.Println(name)
		if r.output == "" {
			fmt.Println("  (no snapshots)")
			continue
		}
		fmt.Print(r.output)
	}
}

// snapshotTargetArgs splits "[vm-names...] <snapshot-name>" arguments.
// noNameFlag is true for --origin/--current, which take no snapshot name.
func snapshotTargetArgs(args []string, noNameFlag bool, flagName string) (snapName string, vmArgs []string, ok bool) {
	switch {
	case noNameFlag:
		return "", args, true
	case folderFlag != "":
		if len(args) < 1 {
			fail(core.ErrSnapshot, "specify a snapshot name, or use --%s", flagName)
			return "", nil, false
		}
		return args[0], nil, true
	case len(args) < 2:
		fail(core.ErrSnapshot, "specify one or more VM names followed by a snapshot name, or use --folder / --%s", flagName)
		return "", nil, false
	default:
		return args[len(args)-1], args[:len(args)-1], true
	}
}

// confirm asks a yes/no question unless --yes was given.
func confirm(warning string) bool {
	if yesFlag {
		return true
	}
	if warning != "" {
		fmt.Println(warning)
	}
	fmt.Print("Continue? [y/N]: ")
	var answer string
	fmt.Scanln(&answer)
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		fmt.Println("Aborted. (pass --yes to skip this prompt, e.g. in the pipeline)")
		return false
	}
	return true
}

var snapshotRevertCmd = &cobra.Command{
	Use:   "revert [vm-names...] [snapshot-name]",
	Short: "Revert a VM to a named snapshot (VM must be powered off)",
	Run: func(cmd *cobra.Command, args []string) {
		snapName, vmArgs, ok := snapshotTargetArgs(args, snapshotOriginFlag, "origin")
		if !ok {
			return
		}
		targets := resolveTargets(vmArgs, needTargets)
		if len(targets) == 0 {
			return
		}
		if snapshotOriginFlag && !confirm("WARNING: --origin will revert to the first snapshot and delete ALL snapshots for each target VM.") {
			return
		}
		p := snapshotPlan
		p.verb = "revert"
		report(p, run(targets, p, func(t core.Target) result {
			if r, ok := requireOff(t, "revert", "revert"); !ok {
				return r
			}
			if !snapshotOriginFlag {
				if err := t.HV.RevertToSnapshot(t.Path, snapName); err != nil {
					return failed(t, core.ErrSnapRevert, "%s", err)
				}
				return success(t, "reverted to snapshot %q", snapName)
			}

			snapshots, err := t.HV.ListSnapshots(t.Path)
			if err != nil {
				return failed(t, core.ErrSnapRevert, "%s", err)
			}
			if len(snapshots) == 0 {
				return skipped(t, core.ErrSnapNotFound, "no snapshots found")
			}
			origin := snapshots[0]
			if err := t.HV.RevertToSnapshot(t.Path, origin); err != nil {
				return failed(t, core.ErrSnapRevert, "%s", err)
			}
			if bad := deleteAll(t, snapshots); len(bad) > 0 {
				return failed(t, core.ErrSnapDelete, "reverted to origin %q but failed to delete %s", origin, strings.Join(bad, "; "))
			}
			return success(t, "reverted to origin snapshot %q, all snapshots deleted", origin)
		}))
	},
}

var snapshotDeleteCmd = &cobra.Command{
	Use:   "delete [vm-names...] [snapshot-name]",
	Short: "Delete a named snapshot from one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		snapName, vmArgs, ok := snapshotTargetArgs(args, snapshotCurrentFlag, "current")
		if !ok {
			return
		}
		targets := resolveTargets(vmArgs, needTargets)
		if len(targets) == 0 {
			return
		}
		if snapshotCurrentFlag && !confirm("WARNING: --current will delete ALL snapshots for each target VM.") {
			return
		}
		p := snapshotPlan
		p.verb = "snapshot delete"
		report(p, run(targets, p, func(t core.Target) result {
			if !snapshotCurrentFlag {
				if err := t.HV.DeleteSnapshot(t.Path, snapName); err != nil {
					return failed(t, core.ErrSnapDelete, "%s", err)
				}
				return success(t, "snapshot %q deleted", snapName)
			}
			snapshots, err := t.HV.ListSnapshots(t.Path)
			if err != nil {
				return failed(t, core.ErrSnapshot, "%s", err)
			}
			if len(snapshots) == 0 {
				return skipped(t, core.ErrSnapNotFound, "no snapshots")
			}
			if bad := deleteAll(t, snapshots); len(bad) > 0 {
				return failed(t, core.ErrSnapDelete, "failed to delete %s", strings.Join(bad, "; "))
			}
			return success(t, "all snapshots deleted")
		}))
	},
}

// deleteAll deletes snapshots and returns a description of each failure.
func deleteAll(t core.Target, snapshots []string) []string {
	var bad []string
	for _, s := range snapshots {
		if err := t.HV.DeleteSnapshot(t.Path, s); err != nil {
			bad = append(bad, fmt.Sprintf("%q: %s", s, err))
		}
	}
	return bad
}

func init() {
	rootCmd.AddCommand(snapshotCmd)
	snapshotCmd.AddCommand(snapshotCreateCmd, snapshotListCmd, snapshotRevertCmd, snapshotDeleteCmd)
	addFolderFlag(snapshotCreateCmd, snapshotListCmd, snapshotRevertCmd, snapshotDeleteCmd)

	snapshotCreateCmd.Flags().StringVarP(&snapshotNameFlag, "name", "n", "", "Snapshot name (default: {vmname}-YYYYMMDD-HHmmss)")
	snapshotRevertCmd.Flags().BoolVar(&snapshotOriginFlag, "origin", false, "Revert to first snapshot then delete all snapshots")
	snapshotRevertCmd.Flags().BoolVarP(&yesFlag, "yes", "y", false, "Skip confirmation prompt")
	snapshotDeleteCmd.Flags().BoolVar(&snapshotCurrentFlag, "current", false, "Delete all snapshots")
	snapshotDeleteCmd.Flags().BoolVarP(&yesFlag, "yes", "y", false, "Skip confirmation prompt")
}
