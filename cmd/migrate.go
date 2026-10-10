package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/J0sh0909/rift/internal/core"
	"github.com/J0sh0909/rift/internal/vbox"
	"github.com/spf13/cobra"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

// ---------------------------------------------------------------------------
// Migrate flags
// ---------------------------------------------------------------------------

var (
	migrateFromFlag   string
	migrateToFlag     string
	migrateFolderFlag string
)

// ---------------------------------------------------------------------------
// qemu-img helper
// ---------------------------------------------------------------------------

func findQemuImg() (string, error) {
	s, _ := core.LoadSettings()
	if s.QemuImgPath != "" {
		if _, err := os.Stat(s.QemuImgPath); err == nil {
			return s.QemuImgPath, nil
		}
	}
	p, err := exec.LookPath("qemu-img")
	if err != nil {
		return "", fmt.Errorf("qemu-img not found - set QEMU_IMG_PATH in .env or add to PATH")
	}
	return p, nil
}

// convertDiskWithBar runs qemu-img convert, updating an mpb bar by monitoring
// output file size growth relative to the source disk size.
func convertDiskWithBar(qemuImg, srcPath, srcFmt, dstPath, dstFmt string, bar *mpb.Bar) error {
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat source disk: %s", err)
	}
	srcSize := srcInfo.Size()
	if srcSize == 0 {
		srcSize = 1
	}

	args := []string{"convert", "-f", srcFmt, "-O", dstFmt, srcPath, dstPath}
	cmd := exec.Command(qemuImg, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if fi, err := os.Stat(dstPath); err == nil {
					pct := int64(fi.Size() * 100 / srcSize)
					if pct > 99 {
						pct = 99
					}
					bar.SetCurrent(pct)
				}
			}
		}
	}()

	err = cmd.Wait()
	close(done)
	if err != nil {
		bar.Abort(false)
		return err
	}
	bar.SetCurrent(100)
	return nil
}

// ---------------------------------------------------------------------------
// OS type mapping
// ---------------------------------------------------------------------------

// vmwareToVBoxOS maps common VMware guestOS values to VirtualBox ostype values.
func vmwareToVBoxOS(guestOS string) string {
	g := strings.ToLower(guestOS)
	switch {
	case strings.Contains(g, "windows11"):
		return "Windows11_64"
	case strings.Contains(g, "windows10"):
		return "Windows10_64"
	case strings.Contains(g, "windows-server-2022"), strings.Contains(g, "windows2022"):
		return "Windows2022_64"
	case strings.Contains(g, "windows-server-2019"), strings.Contains(g, "windows2019"):
		return "Windows2019_64"
	case strings.Contains(g, "windows"):
		return "Windows10_64"
	case strings.Contains(g, "ubuntu-64"), strings.Contains(g, "ubuntu"):
		return "Ubuntu_64"
	case strings.Contains(g, "debian"):
		return "Debian_64"
	case strings.Contains(g, "centos"):
		return "RedHat_64"
	case strings.Contains(g, "fedora"):
		return "Fedora_64"
	case strings.Contains(g, "rhel"), strings.Contains(g, "redhat"):
		return "RedHat_64"
	case strings.Contains(g, "linux"):
		return "Linux_64"
	default:
		return "Other_64"
	}
}

// vboxToVMwareOS maps common VirtualBox ostype values to VMware guestOS values.
func vboxToVMwareOS(osType string) string {
	o := strings.ToLower(osType)
	switch {
	case strings.Contains(o, "windows11"):
		return "windows11-64"
	case strings.Contains(o, "windows10"):
		return "windows10-64"
	case strings.Contains(o, "windows2022"):
		return "windows-server-2022"
	case strings.Contains(o, "windows2019"):
		return "windows-server-2019"
	case strings.Contains(o, "windows"):
		return "windows10-64"
	case strings.Contains(o, "ubuntu"):
		return "ubuntu-64"
	case strings.Contains(o, "debian"):
		return "debian12-64"
	case strings.Contains(o, "fedora"):
		return "fedora-64"
	case strings.Contains(o, "redhat"):
		return "rhel9-64"
	case strings.Contains(o, "linux"):
		return "other5xlinux-64"
	default:
		return "other-64"
	}
}

// nicConnVMwareToVBox maps VMware connection types to VBox equivalents.
func nicConnVMwareToVBox(connType string) string {
	switch {
	case strings.HasPrefix(strings.ToLower(connType), "bridged"):
		return "bridged"
	case strings.ToLower(connType) == "nat":
		return "nat"
	case strings.ToLower(connType) == "hostonly":
		return "hostonly"
	default:
		return "nat"
	}
}

// nicDevVMwareToVBox maps VMware virtual device types to VBox NIC types.
func nicDevVMwareToVBox(virtualDev string) string {
	switch strings.ToLower(virtualDev) {
	case "vmxnet3":
		return "virtio"
	case "e1000":
		return "82540EM"
	case "e1000e":
		return "82545EM"
	default:
		return "82540EM"
	}
}

// nicConnVBoxToVMware maps VBox connection types to VMware equivalents.
func nicConnVBoxToVMware(vboxType string) string {
	switch strings.ToLower(vboxType) {
	case "bridged":
		return "bridged"
	case "nat":
		return "nat"
	case "hostonly":
		return "hostonly"
	default:
		return "nat"
	}
}

// nicDevVBoxToVMware maps VBox NIC types to VMware virtual device types.
func nicDevVBoxToVMware(vboxNicType string) string {
	switch strings.ToLower(vboxNicType) {
	case "virtio":
		return "vmxnet3"
	case "82540em", "am79c970a", "am79c973":
		return "e1000"
	case "82545em":
		return "e1000e"
	default:
		return "e1000e"
	}
}

// ---------------------------------------------------------------------------
// rift migrate
// ---------------------------------------------------------------------------

var migrateCmd = &cobra.Command{
	Use:   "migrate [vm-name]",
	Short: "Migrate a VM between hypervisors",
	Long:  "Supported: --from vmware --to vbox, --from vbox --to vmware.\nUse --folder to migrate all VMs in a VMware folder (or all VBox VMs).",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		from := strings.ToLower(migrateFromFlag)
		to := strings.ToLower(migrateToFlag)

		if from == "" || to == "" {
			fmt.Fprintln(os.Stderr, "error: --from and --to are required")
			exit(1)
		}
		if from == to {
			fmt.Fprintln(os.Stderr, "error: --from and --to must be different")
			exit(1)
		}

		if migrateFolderFlag == "" && len(args) == 0 {
			fmt.Fprintln(os.Stderr, "error: provide a VM name or --folder")
			exit(1)
		}

		if migrateFolderFlag != "" {
			migrateFolderBatch(from, to)
			return
		}

		vmName := args[0]
		switch {
		case from == "vmware" && to == "vbox":
			migrateVMwareToVBox(vmName)
		case from == "vbox" && to == "vmware":
			migrateVBoxToVMware(vmName)
		default:
			fmt.Fprintf(os.Stderr, "error: unsupported migration path %s → %s\n", from, to)
			exit(1)
		}
	},
}

// workstationBackend returns the VMware Workstation backend regardless of
// HYPERVISOR, since migration names its source and target explicitly.
func workstationBackend() core.Hypervisor {
	h, err := core.CreateBackend("workstation", loadSettings())
	exitOnErr(err)
	return h
}

// ---------------------------------------------------------------------------
// Folder batch migration
// ---------------------------------------------------------------------------

type migrateResult struct {
	name string
	err  error
}

func migrateFolderBatch(from, to string) {
	switch {
	case from == "vmware" && to == "vbox":
		migrateFolderVMwareToVBox()
	case from == "vbox" && to == "vmware":
		migrateFolderVBoxToVMware()
	default:
		fmt.Fprintf(os.Stderr, "error: unsupported migration path %s → %s\n", from, to)
		exit(1)
	}
}

func migrateFolderVMwareToVBox() {
	vms, err := workstationBackend().GetPowerState()
	if err != nil {
		core.LogError(core.ErrSourceNotFound, "", "listing VMware VMs: %s", err)
		exit(1)
	}
	var targets []core.VM
	for _, vm := range vms {
		if strings.EqualFold(vm.Folder, migrateFolderFlag) {
			targets = append(targets, vm)
		}
	}
	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "no VMs found in folder '%s'\n", migrateFolderFlag)
		exit(1)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })

	maxName := 0
	for _, vm := range targets {
		if len(vm.Name) > maxName {
			maxName = len(vm.Name)
		}
	}
	nameFmt := fmt.Sprintf("%%-%ds", maxName)

	results := make([]migrateResult, len(targets))
	p := mpb.New()
	var wg sync.WaitGroup
	for i, vm := range targets {
		i, vm := i, vm
		bar := p.New(100,
			mpb.BarStyle().Lbound("[").Filler("=").Tip("").Padding(" ").Rbound("]"),
			mpb.BarWidth(40),
			mpb.PrependDecorators(
				decor.Name(fmt.Sprintf(nameFmt, vm.Name)),
			),
			mpb.AppendDecorators(
				decor.Percentage(decor.WCSyncSpaceR),
			),
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := migrateOneVMwareToVBox(vm, bar)
			results[i] = migrateResult{name: vm.Name, err: err}
		}()
	}
	wg.Wait()
	p.Wait()
	for _, r := range results {
		if r.err != nil {
			core.LogError(core.ErrMigration, r.name, "%s", r.err)
			failures++
		} else {
			fmt.Printf("%s → migrated to VirtualBox\n", r.name)
		}
	}
}

func migrateFolderVBoxToVMware() {
	loadSettings()
	vbox, err := newVBox()
	if err != nil {
		core.LogError(core.ErrSourceNotFound, "", "%s", err)
		exit(1)
	}
	vbVMs, err := vbox.ListVMs()
	if err != nil {
		core.LogError(core.ErrSourceNotFound, "", "listing VBox VMs: %s", err)
		exit(1)
	}
	if len(vbVMs) == 0 {
		fmt.Fprintln(os.Stderr, "no VirtualBox VMs found")
		exit(1)
	}
	sort.Slice(vbVMs, func(i, j int) bool { return vbVMs[i].Name < vbVMs[j].Name })

	maxName := 0
	for _, vm := range vbVMs {
		if len(vm.Name) > maxName {
			maxName = len(vm.Name)
		}
	}
	nameFmt := fmt.Sprintf("%%-%ds", maxName)

	results := make([]migrateResult, len(vbVMs))
	p := mpb.New()
	var wg sync.WaitGroup
	for i, vm := range vbVMs {
		i, vm := i, vm
		bar := p.New(100,
			mpb.BarStyle().Lbound("[").Filler("=").Tip("").Padding(" ").Rbound("]"),
			mpb.BarWidth(40),
			mpb.PrependDecorators(
				decor.Name(fmt.Sprintf(nameFmt, vm.Name)),
			),
			mpb.AppendDecorators(
				decor.Percentage(decor.WCSyncSpaceR),
			),
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := migrateOneVBoxToVMware(vm.Name, bar)
			results[i] = migrateResult{name: vm.Name, err: err}
		}()
	}
	wg.Wait()
	p.Wait()
	for _, r := range results {
		if r.err != nil {
			core.LogError(core.ErrMigration, r.name, "%s", r.err)
			failures++
		} else {
			fmt.Printf("%s → migrated to VMware Workstation\n", r.name)
		}
	}
}

// migrateOneVMwareToVBox migrates a single VMware VM to VBox. Returns nil on success.
// The provided mpb bar is updated with disk conversion progress.
func migrateOneVMwareToVBox(sourceVM core.VM, bar *mpb.Bar) error {
	if sourceVM.Running {
		return fmt.Errorf("VM must be powered off before migration")
	}

	specs, err := core.ParseVMXSpecs(sourceVM.Path)
	if err != nil {
		return fmt.Errorf("reading specs: %s", err)
	}
	vmxData, err := core.ParseVMXKeys(sourceVM.Path)
	if err != nil {
		return fmt.Errorf("reading VMX: %s", err)
	}
	guestOS := vmxData["guestos"]
	cpus, _ := strconv.Atoi(specs.CPUCount)
	if cpus < 1 {
		cpus = 1
	}
	ramMB, _ := strconv.Atoi(specs.MemoryMB)
	if ramMB < 512 {
		ramMB = 512
	}

	disks, err := core.ParseVMXDisks(sourceVM.Path)
	if err != nil || len(disks) == 0 {
		return fmt.Errorf("no disks found")
	}
	vmdkPath := disks[0].FileName
	if !filepath.IsAbs(vmdkPath) {
		vmdkPath = filepath.Join(filepath.Dir(sourceVM.Path), vmdkPath)
	}

	qemuImg, err := findQemuImg()
	if err != nil {
		return fmt.Errorf("%s", err)
	}

	vdiPath := filepath.Join(filepath.Dir(vmdkPath), sourceVM.Name+".vdi")
	if err := convertDiskWithBar(qemuImg, vmdkPath, "vmdk", vdiPath, "vdi", bar); err != nil {
		return fmt.Errorf("disk conversion: %s", err)
	}

	vbox, err := newVBox()
	if err != nil {
		return fmt.Errorf("VBoxManage: %s", err)
	}
	vboxOS := vmwareToVBoxOS(guestOS)
	if err := vbox.CreateVM(sourceVM.Name, vboxOS, cpus, ramMB); err != nil {
		return fmt.Errorf("creating VM: %s", err)
	}
	if err := vbox.AddSATAController(sourceVM.Name, "SATA"); err != nil {
		return fmt.Errorf("adding SATA controller: %s", err)
	}
	if err := vbox.AttachDisk(sourceVM.Name, vdiPath, "SATA"); err != nil {
		return fmt.Errorf("attaching disk: %s", err)
	}

	nics, _ := core.ParseVMXNetworking(sourceVM.Path, nil)
	for _, nic := range nics {
		vboxConn := nicConnVMwareToVBox(nic.Type)
		vboxDev := nicDevVMwareToVBox(nic.VirtualDev)
		idx := "1"
		if nic.Index != "" {
			n, _ := strconv.Atoi(nic.Index)
			idx = strconv.Itoa(n + 1)
		}
		exec.Command(vbox.VBoxManagePath(), "modifyvm", sourceVM.Name,
			"--nic"+idx, vboxConn,
			"--nictype"+idx, vboxDev).Run()
	}

	return nil
}

// migrateOneVBoxToVMware migrates a single VBox VM to VMware. Returns nil on success.
// The provided mpb bar is updated with disk conversion progress.
func migrateOneVBoxToVMware(vmName string, bar *mpb.Bar) error {
	vbox, err := newVBox()
	if err != nil {
		return fmt.Errorf("%s", err)
	}
	info, err := vbox.GetVMInfo(vmName)
	if err != nil {
		return fmt.Errorf("%s", err)
	}
	if info.State == "running" {
		return fmt.Errorf("VM must be powered off before migration")
	}

	if len(info.Disks) == 0 {
		return fmt.Errorf("no disks found")
	}

	qemuImg, err := findQemuImg()
	if err != nil {
		return fmt.Errorf("%s", err)
	}

	destDir := filepath.Join(settings.VmDirectory, vmName)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("creating directory: %s", err)
	}

	srcDisk := info.Disks[0]
	srcFmt := "vdi"
	if strings.HasSuffix(srcDisk, ".vmdk") {
		srcFmt = "vmdk"
	} else if strings.HasSuffix(srcDisk, ".vhd") {
		srcFmt = "vpc"
	}
	vmdkPath := filepath.Join(destDir, vmName+".vmdk")
	if err := convertDiskWithBar(qemuImg, srcDisk, srcFmt, vmdkPath, "vmdk", bar); err != nil {
		return fmt.Errorf("disk conversion: %s", err)
	}

	vmxPath := filepath.Join(destDir, vmName+".vmx")
	guestOS := vboxToVMwareOS(info.OSType)
	cpus := info.CPUs
	if cpus < 1 {
		cpus = 1
	}
	ramMB := info.MemoryMB
	if ramMB < 512 {
		ramMB = 512
	}

	vmxContent := generateVMX(vmName, guestOS, cpus, ramMB, vmName+".vmdk", info.NICs)
	if err := os.WriteFile(vmxPath, []byte(vmxContent), 0644); err != nil {
		return fmt.Errorf("writing VMX: %s", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// VMware → VirtualBox
// ---------------------------------------------------------------------------

func migrateVMwareToVBox(vmName string) {
	// 1. Find the VMware VM.
	vms, err := workstationBackend().GetPowerState()
	if err != nil {
		core.LogError(core.ErrSourceNotFound, vmName, "%s", err)
		exit(1)
	}
	var sourceVM core.VM
	found := false
	for _, vm := range vms {
		if strings.EqualFold(vm.Name, vmName) {
			sourceVM = vm
			found = true
			break
		}
	}
	if !found {
		core.LogError(core.ErrSourceNotFound, vmName, "VM not found in VMware inventory")
		exit(1)
	}
	if sourceVM.Running {
		fmt.Fprintf(os.Stderr, "error: VM must be powered off before migration\n")
		exit(1)
	}

	p := mpb.New()
	bar := p.New(100,
		mpb.BarStyle().Lbound("[").Filler("=").Tip("").Padding(" ").Rbound("]"),
		mpb.BarWidth(40),
		mpb.PrependDecorators(
			decor.Name(vmName),
		),
		mpb.AppendDecorators(
			decor.Percentage(decor.WCSyncSpaceR),
		),
	)

	if err := migrateOneVMwareToVBox(sourceVM, bar); err != nil {
		p.Wait()
		core.LogError(core.ErrMigration, vmName, "%s", err)
		exit(1)
	}
	p.Wait()
	fmt.Printf("%s → creating VM...\n", vmName)
	fmt.Printf("%s → migrated to VirtualBox\n", vmName)
}

// ---------------------------------------------------------------------------
// VirtualBox → VMware
// ---------------------------------------------------------------------------

func migrateVBoxToVMware(vmName string) {
	loadSettings()

	// 1. Get VBox VM info.
	vbox, err := newVBox()
	if err != nil {
		core.LogError(core.ErrSourceNotFound, vmName, "%s", err)
		exit(1)
	}
	info, err := vbox.GetVMInfo(vmName)
	if err != nil {
		core.LogError(core.ErrSourceNotFound, vmName, "%s", err)
		exit(1)
	}
	if info.State == "running" {
		fmt.Fprintf(os.Stderr, "error: VM must be powered off before migration\n")
		exit(1)
	}

	p := mpb.New()
	bar := p.New(100,
		mpb.BarStyle().Lbound("[").Filler("=").Tip("").Padding(" ").Rbound("]"),
		mpb.BarWidth(40),
		mpb.PrependDecorators(
			decor.Name(vmName),
		),
		mpb.AppendDecorators(
			decor.Percentage(decor.WCSyncSpaceR),
		),
	)

	if err := migrateOneVBoxToVMware(vmName, bar); err != nil {
		p.Wait()
		core.LogError(core.ErrMigration, vmName, "%s", err)
		exit(1)
	}
	p.Wait()
	fmt.Printf("%s → creating VM...\n", vmName)
	fmt.Printf("%s → migrated to VMware Workstation\n", vmName)
}

// generateVMX creates a minimal VMX file for a migrated VM.
func generateVMX(name, guestOS string, cpus, ramMB int, vmdkFile string, nics []vbox.VBoxNIC) string {
	var b strings.Builder
	b.WriteString(".encoding = \"UTF-8\"\n")
	b.WriteString("config.version = \"8\"\n")
	b.WriteString("virtualHW.version = \"21\"\n")
	b.WriteString(fmt.Sprintf("displayName = \"%s\"\n", name))
	b.WriteString(fmt.Sprintf("guestOS = \"%s\"\n", guestOS))
	b.WriteString(fmt.Sprintf("numvcpus = \"%d\"\n", cpus))
	b.WriteString(fmt.Sprintf("cpuid.coresPerSocket = \"%d\"\n", cpus))
	b.WriteString(fmt.Sprintf("memsize = \"%d\"\n", ramMB))
	b.WriteString("pciBridge0.present = \"TRUE\"\n")
	b.WriteString("pciBridge4.present = \"TRUE\"\n")
	b.WriteString("pciBridge4.virtualDev = \"pcieRootPort\"\n")
	b.WriteString("pciBridge4.functions = \"8\"\n")

	// SATA controller + disk.
	b.WriteString("sata0.present = \"TRUE\"\n")
	b.WriteString("sata0:0.present = \"TRUE\"\n")
	b.WriteString(fmt.Sprintf("sata0:0.fileName = \"%s\"\n", vmdkFile))

	// NICs.
	if len(nics) == 0 {
		b.WriteString("ethernet0.present = \"TRUE\"\n")
		b.WriteString("ethernet0.connectionType = \"nat\"\n")
		b.WriteString("ethernet0.virtualDev = \"e1000e\"\n")
		b.WriteString("ethernet0.startConnected = \"TRUE\"\n")
	} else {
		for _, nic := range nics {
			idx := nic.Index - 1 // VBox uses 1-based, VMware uses 0-based
			if idx < 0 {
				idx = 0
			}
			vmwareConn := nicConnVBoxToVMware(nic.Type)
			vmwareDev := nicDevVBoxToVMware(nic.NICType)
			b.WriteString(fmt.Sprintf("ethernet%d.present = \"TRUE\"\n", idx))
			b.WriteString(fmt.Sprintf("ethernet%d.connectionType = \"%s\"\n", idx, vmwareConn))
			b.WriteString(fmt.Sprintf("ethernet%d.virtualDev = \"%s\"\n", idx, vmwareDev))
			b.WriteString(fmt.Sprintf("ethernet%d.startConnected = \"TRUE\"\n", idx))
		}
	}

	// Tools and power.
	b.WriteString("tools.syncTime = \"TRUE\"\n")
	b.WriteString("powerType.powerOff = \"soft\"\n")
	b.WriteString("powerType.suspend = \"soft\"\n")
	b.WriteString("powerType.reset = \"soft\"\n")

	return b.String()
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func init() {
	rootCmd.AddCommand(migrateCmd)
	migrateCmd.Flags().StringVar(&migrateFromFlag, "from", "", "Source hypervisor (vmware, vbox)")
	migrateCmd.Flags().StringVar(&migrateToFlag, "to", "", "Target hypervisor (vmware, vbox)")
	migrateCmd.Flags().StringVar(&migrateFolderFlag, "folder", "", "Migrate all VMs in a VMware folder (or all VBox VMs)")
}
