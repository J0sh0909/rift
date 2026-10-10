package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/J0sh0909/rift/internal/core"
	"github.com/spf13/cobra"
)

var (
	execUserFlag        string
	execPassFlag        string
	execInterpreterFlag string
	execTimeoutFlag     time.Duration
)

var execCmd = &cobra.Command{
	Use:   "exec (<vm-name> | -f <folder>) <command...>",
	Short: "Run a command inside running guest VMs",
	Long: `Run a shell command inside running guests and print each one's output.

  rift exec H26-U-S uname -a
  rift exec -f DEV ip a          every running VM in DEV, on every hypervisor
  rift exec -f WIN ipconfig      Windows guests run the command through cmd.exe

rift flags must come before the VM name; everything after it is the guest
command, so "rift exec vm ls -la" works without "--".

Transports: VMware Tools (vmrun), VirtualBox Guest Additions (guestcontrol),
Hyper-V PowerShell Direct for Windows guests and SSH for other guests.
A non-zero exit status in any guest makes rift exit 1.`,
	Run: func(cmd *cobra.Command, args []string) {
		var vmArgs, cmdParts []string
		if folderFlag != "" {
			cmdParts = args
		} else {
			if len(args) < 2 {
				fail(core.ErrExec, "specify a VM name and a command (or --folder and a command)")
				return
			}
			vmArgs, cmdParts = args[:1], args[1:]
		}
		if len(cmdParts) == 0 {
			fail(core.ErrExec, "specify a command to run")
			return
		}
		script := strings.Join(cmdParts, " ")

		s := loadSettings()
		user, pass := execUserFlag, execPassFlag
		implicitUser := user == ""
		if user == "" {
			user = s.DefaultUser
			if user == "" {
				user = "runner"
			}
		}
		if pass == "" {
			pass = s.DefaultPass
		}
		if pass == "" {
			fail(core.ErrExec, "guest password required: use --pass or set VM_DEFAULT_PASS in .env")
			return
		}

		targets := resolveTargets(vmArgs, needTargets)
		p := plan{verb: "exec", render: renderExec}
		report(p, run(targets, p, func(t core.Target) result {
			return execOne(t, user, pass, script, implicitUser)
		}))
	},
}

func execOne(t core.Target, user, pass, script string, implicitUser bool) result {
	if !t.Running {
		return skipped(t, core.ErrNotRunning, "not running")
	}
	gx, ok := t.HV.(core.GuestExecutor)
	if !ok {
		return skipped(t, core.ErrUnsupported, "guest exec is not supported on %s", core.BackendLabel(t.Backend))
	}

	osID := guestOSOf(t)
	family := guestFamilyOf(t, osID)
	interp := execInterpreterFlag
	if interp == "" {
		var found bool
		if interp, found = core.InterpreterFor(osID, family); !found {
			if osID == "" {
				return failed(t, core.ErrGuestOSNotDet, "guest OS not detected - %s, or pass --interpreter", osHint(t))
			}
			return failed(t, core.ErrGuestOSNotDet, "unknown guest OS %q - pass --interpreter", osID)
		}
	}
	windows := family == core.FamilyWindows || (family == "" && strings.Contains(interp, `\`))

	res, err := gx.ExecInGuest(t.VM, core.ExecRequest{
		User: user, Pass: pass, Interpreter: interp, Script: script,
		Windows: windows, Timeout: execTimeoutFlag,
	})
	if err != nil {
		if implicitUser && isAuthError(err) {
			return failed(t, core.ErrGuestCmd, "auth failed for '%s'. Run 'rift bootstrap create %s' or specify --user/--pass", user, t.Name)
		}
		return failed(t, core.ErrGuestCmd, "%s", err)
	}

	var r result
	switch res.ExitCode {
	case 0:
		r = success(t, "exit 0")
		r.quiet = true
	case -1:
		r = failed(t, core.ErrGuestCmd, "command failed")
	default:
		r = failed(t, core.ErrGuestCmd, "exit status %d", res.ExitCode)
	}
	r.output = res.Output
	return r
}

// isAuthError recognizes credential failures from every exec transport.
func isAuthError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"authentication failure", "invalid user name", "incorrect", // vmrun
		"not able to logon", "logon failure", // VirtualBox
		"credential is invalid", "access is denied", // PowerShell Direct
		"unable to authenticate", // SSH
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// renderExec prints guest output: raw for a single VM (so scripts can parse
// it), under a header per VM otherwise.
func renderExec(rs []result) {
	if len(rs) == 1 {
		r := rs[0]
		fmt.Print(r.output)
		if r.output != "" && !strings.HasSuffix(r.output, "\n") {
			fmt.Println()
		}
		printStatus(r, r.t.Name)
		return
	}
	printResults(rs)
}

// ---------------------------------------------------------------------------
// ip
// ---------------------------------------------------------------------------

var ipCmd = &cobra.Command{
	Use:   "ip [vm-names...]",
	Short: "Show guest IP addresses reported by the hypervisor",
	Long: `Show the IP addresses of running guests as reported by VMware Tools,
VirtualBox Guest Additions or Hyper-V integration services. No guest
credentials are needed. With no names or --folder, shows every VM on the node.

  rift ip -f DEV
  rift dispatch "ip -f DEV"     (from the pipeline: every node)`,
	Run: func(cmd *cobra.Command, args []string) {
		targets := resolveTargets(args, allIfNone)
		p := plan{verb: "ip", render: renderIPs}
		report(p, run(targets, p, func(t core.Target) result {
			if !t.Running {
				return skipped(t, core.ErrNotRunning, "not running")
			}
			r, ok := t.HV.(core.GuestIPReporter)
			if !ok {
				return skipped(t, core.ErrUnsupported, "IP lookup is not supported on %s", core.BackendLabel(t.Backend))
			}
			ips, err := r.GuestIPs(t.VM)
			if err != nil {
				return skipped(t, core.ErrGuestNoIP, "%s", err)
			}
			return success(t, "%s", strings.Join(ips, ", "))
		}))
	},
}

func renderIPs(rs []result) {
	if len(rs) == 0 {
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VM\tFOLDER\tHYPERVISOR\tIP")
	for _, r := range rs {
		ip := r.msg
		if r.status != statusOK {
			ip = "-  (" + r.msg + ")"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.t.Name, r.t.Folder, r.t.Backend, ip)
	}
	w.Flush()
}

func init() {
	rootCmd.AddCommand(execCmd, ipCmd)
	addFolderFlag(execCmd, ipCmd)
	// Stop flag parsing at the VM name so the guest command can carry its own
	// flags ("rift exec vm ls -la").
	execCmd.Flags().SetInterspersed(false)
	execCmd.Flags().StringVarP(&execUserFlag, "user", "u", "", "Guest OS username (default: VM_DEFAULT_USER or runner)")
	execCmd.Flags().StringVarP(&execPassFlag, "pass", "p", "", "Guest OS password (default: VM_DEFAULT_PASS)")
	execCmd.Flags().StringVarP(&execInterpreterFlag, "interpreter", "i", "", `Script interpreter (e.g. /bin/bash, C:\Windows\System32\cmd.exe; default: auto-detect)`)
	execCmd.Flags().DurationVar(&execTimeoutFlag, "timeout", 0, "Give up on a guest after this long (e.g. 30s, 5m; default: no limit)")
}
