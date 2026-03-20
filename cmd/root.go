package cmd

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/J0sh0909/rift/internal"
	"github.com/spf13/cobra"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

// ---------------------------------------------------------------------------
// Global flags
// ---------------------------------------------------------------------------

var (
	folderFlag          string
	hardFlag            bool
	netFlag             bool
	specsFlag           bool
	coresFlag           int
	socketsFlag         int
	ramFlag             int
	nicIndexFlag        int
	nicTypeFlag         string
	regenMacFlag        bool
	addNicFlag          bool
	removeNicFlag       bool
	vnetFlag            string
	diskFlag            bool
	diskSizeFlag        int
	diskTypeFlag        int
	diskControllerFlag  string
	diskSlotFlag        int
	deleteFilesFlag     bool
	cdromFlag           bool
	isoFlag             string
	cdromControllerFlag string
	cdromSlotFlag       int
	bootConnectFlag     bool
	noBootConnectFlag   bool
	displayFlag         bool
	accel3dFlag         string
	gfxMemFlag          int
	snapshotNameFlag    string
	snapshotOriginFlag  bool
	snapshotCurrentFlag bool
	archiveFormatFlag   string
	archiveNameFlag     string
	archiveLatestFlag   bool
	archiveOldestFlag   bool
	yesFlag             bool
	execUserFlag        string
	execPassFlag        string
	execInterpreterFlag string
	osSetFlag           string
	bootstrapUserFlag      string
	bootstrapPassFlag      string
	bootstrapRunnerUserFlag string
	bootstrapRunnerPassFlag string
)

// ---------------------------------------------------------------------------
// Hypervisor + settings (lazy-initialized)
// ---------------------------------------------------------------------------

var (
	hv       internal.Hypervisor
	settings internal.Settings
	hvOnce   sync.Once
	hvErr    error
)

func requireSettings() {
	hvOnce.Do(func() {
		settings, hvErr = internal.LoadSettings()
		if hvErr != nil {
			return
		}
		hv, hvErr = internal.NewHypervisor(settings)
	})
	if hvErr != nil {
		fmt.Fprintln(os.Stderr, hvErr)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Helper: exit on error
// ---------------------------------------------------------------------------

func exitOnErr(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Helper: power-off gate — skips VM and prints message if running and required
// ---------------------------------------------------------------------------

func requireOff(vm internal.VM, action string) bool {
	if vm.Running && internal.RequiresPowerOff[action] {
		internal.LogError(internal.ErrPower, vm.Name, "must be powered off to %s", action)
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Helper: parallel power operations (used by start/stop/suspend/reset)
// ---------------------------------------------------------------------------

type powerResult struct {
	name string
	code string // empty = success
	msg  string
}

func runPowerParallel(targets []internal.VM, action func(internal.VM) powerResult) {
	var (
		mu      sync.Mutex
		results []powerResult
		wg      sync.WaitGroup
	)
	for _, vm := range targets {
		wg.Add(1)
		go func(vm internal.VM) {
			defer wg.Done()
			r := action(vm)
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}(vm)
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].name < results[j].name })
	for _, r := range results {
		if r.code != "" {
			internal.LogError(r.code, r.name, "%s", r.msg)
		} else {
			internal.LogInfo(r.name, r.msg)
		}
	}
}

// ---------------------------------------------------------------------------
// Root
// ---------------------------------------------------------------------------

var rootCmd = &cobra.Command{
	Use:   "rift",
	Short: "VMware Workstation VM manager",
}

// ---------------------------------------------------------------------------
// list
// ---------------------------------------------------------------------------

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all VMs",
	Run: func(cmd *cobra.Command, args []string) {
		requireSettings()
		vms, err := hv.GetPowerState()
		exitOnErr(err)

		folders := make(map[string][]internal.VM)
		for _, vm := range vms {
			folders[vm.Folder] = append(folders[vm.Folder], vm)
		}

		folderNames := make([]string, 0, len(folders))
		for name := range folders {
			folderNames = append(folderNames, name)
		}
		sort.Strings(folderNames)

		for i, folder := range folderNames {
			folderPrefix := "├──"
			childPrefix := "│   "
			if i == len(folderNames)-1 {
				folderPrefix = "└──"
				childPrefix = "    "
			}
			fmt.Printf("%s %s\n", folderPrefix, folder)

			for j, vm := range folders[folder] {
				status := "OFF"
				if vm.Running {
					status = "ON"
				}
				vmPrefix := childPrefix + "├──"
				if j == len(folders[folder])-1 {
					vmPrefix = childPrefix + "└──"
				}
				fmt.Printf("%s %s [%s]\n", vmPrefix, vm.Name, status)
			}
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
		requireSettings()
		targets, err := internal.ResolveAllVMs(hv, folderFlag, args)
		exitOnErr(err)

		showAll := !netFlag && !specsFlag && !diskFlag && !cdromFlag && !displayFlag
		pvnNames := internal.LoadPVNNames(settings.VmInventory)

		for i, vm := range targets {
			status := "OFF"
			if vm.Running {
				status = "ON"
			}
			fmt.Printf("%s [%s]\n", vm.Name, status)

			// Track which sections have been printed to determine tree prefixes
			hasPriorSection := false

			if showAll || specsFlag {
				specs, err := internal.ParseVMXSpecs(vm.Path)
				if err != nil {
					internal.LogError(internal.ErrVMXRead, vm.Name, "error reading specs")
				} else {
					fmt.Printf("├── CPU: %s vCPUs (%s sockets x %s cores)\n", specs.CPUCount, specs.Sockets, specs.CoresPerSocket)
					fmt.Printf("├── RAM: %s MB\n", specs.MemoryMB)
					fmt.Printf("├── Nested Virtualization: %s\n", specs.NestedVirt)
					fmt.Printf("├── Performance Counters: %s\n", specs.PerfCounters)
				}
				hasPriorSection = true
			}

			if showAll || netFlag {
				nics, err := internal.ParseVMXNetworking(vm.Path, pvnNames)
				if err != nil {
					internal.LogError(internal.ErrVMXRead, vm.Name, "error reading NICs")
				} else {
					indent := ""
					if hasPriorSection {
						fmt.Println("└── Network")
						indent = "    "
					}
					for k, nic := range nics {
						prefix := indent + "├──"
						if k == len(nics)-1 {
							prefix = indent + "└──"
						}
						fmt.Printf("%s NIC %s (%s)  MAC: %s\n", prefix, nic.Index, nic.Type, nic.MAC)
					}
				}
				hasPriorSection = true
			}

			if showAll || diskFlag {
				disks, err := internal.ParseVMXDisks(vm.Path)
				if err != nil {
					internal.LogError(internal.ErrVMXRead, vm.Name, "error reading disks")
				} else {
					indent := ""
					if hasPriorSection {
						fmt.Println("└── Disks")
						indent = "    "
					}
					for k, disk := range disks {
						prefix := indent + "├──"
						if k == len(disks)-1 {
							prefix = indent + "└──"
						}
						fmt.Printf("%s %s%s:%s [%s] %s (%s)\n", prefix, disk.Controller, disk.Index, disk.Slot, disk.SizeGB, disk.FileName, disk.DiskType)
					}
				}
				hasPriorSection = true
			}

			if showAll || cdromFlag {
				drives, err := internal.ParseVMXCDDrives(vm.Path)
				if err != nil {
					internal.LogError(internal.ErrVMXRead, vm.Name, "error reading CD/DVD drives")
				} else if len(drives) > 0 {
					indent := ""
					if hasPriorSection {
						fmt.Println("└── CD/DVD")
						indent = "    "
					}
					for k, drive := range drives {
						prefix := indent + "├──"
						if k == len(drives)-1 {
							prefix = indent + "└──"
						}
						connStr := "connect at boot: yes"
						if drive.StartConnected == "FALSE" {
							connStr = "connect at boot: no"
						}
						fmt.Printf("%s %s:%s [%s] %s (%s)\n", prefix, drive.Controller, drive.Slot, drive.DeviceType, drive.FileName, connStr)
					}
				}
				hasPriorSection = true
			}

			if showAll || displayFlag {
				disp, err := internal.ParseVMXDisplay(vm.Path)
				if err != nil {
					internal.LogError(internal.ErrVMXRead, vm.Name, "error reading display")
				} else {
					indent := ""
					if hasPriorSection {
						fmt.Println("└── Display")
						indent = "    "
					}
					fmt.Printf("%s├── 3D Acceleration: %s\n", indent, disp.Accelerated3D)
					fmt.Printf("%s└── Graphics Memory: %s MB\n", indent, disp.GraphicsMemoryMB)
				}
			}

			if i < len(targets)-1 {
				fmt.Println()
			}
		}
	},
}

// ---------------------------------------------------------------------------
// Power commands — all follow the same pattern via ResolveTargets
// ---------------------------------------------------------------------------

var startCmd = &cobra.Command{
	Use:   "start [vm-names...]",
	Short: "Start one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)
		exitOnErr(hv.EnsureVMwareRunning())

		if folderFlag == "" {
			for _, vm := range targets {
				if vm.Running {
					internal.LogError(internal.ErrAlreadyRunning, vm.Name, "already running")
					continue
				}
				if err := hv.StartVM(vm.Path); err != nil {
					internal.LogError(internal.ErrStartFailed, vm.Name, "%s", err)
					continue
				}
				internal.LogInfo(vm.Name, "started")
			}
			return
		}

		runPowerParallel(targets, func(vm internal.VM) powerResult {
			if vm.Running {
				return powerResult{vm.Name, internal.ErrAlreadyRunning, "already running"}
			}
			if err := hv.StartVM(vm.Path); err != nil {
				return powerResult{vm.Name, internal.ErrStartFailed, err.Error()}
			}
			return powerResult{vm.Name, "", "started"}
		})
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop [vm-names...]",
	Short: "Stop one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		if folderFlag == "" {
			for _, vm := range targets {
				if !vm.Running {
					internal.LogError(internal.ErrAlreadyStopped, vm.Name, "already stopped")
					continue
				}
				if hardFlag {
					err = hv.StopVM(vm.Path, "hard")
				} else {
					err = hv.StopVM(vm.Path)
				}
				if err != nil {
					internal.LogError(internal.ErrStopFailed, vm.Name, "%s", err)
					continue
				}
				internal.LogInfo(vm.Name, "stopped")
			}
			return
		}

		runPowerParallel(targets, func(vm internal.VM) powerResult {
			if !vm.Running {
				return powerResult{vm.Name, internal.ErrAlreadyStopped, "already stopped"}
			}
			var stopErr error
			if hardFlag {
				stopErr = hv.StopVM(vm.Path, "hard")
			} else {
				stopErr = hv.StopVM(vm.Path)
			}
			if stopErr != nil {
				return powerResult{vm.Name, internal.ErrStopFailed, stopErr.Error()}
			}
			return powerResult{vm.Name, "", "stopped"}
		})
	},
}

var suspendCmd = &cobra.Command{
	Use:   "suspend [vm-names...]",
	Short: "Suspend one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		if folderFlag == "" {
			for _, vm := range targets {
				if !vm.Running {
					internal.LogError(internal.ErrAlreadyStopped, vm.Name, "already stopped")
					continue
				}
				if err := hv.SuspendVM(vm.Path); err != nil {
					internal.LogError(internal.ErrSuspendFailed, vm.Name, "%s", err)
					continue
				}
				internal.LogInfo(vm.Name, "suspended")
			}
			return
		}

		runPowerParallel(targets, func(vm internal.VM) powerResult {
			if !vm.Running {
				return powerResult{vm.Name, internal.ErrAlreadyStopped, "already stopped"}
			}
			if err := hv.SuspendVM(vm.Path); err != nil {
				return powerResult{vm.Name, internal.ErrSuspendFailed, err.Error()}
			}
			return powerResult{vm.Name, "", "suspended"}
		})
	},
}

var resetCmd = &cobra.Command{
	Use:   "reset [vm-names...]",
	Short: "Reset one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		if folderFlag == "" {
			for _, vm := range targets {
				if !vm.Running {
					internal.LogError(internal.ErrAlreadyStopped, vm.Name, "already stopped")
					continue
				}
				if err := hv.ResetVM(vm.Path); err != nil {
					internal.LogError(internal.ErrResetFailed, vm.Name, "%s", err)
					continue
				}
				internal.LogInfo(vm.Name, "reset")
			}
			return
		}

		runPowerParallel(targets, func(vm internal.VM) powerResult {
			if !vm.Running {
				return powerResult{vm.Name, internal.ErrAlreadyStopped, "already stopped"}
			}
			if err := hv.ResetVM(vm.Path); err != nil {
				return powerResult{vm.Name, internal.ErrResetFailed, err.Error()}
			}
			return powerResult{vm.Name, "", "reset"}
		})
	},
}

// ---------------------------------------------------------------------------
// exec
// ---------------------------------------------------------------------------

var execCmd = &cobra.Command{
	Use:   "exec <target> <command...>",
	Short: "Run a command inside a running guest VM",
	Run: func(cmd *cobra.Command, args []string) {
		var vmArgs []string
		var cmdParts []string

		if folderFlag != "" {
			cmdParts = args
		} else {
			if len(args) < 2 {
				fmt.Println("Specify a VM name and a command")
				return
			}
			vmArgs = args[:1]
			cmdParts = args[1:]
		}

		if len(cmdParts) == 0 {
			fmt.Println("Specify a command to run")
			return
		}

		script := strings.Join(cmdParts, " ")

		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, vmArgs)
		exitOnErr(err)

		user := execUserFlag
		pass := execPassFlag
		if user == "" {
			user = settings.DefaultUser
		}
		if pass == "" {
			pass = settings.DefaultPass
		}
		if user == "" || pass == "" {
			fmt.Println("guest credentials required: use --user/--pass or set VM_DEFAULT_USER/VM_DEFAULT_PASS in .env")
			return
		}

		type result struct {
			name   string
			output string
		}

		runOne := func(vm internal.VM) result {
			if !vm.Running {
				internal.LogError(internal.ErrNotRunning, vm.Name, "not running")
				return result{vm.Name, ""}
			}
			guestOS := internal.GetGuestOS(vm.Path)
			interpreter := execInterpreterFlag
			if interpreter == "" {
				var ok bool
				interpreter, ok = internal.DefaultInterpreter(guestOS)
				if !ok {
					if guestOS == "" {
						internal.LogError(internal.ErrGuestOSNotDet, vm.Name, "guest OS not set — use \"rift config os %s --set <guestOS>\" or specify --interpreter manually", vm.Name)
						return result{vm.Name, ""}
					}
					internal.LogError(internal.ErrGuestOSNotDet, vm.Name, "unknown guest OS %q — specify --interpreter manually", guestOS)
					return result{vm.Name, ""}
				}
			}

			isWindows := strings.HasPrefix(strings.ToLower(guestOS), "windows") ||
				(guestOS == "" && (strings.Contains(interpreter, `C:\`) || strings.Contains(interpreter, `\`)))

			if isWindows {
				guestOutputPath := `C:\Windows\Temp\rift-exec-output.txt`
				if _, err := hv.RunGuestProgram(vm.Path, user, pass, `C:\Windows\System32\cmd.exe`, "/c "+script+` > C:\Windows\Temp\rift-exec-output.txt`); err != nil {
					internal.LogError(internal.ErrGuestCmd, vm.Name, "%s", err)
					return result{vm.Name, ""}
				}

				tmpFile, err := os.CreateTemp("", "rift-exec-*")
				if err != nil {
					_ = hv.DeleteFileInGuest(vm.Path, user, pass, guestOutputPath)
					internal.LogError(internal.ErrOutputCapture, vm.Name, "failed to create temp file: %s", err)
					return result{vm.Name, ""}
				}
				hostPath := tmpFile.Name()
				tmpFile.Close()

				if err := hv.CopyFileFromGuest(vm.Path, user, pass, guestOutputPath, hostPath); err != nil {
					os.Remove(hostPath)
					_ = hv.DeleteFileInGuest(vm.Path, user, pass, guestOutputPath)
					internal.LogError(internal.ErrOutputCapture, vm.Name, "%s", err)
					return result{vm.Name, ""}
				}

				data, readErr := os.ReadFile(hostPath)
				os.Remove(hostPath)
				_ = hv.DeleteFileInGuest(vm.Path, user, pass, guestOutputPath)
				if readErr != nil {
					internal.LogError(internal.ErrOutputCapture, vm.Name, "failed to read output: %s", readErr)
					return result{vm.Name, ""}
				}
				return result{vm.Name, string(data)}
			}
			// Linux/non-Windows: temp file redirect
			guestOutputPath := "/tmp/rift-exec-output.txt"
			wrappedScript := script + " > /tmp/rift-exec-output.txt 2>&1"
			if _, err := hv.RunGuestCommand(vm.Path, user, pass, interpreter, wrappedScript); err != nil {
				internal.LogError(internal.ErrGuestCmd, vm.Name, "%s", err)
				return result{vm.Name, ""}
			}

			tmpFile, err := os.CreateTemp("", "rift-exec-*")
			if err != nil {
				_ = hv.DeleteFileInGuest(vm.Path, user, pass, guestOutputPath)
				internal.LogError(internal.ErrOutputCapture, vm.Name, "failed to create temp file: %s", err)
				return result{vm.Name, ""}
			}
			hostPath := tmpFile.Name()
			tmpFile.Close()

			if err := hv.CopyFileFromGuest(vm.Path, user, pass, guestOutputPath, hostPath); err != nil {
				os.Remove(hostPath)
				_ = hv.DeleteFileInGuest(vm.Path, user, pass, guestOutputPath)
				internal.LogError(internal.ErrOutputCapture, vm.Name, "%s", err)
				return result{vm.Name, ""}
			}

			data, readErr := os.ReadFile(hostPath)
			os.Remove(hostPath)
			_ = hv.DeleteFileInGuest(vm.Path, user, pass, guestOutputPath)
			if readErr != nil {
				internal.LogError(internal.ErrOutputCapture, vm.Name, "failed to read output: %s", readErr)
				return result{vm.Name, ""}
			}

			return result{vm.Name, string(data)}
		}

		if folderFlag == "" {
			r := runOne(targets[0])
			fmt.Print(r.output)
			return
		}

		var (
			mu      sync.Mutex
			results []result
			wg      sync.WaitGroup
		)

		for _, vm := range targets {
			wg.Add(1)
			go func(vm internal.VM) {
				defer wg.Done()
				r := runOne(vm)
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}(vm)
		}
		wg.Wait()

		sort.Slice(results, func(i, j int) bool {
			return results[i].name < results[j].name
		})

		for _, r := range results {
			if r.output != "" {
				fmt.Printf("%s\n%s", r.name, r.output)
			}
		}
	},
}

// ---------------------------------------------------------------------------
// bootstrap helpers
// ---------------------------------------------------------------------------

// bootstrapRunOne executes a bootstrap script operation on a single VM.
// winURL is the URL to download the Windows script from.
// winTempFile is the guest-side destination path for the downloaded script.
// winPSArgs is the full PowerShell argument passed after -ExecutionPolicy Bypass
// (e.g. "-File C:\...\script.ps1 -Username foo" or "-Command \"...\"").
// linuxInner is the command string passed inside sudo -S bash -c '...'.
// successMsg is printed on success.
func bootstrapRunOne(vm internal.VM, user, pass, winURL, winTempFile, winPSArgs, linuxInner, successMsg string) powerResult {
	if !vm.Running {
		return powerResult{vm.Name, internal.ErrNotRunning, "not running"}
	}
	guestOS := internal.GetGuestOS(vm.Path)
	if guestOS == "" {
		return powerResult{vm.Name, internal.ErrGuestOSNotDet, `guest OS not set — use "rift config os <name> --set <guestOS>"`}
	}
	if strings.HasPrefix(strings.ToLower(guestOS), "windows") {
		if _, err := hv.RunGuestProgram(vm.Path, user, pass, `C:\Windows\System32\cmd.exe`,
			`/c curl.exe -sL `+winURL+` -o `+winTempFile); err != nil {
			return powerResult{vm.Name, internal.ErrBootstrapWindows, "script download failed: " + err.Error()}
		}
		if _, err := hv.RunGuestProgram(vm.Path, user, pass, `C:\Windows\System32\cmd.exe`,
			`/c powershell -ExecutionPolicy Bypass `+winPSArgs); err != nil {
			return powerResult{vm.Name, internal.ErrBootstrapWindows, "script execution failed: " + err.Error()}
		}
	} else {
		if _, err := hv.RunGuestCommand(vm.Path, user, pass, "/bin/bash",
			`echo '`+pass+`' | sudo -S bash -c '`+linuxInner+`'`); err != nil {
			return powerResult{vm.Name, internal.ErrBootstrapLinux, "failed: " + err.Error()}
		}
	}
	return powerResult{vm.Name, "", successMsg}
}

// bootstrapAuth resolves guest credentials from flags then .env defaults.
// requireSettings() must be called before this.
func bootstrapAuth(userFlag, passFlag string) (user, pass string, ok bool) {
	user = userFlag
	pass = passFlag
	if user == "" {
		user = settings.DefaultUser
	}
	if pass == "" {
		pass = settings.DefaultPass
	}
	if user == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "guest credentials required: use --user/--pass or set VM_DEFAULT_USER/VM_DEFAULT_PASS in .env")
		return "", "", false
	}
	return user, pass, true
}

// bootstrapEffectiveRunnerUser returns the runner username, defaulting to "runner".
func bootstrapEffectiveRunnerUser() string {
	if bootstrapRunnerUserFlag != "" {
		return bootstrapRunnerUserFlag
	}
	return "runner"
}

// bootstrapDispatch runs action for a single VM (inline) or all targets in parallel (folder mode).
func bootstrapDispatch(targets []internal.VM, action func(internal.VM) powerResult) {
	if folderFlag == "" {
		r := action(targets[0])
		if r.code != "" {
			internal.LogError(r.code, r.name, "%s", r.msg)
		} else {
			internal.LogInfo(r.name, r.msg)
		}
		return
	}
	runPowerParallel(targets, action)
}

// ---------------------------------------------------------------------------
// bootstrap
// ---------------------------------------------------------------------------

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap <target>",
	Short: "Provision and manage the automation user on guest VMs",
	Run: func(cmd *cobra.Command, args []string) {
		bootstrapCreateCmd.Run(cmd, args)
	},
}

var bootstrapCreateCmd = &cobra.Command{
	Use:   "create <target>",
	Short: "Provision the automation user on a guest VM",
	Run: func(cmd *cobra.Command, args []string) {
		if bootstrapRunnerPassFlag == "" {
			fmt.Fprintln(os.Stderr, "error: --runner-pass is required")
			return
		}
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)
		user, pass, ok := bootstrapAuth(bootstrapUserFlag, bootstrapPassFlag)
		if !ok {
			return
		}
		ru := bootstrapEffectiveRunnerUser()
		rp := bootstrapRunnerPassFlag
		b64rp := base64.StdEncoding.EncodeToString([]byte(rp))
		bootstrapDispatch(targets, func(vm internal.VM) powerResult {
			return bootstrapRunOne(vm, user, pass,
				"https://raw.githubusercontent.com/J0sh0909/bootstrap-utilities/main/bootstrap/bootstrap-windows.ps1",
				`C:\Windows\Temp\bootstrap.ps1`,
				`-File C:\Windows\Temp\bootstrap.ps1 -Username `+ru+` -Password `+rp,
				`RUNNER_PASS=$(echo `+b64rp+` | base64 -d) && curl -sL https://raw.githubusercontent.com/J0sh0909/bootstrap-utilities/main/bootstrap/bootstrap-linux.sh -o /tmp/bootstrap.sh && chmod +x /tmp/bootstrap.sh && /tmp/bootstrap.sh `+ru+` "$RUNNER_PASS"`,
				"bootstrap complete",
			)
		})
	},
}

var bootstrapVerifyCmd = &cobra.Command{
	Use:   "verify <target>",
	Short: "Verify the automation user is correctly provisioned on a guest VM",
	Run: func(cmd *cobra.Command, args []string) {
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)
		user, pass, ok := bootstrapAuth(bootstrapUserFlag, bootstrapPassFlag)
		if !ok {
			return
		}
		ru := bootstrapEffectiveRunnerUser()
		bootstrapDispatch(targets, func(vm internal.VM) powerResult {
			return bootstrapRunOne(vm, user, pass,
				"https://raw.githubusercontent.com/J0sh0909/bootstrap-utilities/main/verify/verify-windows.ps1",
				`C:\Windows\Temp\verify.ps1`,
				`-File C:\Windows\Temp\verify.ps1 -Username `+ru,
				`curl -sL https://raw.githubusercontent.com/J0sh0909/bootstrap-utilities/main/verify/verify-linux.sh -o /tmp/verify.sh && chmod +x /tmp/verify.sh && /tmp/verify.sh `+ru,
				"verify passed",
			)
		})
	},
}

var bootstrapResetCmd = &cobra.Command{
	Use:   "reset <target>",
	Short: "Reset the automation user password on a guest VM",
	Run: func(cmd *cobra.Command, args []string) {
		if bootstrapRunnerPassFlag == "" {
			fmt.Fprintln(os.Stderr, "error: --runner-pass is required")
			return
		}
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)
		user, pass, ok := bootstrapAuth(bootstrapUserFlag, bootstrapPassFlag)
		if !ok {
			return
		}
		ru := bootstrapEffectiveRunnerUser()
		rp := bootstrapRunnerPassFlag
		b64rp := base64.StdEncoding.EncodeToString([]byte(rp))
		bootstrapDispatch(targets, func(vm internal.VM) powerResult {
			return bootstrapRunOne(vm, user, pass,
				"https://raw.githubusercontent.com/J0sh0909/bootstrap-utilities/main/reset-password/reset-windows.ps1",
				`C:\Windows\Temp\reset-password.ps1`,
				`-File C:\Windows\Temp\reset-password.ps1 -Username `+ru+` -NewPassword `+rp,
				`RUNNER_PASS=$(echo `+b64rp+` | base64 -d) && curl -sL https://raw.githubusercontent.com/J0sh0909/bootstrap-utilities/main/reset-password/reset-linux.sh -o /tmp/reset-password.sh && chmod +x /tmp/reset-password.sh && /tmp/reset-password.sh `+ru+` "$RUNNER_PASS"`,
				"password reset complete",
			)
		})
	},
}

var bootstrapRevokeCmd = &cobra.Command{
	Use:   "revoke <target>",
	Short: "Remove the automation user from a guest VM",
	Run: func(cmd *cobra.Command, args []string) {
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)
		user, pass, ok := bootstrapAuth(bootstrapUserFlag, bootstrapPassFlag)
		if !ok {
			return
		}
		ru := bootstrapEffectiveRunnerUser()
		bootstrapDispatch(targets, func(vm internal.VM) powerResult {
			return bootstrapRunOne(vm, user, pass,
				"https://raw.githubusercontent.com/J0sh0909/bootstrap-utilities/main/revoke/revoke-windows.ps1",
				`C:\Windows\Temp\revoke.ps1`,
				`-File C:\Windows\Temp\revoke.ps1 -Username `+ru,
				`curl -sL https://raw.githubusercontent.com/J0sh0909/bootstrap-utilities/main/revoke/revoke-linux.sh -o /tmp/revoke.sh && chmod +x /tmp/revoke.sh && /tmp/revoke.sh `+ru,
				"revoke complete",
			)
		})
	},
}

// ---------------------------------------------------------------------------
// snapshot
// ---------------------------------------------------------------------------

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Manage VM snapshots",
}

var snapshotCreateCmd = &cobra.Command{
	Use:   "create [vm-names...]",
	Short: "Create a snapshot of one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		for _, vm := range targets {
			name := snapshotNameFlag
			if name == "" {
				name = vm.Name + "-" + time.Now().Format("20060102-150405")
			} else {
				if err := internal.ValidateVMName(name); err != nil {
					internal.LogError(internal.ErrSnapCreate, vm.Name, "invalid snapshot name: %s", err)
					continue
				}
			}

			wasRunning := vm.Running

			existingSnaps, snapListErr := hv.ListSnapshots(vm.Path)
			if snapListErr == nil {
				for _, s := range existingSnaps {
					if s == name {
						internal.LogError(internal.ErrSnapExists, vm.Name, "snapshot %q already exists", name)
						goto nextVM
					}
				}
			}

			if wasRunning {
				internal.LogInfo(vm.Name, "running — suspending before snapshot")
				if err := hv.SuspendVM(vm.Path); err != nil {
					internal.LogError(internal.ErrSuspendFailed, vm.Name, "%s", err)
					continue
				}
			}

			if err := hv.CreateSnapshot(vm.Path, name); err != nil {
				internal.LogError(internal.ErrSnapCreate, vm.Name, "%s", err)
				// Try to resume even if snapshot failed
				if wasRunning {
					_ = hv.StartVM(vm.Path)
				}
				continue
			}
			internal.LogInfo(vm.Name, "snapshot %q created", name)

			if wasRunning {
				if err := hv.StartVM(vm.Path); err != nil {
					internal.LogError(internal.ErrSnapCreate, vm.Name, "snapshot created but failed to resume: %s", err)
					continue
				}
				internal.LogInfo(vm.Name, "resumed")
			}
		nextVM:
		}
	},
}

var snapshotListCmd = &cobra.Command{
	Use:   "list [vm-names...]",
	Short: "List snapshots for one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		for _, vm := range targets {
			snapshots, err := hv.ListSnapshots(vm.Path)
			if err != nil {
				internal.LogError(internal.ErrSnapshot, vm.Name, "%s", err)
				continue
			}
			fmt.Printf("%s\n", vm.Name)
			if len(snapshots) == 0 {
				fmt.Println("  (no snapshots)")
				continue
			}
			for i, s := range snapshots {
				prefix := "├──"
				if i == len(snapshots)-1 {
					prefix = "└──"
				}
				fmt.Printf("  %s %s\n", prefix, s)
			}
		}
	},
}

var snapshotRevertCmd = &cobra.Command{
	Use:   "revert [vm-names...] [snapshot-name]",
	Short: "Revert a VM to a named snapshot (VM must be powered off)",
	Run: func(cmd *cobra.Command, args []string) {
		var snapName string
		var vmArgs []string

		if !snapshotOriginFlag {
			if folderFlag != "" {
				if len(args) < 1 {
					fmt.Println("Specify a snapshot name, or use --origin")
					return
				}
				snapName = args[0]
			} else {
				if len(args) < 2 {
					fmt.Println("Specify one or more VM names followed by a snapshot name, or use --folder / --origin")
					return
				}
				snapName = args[len(args)-1]
				vmArgs = args[:len(args)-1]
			}
		} else {
			vmArgs = args
		}

		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, vmArgs)
		exitOnErr(err)

		if snapshotOriginFlag && !yesFlag {
			fmt.Println("WARNING: --origin will revert to the first snapshot and delete ALL snapshots for each target VM.")
			fmt.Print("Continue? [y/N]: ")
			var confirm string
			fmt.Scanln(&confirm)
			if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
				fmt.Println("Aborted.")
				return
			}
		}

		for _, vm := range targets {
			if !requireOff(vm, "revert") {
				continue
			}

			if snapshotOriginFlag {
				snapshots, err := hv.ListSnapshots(vm.Path)
				if err != nil {
					internal.LogError(internal.ErrSnapRevert, vm.Name, "%s", err)
					continue
				}
				if len(snapshots) == 0 {
					internal.LogError(internal.ErrSnapNotFound, vm.Name, "no snapshots found")
					continue
				}
				origin := snapshots[0]
				if err := hv.RevertToSnapshot(vm.Path, origin); err != nil {
					internal.LogError(internal.ErrSnapRevert, vm.Name, "%s", err)
					continue
				}
				internal.LogInfo(vm.Name, "reverted to origin snapshot %q", origin)
				for _, s := range snapshots {
					if err := hv.DeleteSnapshot(vm.Path, s); err != nil {
						internal.LogError(internal.ErrSnapDelete, vm.Name, "failed to delete snapshot %q: %s", s, err)
					}
				}
				internal.LogInfo(vm.Name, "all snapshots deleted")
				continue
			}

			if err := hv.RevertToSnapshot(vm.Path, snapName); err != nil {
				internal.LogError(internal.ErrSnapRevert, vm.Name, "%s", err)
				continue
			}
			internal.LogInfo(vm.Name, "reverted to snapshot %q", snapName)
		}
	},
}

var snapshotDeleteCmd = &cobra.Command{
	Use:   "delete [vm-names...] [snapshot-name]",
	Short: "Delete a named snapshot from one or more VMs",
	Run: func(cmd *cobra.Command, args []string) {
		var snapName string
		var vmArgs []string

		if !snapshotCurrentFlag {
			if folderFlag != "" {
				if len(args) < 1 {
					fmt.Println("Specify a snapshot name, or use --current")
					return
				}
				snapName = args[0]
			} else {
				if len(args) < 2 {
					fmt.Println("Specify one or more VM names followed by a snapshot name, or use --folder / --current")
					return
				}
				snapName = args[len(args)-1]
				vmArgs = args[:len(args)-1]
			}
		} else {
			vmArgs = args
		}

		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, vmArgs)
		exitOnErr(err)

		if snapshotCurrentFlag && !yesFlag {
			fmt.Println("WARNING: --current will delete ALL snapshots for each target VM.")
			fmt.Print("Continue? [y/N]: ")
			var confirm string
			fmt.Scanln(&confirm)
			if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
				fmt.Println("Aborted.")
				return
			}
		}

		for _, vm := range targets {
			if snapshotCurrentFlag {
				snapshots, err := hv.ListSnapshots(vm.Path)
				if err != nil {
					internal.LogError(internal.ErrSnapshot, vm.Name, "%s", err)
					continue
				}
				if len(snapshots) == 0 {
					internal.LogError(internal.ErrSnapNotFound, vm.Name, "no snapshots")
					continue
				}
				for _, s := range snapshots {
					if err := hv.DeleteSnapshot(vm.Path, s); err != nil {
						internal.LogError(internal.ErrSnapDelete, vm.Name, "failed to delete snapshot %q: %s", s, err)
					}
				}
				internal.LogInfo(vm.Name, "all snapshots deleted")
				continue
			}

			if err := hv.DeleteSnapshot(vm.Path, snapName); err != nil {
				internal.LogError(internal.ErrSnapDelete, vm.Name, "%s", err)
				continue
			}
			internal.LogInfo(vm.Name, "snapshot %q deleted", snapName)
		}
	},
}

// ---------------------------------------------------------------------------
// archive
// ---------------------------------------------------------------------------

var archiveCmd = &cobra.Command{
	Use:   "archive",
	Short: "Archive VM exports",
}

var archiveExportCmd = &cobra.Command{
	Use:   "export [vm-names...]",
	Short: "Export a VM as OVF or OVA",
	Run: func(cmd *cobra.Command, args []string) {
		if archiveFormatFlag == "" {
			fmt.Println("--format is required (ovf or ova)")
			return
		}
		format, err := internal.ValidateFormat(archiveFormatFlag)
		if err != nil {
			fmt.Println(err)
			return
		}
		if archiveNameFlag != "" {
			if err := internal.ValidateVMName(archiveNameFlag); err != nil {
				fmt.Printf("invalid --name: %s\n", err)
				return
			}
		}

		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		if settings.ArchivePath == "" {
			internal.LogError(internal.ErrMissingSetting, "", "ARCHIVE_PATH is not set in .env")
			return
		}

		if _, err := hv.FindOvftool(); err != nil {
			internal.LogError(internal.ErrOvftoolNotFound, "", "%s", err)
			return
		}

		ts := time.Now().Format("20060102-150405")

		// resolveDestPath builds the destination path for a VM and creates its
		// directory. Returns ("", false) and prints a message on failure.
		type exportJob struct {
			vm       internal.VM
			destPath string
		}
		resolveDestPath := func(vm internal.VM) (string, bool) {
			label := vm.Name
			if archiveNameFlag != "" {
				if folderFlag != "" {
					label = archiveNameFlag + "-" + vm.Name
				} else {
					label = archiveNameFlag
				}
			}
			folderPart := vm.Folder
			if folderPart == "." {
				folderPart = ""
			}
			var destPath, dir string
			switch format {
			case "ovf":
				versionDir := label + "-" + ts
				if folderPart != "" {
					dir = filepath.Join(settings.ArchivePath, "OVF", folderPart, label, versionDir)
				} else {
					dir = filepath.Join(settings.ArchivePath, "OVF", label, versionDir)
				}
				if err := os.MkdirAll(dir, 0755); err != nil {
					internal.LogError(internal.ErrExportFailed, vm.Name, "failed to create export directory: %s", err)
					return "", false
				}
				destPath = filepath.Join(dir, label+".ovf")
			case "ova":
				if folderPart != "" {
					dir = filepath.Join(settings.ArchivePath, "OVA", folderPart, label)
				} else {
					dir = filepath.Join(settings.ArchivePath, "OVA", label)
				}
				if err := os.MkdirAll(dir, 0755); err != nil {
					internal.LogError(internal.ErrExportFailed, vm.Name, "failed to create export directory: %s", err)
					return "", false
				}
				destPath = filepath.Join(dir, label+"-"+ts+".ova")
			}
			return destPath, true
		}

		if folderFlag != "" {
			// Parallel export with one mpb progress bar per VM.
			var jobs []exportJob
			for _, vm := range targets {
				if !requireOff(vm, "export") {
					continue
				}
				destPath, ok := resolveDestPath(vm)
				if !ok {
					continue
				}
				internal.LogInfo(vm.Name, "exporting as %s to %s", strings.ToUpper(format), destPath)
				jobs = append(jobs, exportJob{vm, destPath})
			}
			if len(jobs) == 0 {
				return
			}

			// Compute label width for aligned bars.
			maxName := 0
			for _, j := range jobs {
				if len(j.vm.Name) > maxName {
					maxName = len(j.vm.Name)
				}
			}
			nameFmt := fmt.Sprintf("%%-%ds", maxName)

			type exportResult struct {
				name string
				err  error
			}
			results := make([]exportResult, len(jobs))

			p := mpb.New()
			var wg sync.WaitGroup
			for i, j := range jobs {
				i, j := i, j
				bar := p.New(100,
					mpb.BarStyle().Lbound("[").Filler("=").Tip("").Padding(" ").Rbound("]"),
					mpb.BarWidth(50),
					mpb.PrependDecorators(
						decor.Name(fmt.Sprintf(nameFmt, j.vm.Name)),
					),
					mpb.AppendDecorators(
						decor.Percentage(decor.WCSyncSpaceR),
					),
				)
				wg.Add(1)
				go func() {
					defer wg.Done()
					err := hv.ExportVMWithBar(j.vm.Path, j.destPath, bar)
					if err != nil {
						bar.Abort(false)
					} else {
						bar.SetCurrent(100)
					}
					results[i] = exportResult{j.vm.Name, err}
				}()
			}
			wg.Wait()
			p.Wait()
			for _, r := range results {
				if r.err != nil {
					internal.LogError(internal.ErrExportFailed, r.name, "%s", r.err)
				} else {
					internal.LogInfo(r.name, "export complete")
				}
			}
		} else {
			// Sequential export with inline RenderProgressBar.
			for _, vm := range targets {
				if !requireOff(vm, "export") {
					continue
				}
				destPath, ok := resolveDestPath(vm)
				if !ok {
					continue
				}
				internal.LogInfo(vm.Name, "exporting as %s to %s", strings.ToUpper(format), destPath)
				if err := hv.ExportVM(vm.Path, destPath); err != nil {
					internal.LogError(internal.ErrExportFailed, vm.Name, "%s", err)
					continue
				}
				internal.LogInfo(vm.Name, "export complete")
			}
		}
	},
}

var archiveImportCmd = &cobra.Command{
	Use:   "import <archive-name>",
	Short: "Import an archived VM into VMware",
	Run: func(cmd *cobra.Command, args []string) {
		if archiveLatestFlag && archiveOldestFlag {
			fmt.Println("--latest and --oldest are mutually exclusive")
			return
		}
		if len(args) < 1 {
			if archiveLatestFlag || archiveOldestFlag {
				fmt.Println("Specify the VM name (e.g. H74-RT)")
			} else {
				fmt.Println("Specify the archive name to import (e.g. H74-RT-20260302-181915)")
			}
			return
		}
		target := args[0]

		requireSettings()

		if settings.ArchivePath == "" {
			internal.LogError(internal.ErrMissingSetting, "", "ARCHIVE_PATH is not set in .env")
			return
		}

		if _, err := hv.FindOvftool(); err != nil {
			internal.LogError(internal.ErrOvftoolNotFound, "", "%s", err)
			return
		}

		entries, err := internal.ScanArchives(settings.ArchivePath)
		if err != nil {
			internal.LogError(internal.ErrArchive, "", "%s", err)
			return
		}

		var matches []internal.ArchiveEntry
		if archiveLatestFlag || archiveOldestFlag {
			var fmtFilter string
			if archiveFormatFlag != "" {
				var fmtErr error
				fmtFilter, fmtErr = internal.ValidateFormat(archiveFormatFlag)
				if fmtErr != nil {
					internal.LogError(internal.ErrArchive, "", "%s", fmtErr)
					return
				}
			}
			for _, e := range entries {
				if e.VMName != target {
					continue
				}
				if fmtFilter != "" && strings.ToLower(e.Format) != fmtFilter {
					continue
				}
				matches = append(matches, e)
			}
			if len(matches) == 0 {
				internal.LogError(internal.ErrArchive, "", "no archives found for VM %q", target)
				return
			}
			sort.Slice(matches, func(i, j int) bool {
				return matches[i].Version < matches[j].Version
			})
			if archiveLatestFlag {
				matches = []internal.ArchiveEntry{matches[len(matches)-1]}
			} else {
				matches = []internal.ArchiveEntry{matches[0]}
			}
		} else {
			for _, e := range entries {
				if e.Version == target {
					matches = append(matches, e)
				}
			}
			if len(matches) == 0 {
				internal.LogError(internal.ErrArchive, "", "no archive found with name %q", target)
				return
			}
		}

		var chosen internal.ArchiveEntry
		if len(matches) == 1 {
			chosen = matches[0]
		} else {
			fmt.Printf("Found %d matches for %q:\n", len(matches), target)
			for i, m := range matches {
				fmt.Printf("  %d) %s  %s  (%s)\n", i+1, m.Format, m.Version, formatBytes(m.SizeBytes))
			}
			fmt.Printf("Select [1-%d]: ", len(matches))
			var sel int
			fmt.Scan(&sel)
			if sel < 1 || sel > len(matches) {
				fmt.Println("Invalid selection. Aborted.")
				return
			}
			chosen = matches[sel-1]
		}

		// Resolve the actual file path for ovftool
		var srcPath string
		if chosen.Format == "OVF" {
			ovfFiles, err := filepath.Glob(filepath.Join(chosen.Path, "*.ovf"))
			if err != nil || len(ovfFiles) == 0 {
				internal.LogError(internal.ErrImportFailed, "", "no .ovf file found in %s", chosen.Path)
				return
			}
			srcPath = ovfFiles[0]
		} else {
			srcPath = chosen.Path
		}

		name := archiveNameFlag
		if name == "" {
			name = chosen.VMName
		}
		if err := internal.ValidateVMName(name); err != nil {
			fmt.Printf("invalid VM name %q: %s\nUse --name to specify a valid name\n", name, err)
			return
		}

		destDir := filepath.Join(settings.VmDirectory, name)
		if err := os.MkdirAll(destDir, 0755); err != nil {
			internal.LogError(internal.ErrImportFailed, "", "failed to create VM directory: %s", err)
			return
		}
		destVmx := filepath.Join(destDir, name+".vmx")

		fmt.Printf("importing [%s] %s → %s\n", chosen.Format, chosen.Version, destVmx)
		if err := hv.ImportVM(srcPath, destVmx); err != nil {
			internal.LogError(internal.ErrImportFailed, name, "%s", err)
			return
		}
		internal.LogInfo(name, "import complete")

		internal.LogInfo(name, "registering with VMware...")
		if err := hv.StartVM(destVmx); err != nil {
			internal.LogError(internal.ErrImportFailed, name, "failed to register (start): %s", err)
			return
		}
		if err := hv.StopVM(destVmx, "hard"); err != nil {
			internal.LogError(internal.ErrImportFailed, name, "failed to register (stop): %s", err)
			return
		}
		internal.LogInfo(name, "registered and powered off")
	},
}

var archiveListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all VM archives",
	Run: func(cmd *cobra.Command, args []string) {
		requireSettings()

		if settings.ArchivePath == "" {
			internal.LogError(internal.ErrMissingSetting, "", "ARCHIVE_PATH is not set in .env")
			return
		}

		entries, err := internal.ScanArchives(settings.ArchivePath)
		if err != nil {
			internal.LogError(internal.ErrArchive, "", "%s", err)
			return
		}
		if len(entries) == 0 {
			fmt.Println("No archives found")
			return
		}

		type groupKey struct{ Folder, VMName string }
		var order []groupKey
		groups := map[groupKey][]internal.ArchiveEntry{}
		for _, e := range entries {
			k := groupKey{e.Folder, e.VMName}
			if _, exists := groups[k]; !exists {
				order = append(order, k)
			}
			groups[k] = append(groups[k], e)
		}

		for gi, k := range order {
			if k.Folder != "" {
				fmt.Printf("%s  [folder: %s]\n", k.VMName, k.Folder)
			} else {
				fmt.Printf("%s\n", k.VMName)
			}
			group := groups[k]
			for i, e := range group {
				prefix := "├──"
				if i == len(group)-1 {
					prefix = "└──"
				}
				fmt.Printf("  %s %s  %s  %s\n", prefix, e.Format, e.Version, formatBytes(e.SizeBytes))
			}
			if gi < len(order)-1 {
				fmt.Println()
			}
		}
	},
}

var archiveDeleteCmd = &cobra.Command{
	Use:   "delete <archive-name>",
	Short: "Delete an archived VM version",
	Run: func(cmd *cobra.Command, args []string) {
		if archiveLatestFlag && archiveOldestFlag {
			fmt.Println("--latest and --oldest are mutually exclusive")
			return
		}
		if len(args) < 1 {
			if archiveLatestFlag || archiveOldestFlag {
				fmt.Println("Specify the VM name (e.g. H74-RT)")
			} else {
				fmt.Println("Specify the archive name to delete (e.g. win11-dev-20260302-143022)")
			}
			return
		}
		target := args[0]

		requireSettings()

		if settings.ArchivePath == "" {
			internal.LogError(internal.ErrMissingSetting, "", "ARCHIVE_PATH is not set in .env")
			return
		}

		entries, err := internal.ScanArchives(settings.ArchivePath)
		if err != nil {
			internal.LogError(internal.ErrArchive, "", "%s", err)
			return
		}

		var matches []internal.ArchiveEntry
		if archiveLatestFlag || archiveOldestFlag {
			var fmtFilter string
			if archiveFormatFlag != "" {
				var fmtErr error
				fmtFilter, fmtErr = internal.ValidateFormat(archiveFormatFlag)
				if fmtErr != nil {
					internal.LogError(internal.ErrArchive, "", "%s", fmtErr)
					return
				}
			}
			for _, e := range entries {
				if e.VMName != target {
					continue
				}
				if fmtFilter != "" && strings.ToLower(e.Format) != fmtFilter {
					continue
				}
				matches = append(matches, e)
			}
			if len(matches) == 0 {
				internal.LogError(internal.ErrArchive, "", "no archives found for VM %q", target)
				return
			}
			sort.Slice(matches, func(i, j int) bool {
				return matches[i].Version < matches[j].Version
			})
			if archiveLatestFlag {
				matches = []internal.ArchiveEntry{matches[len(matches)-1]}
			} else {
				matches = []internal.ArchiveEntry{matches[0]}
			}
		} else {
			for _, e := range entries {
				if e.Version != target {
					continue
				}
				if archiveFormatFlag != "" {
					norm, err := internal.ValidateFormat(archiveFormatFlag)
					if err != nil {
						internal.LogError(internal.ErrArchive, "", "%s", err)
						return
					}
					if strings.ToLower(e.Format) != norm {
						continue
					}
				}
				matches = append(matches, e)
			}
			if len(matches) == 0 {
				if archiveFormatFlag != "" {
					internal.LogError(internal.ErrArchive, "", "no %s archive found with name %q", strings.ToUpper(archiveFormatFlag), target)
				} else {
					internal.LogError(internal.ErrArchive, "", "no archive found with name %q", target)
				}
				return
			}
		}

		fmt.Printf("WARNING: will permanently delete %d archive(s):\n", len(matches))
		for _, m := range matches {
			fmt.Printf("  [%s] %s (%s)\n", m.Format, m.Version, formatBytes(m.SizeBytes))
		}
		if !yesFlag {
			fmt.Print("Continue? [y/N]: ")
			var confirm string
			fmt.Scanln(&confirm)
			if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
				fmt.Println("Aborted.")
				return
			}
		}

		for _, m := range matches {
			if err := os.RemoveAll(m.Path); err != nil {
				internal.LogError(internal.ErrArchive, "", "failed to delete [%s] %s: %s", m.Format, m.Version, err)
				continue
			}
			fmt.Printf("[%s] %s → deleted\n", m.Format, m.Version)
		}
	},
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// ---------------------------------------------------------------------------
// config cpu
// ---------------------------------------------------------------------------

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Modify VM settings",
}

var configCpuCmd = &cobra.Command{
	Use:   "cpu [vm-names...]",
	Short: "Change CPU settings",
	Run: func(cmd *cobra.Command, args []string) {
		if coresFlag == 0 || socketsFlag == 0 {
			fmt.Println("Both --cores and --sockets are required")
			return
		}

		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		// Hardware validation
		host, _ := internal.DetectHostResources()
		if err := internal.ValidateCPU(host, socketsFlag, coresFlag); err != nil {
			internal.LogError(internal.ErrCPUConfig, "", "%s", err)
			return
		}

		totalCPUs := coresFlag * socketsFlag

		for _, vm := range targets {
			if !requireOff(vm, "change CPU") {
				continue
			}
			err = internal.SetVMXKey(vm.Path, "numvcpus", fmt.Sprintf("%d", totalCPUs))
			if err != nil {
				internal.LogError(internal.ErrCPUConfig, vm.Name, "%s", err)
				continue
			}
			err = internal.SetVMXKey(vm.Path, "cpuid.coresPerSocket", fmt.Sprintf("%d", coresFlag))
			if err != nil {
				internal.LogError(internal.ErrCPUConfig, vm.Name, "%s", err)
				continue
			}
			internal.LogInfo(vm.Name, "CPU: %d vCPUs (%d sockets x %d cores)", totalCPUs, socketsFlag, coresFlag)
		}
	},
}

// ---------------------------------------------------------------------------
// config ram — GB input, stored as MB in VMX
// ---------------------------------------------------------------------------

var configRamCmd = &cobra.Command{
	Use:   "ram [vm-names...]",
	Short: "Change RAM size (in GB)",
	Run: func(cmd *cobra.Command, args []string) {
		if ramFlag == 0 {
			fmt.Println("--size is required")
			return
		}

		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		// Hardware validation
		host, _ := internal.DetectHostResources()
		if err := internal.ValidateRAM(host, ramFlag); err != nil {
			internal.LogError(internal.ErrRAMConfig, "", "%s", err)
			return
		}

		memMB := internal.GBtoMB(ramFlag)

		for _, vm := range targets {
			if !requireOff(vm, "change RAM") {
				continue
			}
			err = internal.SetVMXKey(vm.Path, "memsize", fmt.Sprintf("%d", memMB))
			if err != nil {
				internal.LogError(internal.ErrRAMConfig, vm.Name, "%s", err)
				continue
			}
			internal.LogInfo(vm.Name, "RAM: %d GB (%d MB)", ramFlag, memMB)
		}
	},
}

// ---------------------------------------------------------------------------
// config display — gfxmem takes MB, stored as KB
// ---------------------------------------------------------------------------

var configDisplayCmd = &cobra.Command{
	Use:   "display [vm-names...]",
	Short: "Change display settings",
	Run: func(cmd *cobra.Command, args []string) {
		if accel3dFlag == "" && gfxMemFlag == 0 {
			fmt.Println("Specify --accel3d (on/off) and/or --gfxmem (MB)")
			return
		}

		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		// Hardware validation for gfx memory
		if gfxMemFlag > 0 {
			host, _ := internal.DetectHostResources()
			if err := internal.ValidateGfxMem(host, gfxMemFlag); err != nil {
				internal.LogError(internal.ErrDisplayConfig, "", "%s", err)
				return
			}
		}

		for _, vm := range targets {
			if !requireOff(vm, "change display") {
				continue
			}
			if accel3dFlag != "" {
				val := "FALSE"
				if strings.EqualFold(accel3dFlag, "on") {
					val = "TRUE"
				}
				err := internal.SetVMXKey(vm.Path, "mks.enable3d", val)
				if err != nil {
					internal.LogError(internal.ErrDisplayConfig, vm.Name, "%s", err)
					continue
				}
				internal.LogInfo(vm.Name, "3D Acceleration: %s", accel3dFlag)
			}
			if gfxMemFlag > 0 {
				kb := internal.MBtoKB(gfxMemFlag)
				err := internal.SetVMXKey(vm.Path, "svga.graphicsMemoryKB", fmt.Sprintf("%d", kb))
				if err != nil {
					internal.LogError(internal.ErrDisplayConfig, vm.Name, "%s", err)
					continue
				}
				internal.LogInfo(vm.Name, "Graphics Memory: %d MB (%d KB)", gfxMemFlag, kb)
			}
		}
	},
}

// ---------------------------------------------------------------------------
// config os
// ---------------------------------------------------------------------------

var configOsCmd = &cobra.Command{
	Use:   "os [vm-names...]",
	Short: "Get or set the guestOS VMX key",
	Run: func(cmd *cobra.Command, args []string) {
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		for _, vm := range targets {
			if osSetFlag != "" {
				if vm.Running {
					internal.LogError(internal.ErrOSConfig, vm.Name, "must be powered off to change guestOS")
					continue
				}
				if err := internal.SetVMXKey(vm.Path, "guestOS", osSetFlag); err != nil {
					internal.LogError(internal.ErrOSConfig, vm.Name, "%s", err)
					continue
				}
				internal.LogInfo(vm.Name, "guestOS: %s", osSetFlag)
			} else {
				guestOS := internal.GetGuestOS(vm.Path)
				if guestOS == "" {
					fmt.Printf("%s → guestOS: (not set) — use \"rift config os %s --set <guestOS>\" to set it\n", vm.Name, vm.Name)
				} else {
					fmt.Printf("%s → guestOS: %s\n", vm.Name, guestOS)
				}
			}
		}
	},
}

// ---------------------------------------------------------------------------
// networks
// ---------------------------------------------------------------------------

var networksCmd = &cobra.Command{
	Use:   "networks",
	Short: "List available virtual networks and LAN segments",
	Run: func(cmd *cobra.Command, args []string) {
		requireSettings()

		networks, err := internal.LoadVirtualNetworks(settings.NetmapPath)
		exitOnErr(err)

		fmt.Println("Virtual Networks:")
		for _, net := range networks {
			fmt.Printf("  %s - %s (%s)\n", net.Device, net.Name, net.Type)
		}

		pvnNames := internal.LoadPVNNames(settings.VmInventory)
		if len(pvnNames) > 0 {
			fmt.Println("\nLAN Segments:")
			for _, name := range pvnNames {
				fmt.Printf("  %s\n", name)
			}
		}
	},
}

// ---------------------------------------------------------------------------
// config nic
// ---------------------------------------------------------------------------

var configNicCmd = &cobra.Command{
	Use:   "nic [vm-names...]",
	Short: "Change NIC settings",
	Run: func(cmd *cobra.Command, args []string) {
		if !addNicFlag && !removeNicFlag && !regenMacFlag && nicTypeFlag == "" {
			fmt.Println("Specify an action: --type, --regen-mac, --add, or --remove")
			return
		}
		if addNicFlag && nicTypeFlag == "" {
			fmt.Println("--type is required when adding a NIC")
			return
		}

		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		for _, vm := range targets {
			if addNicFlag {
				if !requireOff(vm, "add NIC") {
					continue
				}
				index, err := internal.AddNIC(vm.Path, nicTypeFlag, vnetFlag)
				if err != nil {
					internal.LogError(internal.ErrNetConfig, vm.Name, "%s", err)
					continue
				}
				internal.LogInfo(vm.Name, "added NIC %d (%s)", index, nicTypeFlag)

			} else if removeNicFlag {
				if !requireOff(vm, "remove NIC") {
					continue
				}
				err := internal.RemoveNIC(vm.Path, nicIndexFlag)
				if err != nil {
					internal.LogError(internal.ErrNetConfig, vm.Name, "%s", err)
					continue
				}
				internal.LogInfo(vm.Name, "removed NIC %d", nicIndexFlag)

			} else if regenMacFlag {
				if !requireOff(vm, "regenerate MAC") {
					continue
				}
				err := internal.RegenMAC(vm.Path, nicIndexFlag)
				if err != nil {
					internal.LogError(internal.ErrNetConfig, vm.Name, "%s", err)
					continue
				}
				internal.LogInfo(vm.Name, "NIC %d MAC will regenerate on next boot", nicIndexFlag)

			} else {
				if vm.Running && internal.RequiresPowerOff["nic-type"] {
					internal.LogError(internal.ErrNetConfig, vm.Name, "must be powered off to change NIC type")
					continue
				}
				err := internal.SetNICType(vm.Path, nicIndexFlag, nicTypeFlag, vnetFlag)
				if err != nil {
					internal.LogError(internal.ErrNetConfig, vm.Name, "%s", err)
					continue
				}
				internal.LogInfo(vm.Name, "NIC %d changed to %s", nicIndexFlag, nicTypeFlag)
			}
		}
	},
}

// ---------------------------------------------------------------------------
// config disk — subcommands
// ---------------------------------------------------------------------------

var configDiskCmd = &cobra.Command{
	Use:   "disk",
	Short: "Manage virtual disks",
}

var configDiskAddCmd = &cobra.Command{
	Use:   "add [vm-names...]",
	Short: "Add a new disk",
	Run: func(cmd *cobra.Command, args []string) {
		if diskSizeFlag == 0 || diskControllerFlag == "" {
			fmt.Println("--size and --controller are required")
			return
		}
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		// Hardware validation for pre-allocated disks (bit 2 set)
		if diskTypeFlag&2 != 0 {
			host, _ := internal.DetectHostResources()
			for _, vm := range targets {
				if err := internal.ValidateDiskSpace(host, vm.Path, diskSizeFlag); err != nil {
					internal.LogError(internal.ErrDiskConfig, vm.Name, "%s", err)
					return
				}
			}
		}

		for _, vm := range targets {
			if !requireOff(vm, "add a disk") {
				continue
			}
			slot, err := internal.CreateDisk(settings.VdiskPath, vm.Path, diskControllerFlag, diskSizeFlag, diskTypeFlag)
			if err != nil {
				internal.LogError(internal.ErrDiskConfig, vm.Name, "%s", err)
				continue
			}
			internal.LogInfo(vm.Name, "added disk at %s (%dGB)", slot, diskSizeFlag)
		}
	},
}

var configDiskRemoveCmd = &cobra.Command{
	Use:   "remove [vm-names...]",
	Short: "Remove a disk",
	Run: func(cmd *cobra.Command, args []string) {
		if diskControllerFlag == "" {
			fmt.Println("--controller is required")
			return
		}
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		for _, vm := range targets {
			if !requireOff(vm, "remove a disk") {
				continue
			}
			err := internal.RemoveDisk(vm.Path, diskControllerFlag, diskSlotFlag, deleteFilesFlag)
			if err != nil {
				internal.LogError(internal.ErrDiskConfig, vm.Name, "%s", err)
				continue
			}
			internal.LogInfo(vm.Name, "removed disk %s:%d", diskControllerFlag, diskSlotFlag)
		}
	},
}

var configDiskExpandCmd = &cobra.Command{
	Use:   "expand [vm-names...]",
	Short: "Expand a disk",
	Run: func(cmd *cobra.Command, args []string) {
		if diskSizeFlag == 0 || diskControllerFlag == "" {
			fmt.Println("--size and --controller are required")
			return
		}
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		for _, vm := range targets {
			if !requireOff(vm, "expand a disk") {
				continue
			}
			err := internal.ExpandDisk(settings.VdiskPath, vm.Path, diskControllerFlag, diskSlotFlag, diskSizeFlag)
			if err != nil {
				internal.LogError(internal.ErrDiskConfig, vm.Name, "%s", err)
				continue
			}
			internal.LogInfo(vm.Name, "expanded %s:%d to %dGB", diskControllerFlag, diskSlotFlag, diskSizeFlag)
		}
	},
}

var configDiskDefragCmd = &cobra.Command{
	Use:   "defrag [vm-names...]",
	Short: "Defragment a disk",
	Run: func(cmd *cobra.Command, args []string) {
		if diskControllerFlag == "" {
			fmt.Println("--controller is required")
			return
		}
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		for _, vm := range targets {
			if !requireOff(vm, "defragment a disk") {
				continue
			}
			err := internal.DefragDisk(settings.VdiskPath, vm.Path, diskControllerFlag, diskSlotFlag)
			if err != nil {
				internal.LogError(internal.ErrDiskConfig, vm.Name, "%s", err)
				continue
			}
			internal.LogInfo(vm.Name, "defragmented %s:%d", diskControllerFlag, diskSlotFlag)
		}
	},
}

var configDiskCompactCmd = &cobra.Command{
	Use:   "compact [vm-names...]",
	Short: "Compact a disk",
	Run: func(cmd *cobra.Command, args []string) {
		if diskControllerFlag == "" {
			fmt.Println("--controller is required")
			return
		}
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		for _, vm := range targets {
			if !requireOff(vm, "compact a disk") {
				continue
			}
			err := internal.CompactDisk(settings.VdiskPath, vm.Path, diskControllerFlag, diskSlotFlag)
			if err != nil {
				internal.LogError(internal.ErrDiskConfig, vm.Name, "%s", err)
				continue
			}
			internal.LogInfo(vm.Name, "compacted %s:%d", diskControllerFlag, diskSlotFlag)
		}
	},
}

// ---------------------------------------------------------------------------
// isos
// ---------------------------------------------------------------------------

var isosCmd = &cobra.Command{
	Use:   "isos",
	Short: "List available ISO images",
	Run: func(cmd *cobra.Command, args []string) {
		requireSettings()
		isos, err := internal.ListISOs(settings.IsoDirectory)
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

// ---------------------------------------------------------------------------
// config cdrom — subcommands
// ---------------------------------------------------------------------------

var configCdromCmd = &cobra.Command{
	Use:   "cdrom",
	Short: "Manage CD/DVD drives",
}

var configCdromMountCmd = &cobra.Command{
	Use:   "mount [vm-names...]",
	Short: "Mount an ISO image",
	Run: func(cmd *cobra.Command, args []string) {
		if isoFlag == "" || cdromControllerFlag == "" {
			fmt.Println("--iso and --controller are required")
			return
		}
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		isoPath := filepath.Join(settings.IsoDirectory, isoFlag)
		if _, err := os.Stat(isoPath); os.IsNotExist(err) {
			internal.LogError(internal.ErrInvalidPath, "", "ISO '%s' not found in %s", isoFlag, settings.IsoDirectory)
			return
		}

		for _, vm := range targets {
			err := internal.MountISO(vm.Path, cdromControllerFlag, cdromSlotFlag, isoPath)
			if err != nil {
				internal.LogError(internal.ErrCDVDConfig, vm.Name, "%s", err)
				continue
			}
			internal.LogInfo(vm.Name, "mounted %s on %s:%d", isoFlag, cdromControllerFlag, cdromSlotFlag)
		}
	},
}

var configCdromUnmountCmd = &cobra.Command{
	Use:   "unmount [vm-names...]",
	Short: "Unmount ISO and set to auto detect",
	Run: func(cmd *cobra.Command, args []string) {
		if cdromControllerFlag == "" {
			fmt.Println("--controller is required")
			return
		}
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		for _, vm := range targets {
			err := internal.UnmountISO(vm.Path, cdromControllerFlag, cdromSlotFlag)
			if err != nil {
				internal.LogError(internal.ErrCDVDConfig, vm.Name, "%s", err)
				continue
			}
			internal.LogInfo(vm.Name, "unmounted %s:%d", cdromControllerFlag, cdromSlotFlag)
		}
	},
}

var configCdromBootCmd = &cobra.Command{
	Use:   "boot [vm-names...]",
	Short: "Set connect at power on",
	Run: func(cmd *cobra.Command, args []string) {
		if cdromControllerFlag == "" {
			fmt.Println("--controller is required")
			return
		}
		if !bootConnectFlag && !noBootConnectFlag {
			fmt.Println("--on or --off is required")
			return
		}
		requireSettings()
		targets, err := internal.ResolveTargets(hv, folderFlag, args)
		exitOnErr(err)

		connect := bootConnectFlag

		for _, vm := range targets {
			err := internal.SetCDROMBoot(vm.Path, cdromControllerFlag, cdromSlotFlag, connect)
			if err != nil {
				internal.LogError(internal.ErrCDVDConfig, vm.Name, "%s", err)
				continue
			}
			state := "on"
			if !connect {
				state = "off"
			}
			internal.LogInfo(vm.Name, "%s:%d connect at boot: %s", cdromControllerFlag, cdromSlotFlag, state)
		}
	},
}

// ---------------------------------------------------------------------------
// hwinfo — show detected host resources
// ---------------------------------------------------------------------------

var hwinfoCmd = &cobra.Command{
	Use:   "hwinfo",
	Short: "Show detected host hardware resources",
	Run: func(cmd *cobra.Command, args []string) {
		host, err := internal.DetectHostResources()
		if err != nil {
			internal.LogError(internal.ErrConfig, "", "%s", err)
			return
		}
		fmt.Printf("CPU: %d cores, %d logical processors\n", host.CPUCores, host.CPUThreads)
		fmt.Printf("RAM: %d GB\n", host.TotalRAMGB)
		if len(host.FreeDiskGB) > 0 {
			fmt.Println("Disk:")
			for drive, free := range host.FreeDiskGB {
				fmt.Printf("  %s %d GB free\n", drive, free)
			}
		}
	},
}

// ---------------------------------------------------------------------------
// errors
// ---------------------------------------------------------------------------

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
			{"VM6", "Environment/Settings"},
			{"VM7", "File/VMX Operations"},
		}
		byPrefix := map[string][]internal.ErrorRef{}
		for _, e := range internal.ErrorCodes {
			p := e.Code[:3]
			byPrefix[p] = append(byPrefix[p], e)
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

// ---------------------------------------------------------------------------
// Execute & init
// ---------------------------------------------------------------------------

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	// Top-level commands
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(infoCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(suspendCmd)
	rootCmd.AddCommand(resetCmd)
	rootCmd.AddCommand(execCmd)
	rootCmd.AddCommand(bootstrapCmd)
	bootstrapCmd.AddCommand(bootstrapCreateCmd)
	bootstrapCmd.AddCommand(bootstrapVerifyCmd)
	bootstrapCmd.AddCommand(bootstrapResetCmd)
	bootstrapCmd.AddCommand(bootstrapRevokeCmd)
	rootCmd.AddCommand(networksCmd)
	rootCmd.AddCommand(isosCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(hwinfoCmd)
	rootCmd.AddCommand(errorsCmd)
	rootCmd.AddCommand(snapshotCmd)
	rootCmd.AddCommand(archiveCmd)

	// Archive subcommands
	archiveCmd.AddCommand(archiveExportCmd)
	archiveCmd.AddCommand(archiveImportCmd)
	archiveCmd.AddCommand(archiveListCmd)
	archiveCmd.AddCommand(archiveDeleteCmd)

	// Snapshot subcommands
	snapshotCmd.AddCommand(snapshotCreateCmd)
	snapshotCmd.AddCommand(snapshotListCmd)
	snapshotCmd.AddCommand(snapshotRevertCmd)
	snapshotCmd.AddCommand(snapshotDeleteCmd)

	// Config subcommands
	configCmd.AddCommand(configCpuCmd)
	configCmd.AddCommand(configRamCmd)
	configCmd.AddCommand(configNicCmd)
	configCmd.AddCommand(configDiskCmd)
	configCmd.AddCommand(configCdromCmd)
	configCmd.AddCommand(configDisplayCmd)
	configCmd.AddCommand(configOsCmd)

	// Disk subcommands
	configDiskCmd.AddCommand(configDiskAddCmd)
	configDiskCmd.AddCommand(configDiskRemoveCmd)
	configDiskCmd.AddCommand(configDiskExpandCmd)
	configDiskCmd.AddCommand(configDiskDefragCmd)
	configDiskCmd.AddCommand(configDiskCompactCmd)

	// CD/DVD subcommands
	configCdromCmd.AddCommand(configCdromMountCmd)
	configCdromCmd.AddCommand(configCdromUnmountCmd)
	configCdromCmd.AddCommand(configCdromBootCmd)

	// --- Flags ---

	// Folder flag on all commands that support it
	for _, cmd := range []*cobra.Command{
		startCmd, stopCmd, suspendCmd, resetCmd, execCmd, infoCmd,
		bootstrapCmd, bootstrapCreateCmd, bootstrapVerifyCmd, bootstrapResetCmd, bootstrapRevokeCmd,
		configCpuCmd, configRamCmd, configNicCmd, configDisplayCmd, configOsCmd,
		configDiskAddCmd, configDiskRemoveCmd, configDiskExpandCmd,
		configDiskDefragCmd, configDiskCompactCmd,
		configCdromMountCmd, configCdromUnmountCmd, configCdromBootCmd,
		snapshotCreateCmd, snapshotListCmd, snapshotRevertCmd, snapshotDeleteCmd,
		archiveExportCmd,
	} {
		cmd.Flags().StringVarP(&folderFlag, "folder", "f", "", "Apply to all VMs in a folder")
	}

	// Snapshot
	snapshotCreateCmd.Flags().StringVarP(&snapshotNameFlag, "name", "n", "", "Snapshot name (default: {vmname}-YYYYMMDD-HHmmss)")
	snapshotRevertCmd.Flags().BoolVar(&snapshotOriginFlag, "origin", false, "Revert to first snapshot then delete all snapshots")
	snapshotRevertCmd.Flags().BoolVarP(&yesFlag, "yes", "y", false, "Skip confirmation prompt")
	snapshotDeleteCmd.Flags().BoolVar(&snapshotCurrentFlag, "current", false, "Delete all snapshots")
	snapshotDeleteCmd.Flags().BoolVarP(&yesFlag, "yes", "y", false, "Skip confirmation prompt")

	// Archive
	archiveExportCmd.Flags().StringVarP(&archiveFormatFlag, "format", "F", "", "Export format: ovf or ova (required)")
	archiveExportCmd.Flags().StringVarP(&archiveNameFlag, "name", "n", "", "Override archive name (default: VM name)")
	archiveImportCmd.Flags().StringVarP(&archiveNameFlag, "name", "n", "", "VM name for the imported VM (default: source filename)")
	archiveImportCmd.Flags().StringVarP(&archiveFormatFlag, "format", "F", "", "Filter by format (ovf or ova) when using --latest/--oldest")
	archiveImportCmd.Flags().BoolVarP(&archiveLatestFlag, "latest", "l", false, "Select the newest archive for the given VM name")
	archiveImportCmd.Flags().BoolVarP(&archiveOldestFlag, "oldest", "o", false, "Select the oldest archive for the given VM name")
	archiveDeleteCmd.Flags().StringVarP(&archiveFormatFlag, "format", "F", "", "Narrow deletion to a specific format (ovf or ova)")
	archiveDeleteCmd.Flags().BoolVarP(&archiveLatestFlag, "latest", "l", false, "Select the newest archive for the given VM name")
	archiveDeleteCmd.Flags().BoolVarP(&archiveOldestFlag, "oldest", "o", false, "Select the oldest archive for the given VM name")
	archiveDeleteCmd.Flags().BoolVarP(&yesFlag, "yes", "y", false, "Skip confirmation prompt")

	// Info flags
	infoCmd.Flags().BoolVarP(&netFlag, "net", "n", false, "Show NIC details only")
	infoCmd.Flags().BoolVarP(&specsFlag, "specs", "s", false, "Show CPU/RAM details only")
	infoCmd.Flags().BoolVarP(&diskFlag, "disk", "d", false, "Show disk details only")
	infoCmd.Flags().BoolVarP(&cdromFlag, "cdrom", "C", false, "Show CD/DVD details only")
	infoCmd.Flags().BoolVarP(&displayFlag, "display", "D", false, "Show display details only")

	// Stop
	stopCmd.Flags().BoolVarP(&hardFlag, "hard", "H", false, "Force power off")

	// CPU
	configCpuCmd.Flags().IntVarP(&coresFlag, "cores", "c", 0, "Cores per socket")
	configCpuCmd.Flags().IntVarP(&socketsFlag, "sockets", "S", 0, "Number of sockets")

	// RAM
	configRamCmd.Flags().IntVarP(&ramFlag, "size", "m", 0, "RAM size in GB")

	// NIC
	configNicCmd.Flags().IntVarP(&nicIndexFlag, "index", "i", 0, "NIC index")
	configNicCmd.Flags().StringVarP(&nicTypeFlag, "type", "t", "", "NIC type (bridged, nat, hostonly, custom)")
	configNicCmd.Flags().StringVarP(&vnetFlag, "vnet", "v", "", "Virtual network name (for custom type)")
	configNicCmd.Flags().BoolVarP(&regenMacFlag, "regen-mac", "r", false, "Regenerate MAC address")
	configNicCmd.Flags().BoolVarP(&addNicFlag, "add", "a", false, "Add a new NIC")
	configNicCmd.Flags().BoolVarP(&removeNicFlag, "remove", "R", false, "Remove a NIC")

	// Disk add
	configDiskAddCmd.Flags().IntVarP(&diskSizeFlag, "size", "s", 0, "Disk size in GB")
	configDiskAddCmd.Flags().IntVarP(&diskTypeFlag, "type", "t", 0, "Disk type bitmask: 1=split, 2=pre-allocated (combine: 0=single+growable, 1=split+growable, 2=single+pre-allocated, 3=split+pre-allocated)")
	configDiskAddCmd.Flags().StringVarP(&diskControllerFlag, "controller", "c", "", "Controller (scsi0, sata0, nvme0, ide0)")

	// Disk remove
	configDiskRemoveCmd.Flags().StringVarP(&diskControllerFlag, "controller", "c", "", "Controller")
	configDiskRemoveCmd.Flags().IntVarP(&diskSlotFlag, "slot", "S", 0, "Disk slot number")
	configDiskRemoveCmd.Flags().BoolVarP(&deleteFilesFlag, "delete-files", "D", false, "Also delete VMDK files")

	// Disk expand
	configDiskExpandCmd.Flags().IntVarP(&diskSizeFlag, "size", "s", 0, "New disk size in GB")
	configDiskExpandCmd.Flags().StringVarP(&diskControllerFlag, "controller", "c", "", "Controller")
	configDiskExpandCmd.Flags().IntVarP(&diskSlotFlag, "slot", "S", 0, "Disk slot number")

	// Disk defrag
	configDiskDefragCmd.Flags().StringVarP(&diskControllerFlag, "controller", "c", "", "Controller")
	configDiskDefragCmd.Flags().IntVarP(&diskSlotFlag, "slot", "S", 0, "Disk slot number")

	// Disk compact
	configDiskCompactCmd.Flags().StringVarP(&diskControllerFlag, "controller", "c", "", "Controller")
	configDiskCompactCmd.Flags().IntVarP(&diskSlotFlag, "slot", "S", 0, "Disk slot number")

	// CD/DVD mount
	configCdromMountCmd.Flags().StringVarP(&isoFlag, "iso", "i", "", "ISO filename from ISO directory")
	configCdromMountCmd.Flags().StringVarP(&cdromControllerFlag, "controller", "c", "", "Controller (e.g. sata0)")
	configCdromMountCmd.Flags().IntVarP(&cdromSlotFlag, "slot", "S", 0, "Slot number")

	// CD/DVD unmount
	configCdromUnmountCmd.Flags().StringVarP(&cdromControllerFlag, "controller", "c", "", "Controller")
	configCdromUnmountCmd.Flags().IntVarP(&cdromSlotFlag, "slot", "S", 0, "Slot number")

	// CD/DVD boot
	configCdromBootCmd.Flags().StringVarP(&cdromControllerFlag, "controller", "c", "", "Controller")
	configCdromBootCmd.Flags().IntVarP(&cdromSlotFlag, "slot", "S", 0, "Slot number")
	configCdromBootCmd.Flags().BoolVar(&bootConnectFlag, "on", false, "Connect at power on")
	configCdromBootCmd.Flags().BoolVar(&noBootConnectFlag, "off", false, "Don't connect at power on")

	// Display
	configDisplayCmd.Flags().StringVarP(&accel3dFlag, "accel3d", "a", "", "3D acceleration (on/off)")
	configDisplayCmd.Flags().IntVarP(&gfxMemFlag, "gfxmem", "g", 0, "Graphics memory in MB")

	// OS
	configOsCmd.Flags().StringVarP(&osSetFlag, "set", "s", "", "guestOS value to write into the VMX")

	// Exec
	execCmd.Flags().StringVarP(&execUserFlag, "user", "u", "", "Guest OS username (required)")
	execCmd.Flags().StringVarP(&execPassFlag, "pass", "p", "", "Guest OS password (required)")
	execCmd.Flags().StringVarP(&execInterpreterFlag, "interpreter", "i", "", "Script interpreter (e.g. /bin/bash, C:\\Windows\\System32\\cmd.exe; default: auto-detect)")

	// Bootstrap — persistent flags shared by all subcommands
	bootstrapCmd.PersistentFlags().StringVarP(&bootstrapUserFlag, "user", "u", "", "Guest OS username with admin/root privileges")
	bootstrapCmd.PersistentFlags().StringVarP(&bootstrapPassFlag, "pass", "p", "", "Guest OS password")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapRunnerUserFlag, "runner-user", "", "Automation username to act on (default: runner)")
	// --runner-pass on parent (alias) and on subcommands that require it
	bootstrapCmd.Flags().StringVar(&bootstrapRunnerPassFlag, "runner-pass", "", "Password to set for the automation user (required)")
	bootstrapCreateCmd.Flags().StringVar(&bootstrapRunnerPassFlag, "runner-pass", "", "Password to set for the automation user (required)")
	bootstrapResetCmd.Flags().StringVar(&bootstrapRunnerPassFlag, "runner-pass", "", "New password for the automation user (required)")
}
