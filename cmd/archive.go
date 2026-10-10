package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/J0sh0909/rift/internal/core"
	"github.com/spf13/cobra"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

var (
	archiveFormatFlag string
	archiveNameFlag   string
	archiveLatestFlag bool
	archiveOldestFlag bool
)

var archiveCmd = &cobra.Command{
	Use:   "archive",
	Short: "Archive VM exports",
}

// archiveHVID is the hypervisor tag embedded in archive names (-vmw-, -vbox-).
var archiveHVID = map[string]string{
	"workstation": "vmw",
	"vbox":        "vbox",
	"proxmox":     "pve",
}

var archiveExportCmd = &cobra.Command{
	Use:   "export [vm-names...]",
	Short: "Export a VM as OVF or OVA",
	Run: func(cmd *cobra.Command, args []string) {
		if archiveFormatFlag == "" {
			fail(core.ErrArchive, "--format is required (ovf or ova)")
			return
		}
		format, err := core.ValidateFormat(archiveFormatFlag)
		if err != nil {
			fail(core.ErrArchive, "%s", err)
			return
		}
		if archiveNameFlag != "" {
			if err := core.ValidateVMName(archiveNameFlag); err != nil {
				fail(core.ErrArchive, "invalid --name: %s", err)
				return
			}
		}
		s := loadSettings()
		if s.ArchivePath == "" {
			fail(core.ErrMissingSetting, "ARCHIVE_PATH is not set in .env")
			return
		}
		targets := resolveTargets(args, needTargets)
		ts := time.Now().Format("20060102-150405")
		multiple := len(targets) > 1

		// destPath builds the destination for a VM and creates its directory.
		destPath := func(t core.Target, hvID string) (string, error) {
			label := t.Name
			if archiveNameFlag != "" {
				label = archiveNameFlag
				if multiple {
					label = archiveNameFlag + "-" + t.Name
				}
			}
			folderPart := t.Folder
			if folderPart == "." {
				folderPart = ""
			}
			var dir, file string
			switch format {
			case "ovf":
				dir = filepath.Join(s.ArchivePath, "OVF", folderPart, label, label+"-"+hvID+"-"+ts)
				file = label + ".ovf"
			default:
				dir = filepath.Join(s.ArchivePath, "OVA", folderPart, label)
				file = label + "-" + hvID + "-" + ts + ".ova"
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("failed to create export directory: %w", err)
			}
			return filepath.Join(dir, file), nil
		}

		// Prepare jobs; anything that can't be exported is a result already.
		type job struct {
			t    core.Target
			dest string
		}
		var jobs []job
		var results []result
		ovftoolChecked := map[core.Hypervisor]error{}
		for _, t := range targets {
			hvID, supported := archiveHVID[t.Backend]
			if !supported {
				results = append(results, skipped(t, core.ErrUnsupported, "OVF/OVA export is not supported on %s", core.BackendLabel(t.Backend)))
				continue
			}
			if r, ok := requireOff(t, "export", "export"); !ok {
				results = append(results, r)
				continue
			}
			if t.Backend == "workstation" {
				if _, seen := ovftoolChecked[t.HV]; !seen {
					_, ovftoolChecked[t.HV] = t.HV.FindOvftool()
				}
				if err := ovftoolChecked[t.HV]; err != nil {
					results = append(results, failed(t, core.ErrOvftoolNotFound, "%s", err))
					continue
				}
			}
			dest, err := destPath(t, hvID)
			if err != nil {
				results = append(results, failed(t, core.ErrExportFailed, "%s", err))
				continue
			}
			core.LogInfo(t.Name, "exporting as %s to %s", strings.ToUpper(format), dest)
			jobs = append(jobs, job{t, dest})
		}

		switch {
		case len(jobs) == 1 && !multiple:
			// Single VM: inline progress from ovftool.
			j := jobs[0]
			if err := j.t.HV.ExportVM(j.t.Path, j.dest); err != nil {
				results = append(results, failed(j.t, core.ErrExportFailed, "%s", err))
			} else {
				results = append(results, success(j.t, "export complete"))
			}
		case len(jobs) > 0:
			// Several VMs: parallel, one progress bar each.
			maxName := 0
			for _, j := range jobs {
				maxName = max(maxName, len(j.t.Name))
			}
			nameFmt := fmt.Sprintf("%%-%ds", maxName)
			jobResults := make([]result, len(jobs))
			p := mpb.New()
			var wg sync.WaitGroup
			for i, j := range jobs {
				bar := p.New(100,
					mpb.BarStyle().Lbound("[").Filler("=").Tip("").Padding(" ").Rbound("]"),
					mpb.BarWidth(50),
					mpb.PrependDecorators(decor.Name(fmt.Sprintf(nameFmt, j.t.Name))),
					mpb.AppendDecorators(decor.Percentage(decor.WCSyncSpaceR)),
				)
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := j.t.HV.ExportVMWithBar(j.t.Path, j.dest, bar); err != nil {
						bar.Abort(false)
						jobResults[i] = failed(j.t, core.ErrExportFailed, "%s", err)
						return
					}
					bar.SetCurrent(100)
					jobResults[i] = success(j.t, "export complete")
				}()
			}
			wg.Wait()
			p.Wait()
			results = append(results, jobResults...)
		}
		report(plan{verb: "export"}, results)
	},
}

// selectArchives picks archives by exact version name, or the newest/oldest
// for a VM name with --latest/--oldest, optionally filtered by --format.
func selectArchives(entries []core.ArchiveEntry, target string) ([]core.ArchiveEntry, error) {
	var fmtFilter string
	if archiveFormatFlag != "" {
		var err error
		if fmtFilter, err = core.ValidateFormat(archiveFormatFlag); err != nil {
			return nil, err
		}
	}
	byVM := archiveLatestFlag || archiveOldestFlag
	var matches []core.ArchiveEntry
	for _, e := range entries {
		if byVM && e.VMName != target || !byVM && e.Version != target {
			continue
		}
		if fmtFilter != "" && strings.ToLower(e.Format) != fmtFilter {
			continue
		}
		matches = append(matches, e)
	}
	if len(matches) == 0 {
		if byVM {
			return nil, fmt.Errorf("no archives found for VM %q", target)
		}
		return nil, fmt.Errorf("no archive found with name %q", target)
	}
	if byVM {
		sort.Slice(matches, func(i, j int) bool { return matches[i].Version < matches[j].Version })
		if archiveLatestFlag {
			return matches[len(matches)-1:], nil
		}
		return matches[:1], nil
	}
	return matches, nil
}

// archiveTarget validates import/delete arguments and scans the archive.
func archiveTarget(args []string, example string) (string, []core.ArchiveEntry, bool) {
	if archiveLatestFlag && archiveOldestFlag {
		fail(core.ErrArchive, "--latest and --oldest are mutually exclusive")
		return "", nil, false
	}
	if len(args) < 1 {
		if archiveLatestFlag || archiveOldestFlag {
			fail(core.ErrArchive, "specify the VM name (e.g. H74-RT)")
		} else {
			fail(core.ErrArchive, "specify the archive name (e.g. %s)", example)
		}
		return "", nil, false
	}
	s := loadSettings()
	if s.ArchivePath == "" {
		fail(core.ErrMissingSetting, "ARCHIVE_PATH is not set in .env")
		return "", nil, false
	}
	entries, err := core.ScanArchives(s.ArchivePath)
	if err != nil {
		fail(core.ErrArchive, "%s", err)
		return "", nil, false
	}
	return args[0], entries, true
}

var archiveImportCmd = &cobra.Command{
	Use:   "import <archive-name>",
	Short: "Import an archived VM into VMware",
	Run: func(cmd *cobra.Command, args []string) {
		target, entries, ok := archiveTarget(args, "H74-RT-20260302-181915")
		if !ok {
			return
		}
		hv := defaultBackend()
		if _, err := hv.FindOvftool(); err != nil {
			fail(core.ErrOvftoolNotFound, "%s", err)
			return
		}
		matches, err := selectArchives(entries, target)
		if err != nil {
			fail(core.ErrArchive, "%s", err)
			return
		}

		chosen := matches[0]
		if len(matches) > 1 {
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
		srcPath := chosen.Path
		if chosen.Format == "OVF" {
			ovfFiles, err := filepath.Glob(filepath.Join(chosen.Path, "*.ovf"))
			if err != nil || len(ovfFiles) == 0 {
				fail(core.ErrImportFailed, "no .ovf file found in %s", chosen.Path)
				return
			}
			srcPath = ovfFiles[0]
		}

		name := archiveNameFlag
		if name == "" {
			name = chosen.VMName
		}
		if err := core.ValidateVMName(name); err != nil {
			fail(core.ErrImportFailed, "invalid VM name %q: %s - use --name to specify a valid name", name, err)
			return
		}

		destDir := filepath.Join(settings.VmDirectory, name)
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			fail(core.ErrImportFailed, "failed to create VM directory: %s", err)
			return
		}
		destVmx := filepath.Join(destDir, name+".vmx")

		fmt.Printf("importing [%s] %s → %s\n", chosen.Format, chosen.Version, destVmx)
		if err := hv.ImportVM(srcPath, destVmx); err != nil {
			fail(core.ErrImportFailed, "%s: %s", name, err)
			return
		}
		core.LogInfo(name, "import complete")

		core.LogInfo(name, "registering with VMware...")
		if err := hv.StartVM(destVmx); err != nil {
			fail(core.ErrImportFailed, "%s: failed to register (start): %s", name, err)
			return
		}
		if err := hv.StopVM(destVmx, "hard"); err != nil {
			fail(core.ErrImportFailed, "%s: failed to register (stop): %s", name, err)
			return
		}
		core.LogInfo(name, "registered and powered off")
	},
}

var archiveListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all VM archives",
	Run: func(cmd *cobra.Command, args []string) {
		s := loadSettings()
		if s.ArchivePath == "" {
			fail(core.ErrMissingSetting, "ARCHIVE_PATH is not set in .env")
			return
		}
		entries, err := core.ScanArchives(s.ArchivePath)
		if err != nil {
			fail(core.ErrArchive, "%s", err)
			return
		}
		if len(entries) == 0 {
			fmt.Println("No archives found")
			return
		}

		type groupKey struct{ Folder, VMName string }
		var order []groupKey
		groups := map[groupKey][]core.ArchiveEntry{}
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
		target, entries, ok := archiveTarget(args, "win11-dev-20260302-143022")
		if !ok {
			return
		}
		matches, err := selectArchives(entries, target)
		if err != nil {
			fail(core.ErrArchive, "%s", err)
			return
		}

		fmt.Printf("WARNING: will permanently delete %d archive(s):\n", len(matches))
		for _, m := range matches {
			fmt.Printf("  [%s] %s (%s)\n", m.Format, m.Version, formatBytes(m.SizeBytes))
		}
		if !confirm("") {
			return
		}
		for _, m := range matches {
			if err := os.RemoveAll(m.Path); err != nil {
				fail(core.ErrArchive, "failed to delete [%s] %s: %s", m.Format, m.Version, err)
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

func init() {
	rootCmd.AddCommand(archiveCmd)
	archiveCmd.AddCommand(archiveExportCmd, archiveImportCmd, archiveListCmd, archiveDeleteCmd)
	addFolderFlag(archiveExportCmd)

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
}
