package cmd

import (
	"github.com/J0sh0909/rift/internal/core"
	"github.com/spf13/cobra"
)

var hardFlag bool

var startCmd = &cobra.Command{
	Use:   "start [vm-names...]",
	Short: "Start one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		targets := resolveTargets(args, needTargets)
		if len(targets) == 0 {
			return
		}
		ensureHypervisorsRunning(targets)
		p := powerPlan("start")
		report(p, run(targets, p, startOne))
	},
}

func startOne(t core.Target) result {
	if t.Running {
		return skipped(t, core.ErrAlreadyRunning, "already running")
	}
	if err := t.HV.StartVM(t.Path); err != nil {
		return flaky(t, core.ErrStartFailed, err)
	}
	return success(t, "started")
}

var stopCmd = &cobra.Command{
	Use:   "stop [vm-names...]",
	Short: "Stop one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		targets := resolveTargets(args, needTargets)
		p := powerPlan("stop")
		report(p, run(targets, p, func(t core.Target) result {
			if !t.Running {
				return skipped(t, core.ErrAlreadyStopped, "already stopped")
			}
			var err error
			if hardFlag {
				err = t.HV.StopVM(t.Path, "hard")
			} else {
				err = t.HV.StopVM(t.Path)
			}
			if err != nil {
				return flaky(t, core.ErrStopFailed, err)
			}
			return success(t, "stopped")
		}))
	},
}

var suspendCmd = &cobra.Command{
	Use:   "suspend [vm-names...]",
	Short: "Suspend one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		targets := resolveTargets(args, needTargets)
		p := powerPlan("suspend")
		report(p, run(targets, p, func(t core.Target) result {
			if !t.Running {
				return skipped(t, core.ErrAlreadyStopped, "already stopped")
			}
			if err := t.HV.SuspendVM(t.Path); err != nil {
				return flaky(t, core.ErrSuspendFailed, err)
			}
			return success(t, "suspended")
		}))
	},
}

var resetCmd = &cobra.Command{
	Use:   "reset [vm-names...]",
	Short: "Reset one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		targets := resolveTargets(args, needTargets)
		p := powerPlan("reset")
		report(p, run(targets, p, func(t core.Target) result {
			if !t.Running {
				return skipped(t, core.ErrAlreadyStopped, "already stopped")
			}
			if err := t.HV.ResetVM(t.Path); err != nil {
				return flaky(t, core.ErrResetFailed, err)
			}
			return success(t, "reset")
		}))
	},
}

var restartCmd = &cobra.Command{
	Use:   "restart [vm-names...]",
	Short: "Restart one or more VMs (hard stop then start)",
	Run: func(cmd *cobra.Command, args []string) {
		targets := resolveTargets(args, needTargets)
		if len(targets) == 0 {
			return
		}
		ensureHypervisorsRunning(targets)

		// Phase 1: hard-stop every running target. Phase 2: start the ones
		// that stopped. Each phase retries on its own, so a retried start
		// never re-runs a stop that already succeeded.
		p := powerPlan("restart")
		final := run(targets, p, func(t core.Target) result {
			if !t.Running {
				return skipped(t, core.ErrAlreadyStopped, "already stopped")
			}
			if err := t.HV.StopVM(t.Path, "hard"); err != nil {
				return flaky(t, core.ErrStopFailed, err)
			}
			return success(t, "stopped")
		})

		var stopped []core.Target
		var stoppedIdx []int
		for i, r := range final {
			if r.status == statusOK {
				t := r.t
				t.Running = false
				stopped = append(stopped, t)
				stoppedIdx = append(stoppedIdx, i)
			}
		}
		started := run(stopped, plan{retries: p.retries}, func(t core.Target) result {
			if err := t.HV.StartVM(t.Path); err != nil {
				return flaky(t, core.ErrStartFailed, err)
			}
			return success(t, "restarted")
		})
		for j, i := range stoppedIdx {
			final[i] = started[j]
		}
		report(p, final)
	},
}

// ensureHypervisorsRunning starts the VMware Workstation application when
// needed before VMs are started (no-op for other backends).
func ensureHypervisorsRunning(targets []core.Target) {
	seen := map[string]bool{}
	for _, t := range targets {
		if seen[t.Backend] {
			continue
		}
		seen[t.Backend] = true
		if err := t.HV.EnsureVMwareRunning(); err != nil {
			core.LogError(core.ErrStartFailed, "", "%s: %s", core.BackendLabel(t.Backend), err)
		}
	}
}

func init() {
	rootCmd.AddCommand(startCmd, stopCmd, suspendCmd, resetCmd, restartCmd)
	addFolderFlag(startCmd, stopCmd, suspendCmd, resetCmd, restartCmd)
	stopCmd.Flags().BoolVarP(&hardFlag, "hard", "H", false, "Force power off")
}
