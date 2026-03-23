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

	"github.com/J0sh0909/rift/internal"
	"github.com/spf13/cobra"
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
	s, _ := internal.LoadSettings()
	if s.QemuImgPath != "" {
		if _, err := os.Stat(s.QemuImgPath); err == nil {
			return s.QemuImgPath, nil
		}
	}
	p, err := exec.LookPath("qemu-img")
	if err != nil {
		return "", fmt.Errorf("qemu-img not found — set QEMU_IMG_PATH in .env or add to PATH")
	}
	return p, nil
}

// convertDisk runs qemu-img convert with a progress bar based on output file size growth.
func convertDisk(qemuImg, srcPath, srcFmt, dstPath, dstFmt string) error {
	args := []string{"convert", "-f", srcFmt, "-O", dstFmt, "-p", srcPath, dstPath}
	cmd := exec.Command(qemuImg, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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
			os.Exit(1)
		}
		if from == to {
			fmt.Fprintln(os.Stderr, "error: --from and --to must be different")
			os.Exit(1)
		}

		if migrateFolderFlag == "" && len(args) == 0 {
			fmt.Fprintln(os.Stderr, "error: provide a VM name or --folder")
			os.Exit(1)
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
			os.Exit(1)
		}
	},
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
		os.Exit(1)
	}
}

func migrateFolderVMwareToVBox() {
	requireSettings()
	vms, err := hv.GetPowerState()
	if err != nil {
		internal.LogError(internal.ErrSourceNotFound, "", "listing VMware VMs: %s", err)
		os.Exit(1)
	}
	var targets []internal.VM
	for _, vm := range vms {
		if strings.EqualFold(vm.Folder, migrateFolderFlag) {
			targets = append(targets, vm)
		}
	}
	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "no VMs found in folder '%s'\n", migrateFolderFlag)
		os.Exit(1)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })

	var (
		mu      sync.Mutex
		results []migrateResult
		wg      sync.WaitGroup
	)
	for _, vm := range targets {
		wg.Add(1)
		go func(vm internal.VM) {
			defer wg.Done()
			err := migrateOneVMwareToVBox(vm)
			mu.Lock()
			results = append(results, migrateResult{name: vm.Name, err: err})
			mu.Unlock()
		}(vm)
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].name < results[j].name })
	for _, r := range results {
		if r.err != nil {
			internal.LogError(internal.ErrMigration, r.name, "%s", r.err)
		}
	}
}

func migrateFolderVBoxToVMware() {
	requireSettings()
	vbox, err := internal.NewVBoxBackend()
	if err != nil {
		internal.LogError(internal.ErrSourceNotFound, "", "%s", err)
		os.Exit(1)
	}
	vbVMs, err := vbox.ListVMs()
	if err != nil {
		internal.LogError(internal.ErrSourceNotFound, "", "listing VBox VMs: %s", err)
		os.Exit(1)
	}
	if len(vbVMs) == 0 {
		fmt.Fprintln(os.Stderr, "no VirtualBox VMs found")
		os.Exit(1)
	}
	sort.Slice(vbVMs, func(i, j int) bool { return vbVMs[i].Name < vbVMs[j].Name })

	var (
		mu      sync.Mutex
		results []migrateResult
		wg      sync.WaitGroup
	)
	for _, vm := range vbVMs {
		wg.Add(1)
		go func(vm internal.VBoxVM) {
			defer wg.Done()
			err := migrateOneVBoxToVMware(vm.Name)
			mu.Lock()
			results = append(results, migrateResult{name: vm.Name, err: err})
			mu.Unlock()
		}(vm)
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].name < results[j].name })
	for _, r := range results {
		if r.err != nil {
			internal.LogError(internal.ErrMigration, r.name, "%s", r.err)
		}
	}
}

// migrateOneVMwareToVBox migrates a single VMware VM to VBox. Returns nil on success.
func migrateOneVMwareToVBox(sourceVM internal.VM) error {
	if sourceVM.Running {
		return fmt.Errorf("VM must be powered off before migration")
	}

	fmt.Printf("%s → converting disk...\n", sourceVM.Name)

	specs, err := internal.ParseVMXSpecs(sourceVM.Path)
	if err != nil {
		return fmt.Errorf("reading specs: %s", err)
	}
	vmxData, err := internal.ParseVMXKeys(sourceVM.Path)
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

	disks, err := internal.ParseVMXDisks(sourceVM.Path)
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
	if err := convertDisk(qemuImg, vmdkPath, "vmdk", vdiPath, "vdi"); err != nil {
		return fmt.Errorf("disk conversion: %s", err)
	}

	fmt.Printf("%s → creating VM...\n", sourceVM.Name)
	vbox, err := internal.NewVBoxBackend()
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

	nics, _ := internal.ParseVMXNetworking(sourceVM.Path, nil)
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

	fmt.Printf("%s → migrated to VirtualBox\n", sourceVM.Name)
	return nil
}

// migrateOneVBoxToVMware migrates a single VBox VM to VMware. Returns nil on success.
func migrateOneVBoxToVMware(vmName string) error {
	vbox, err := internal.NewVBoxBackend()
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

	fmt.Printf("%s → converting disk...\n", vmName)

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
	if err := convertDisk(qemuImg, srcDisk, srcFmt, vmdkPath, "vmdk"); err != nil {
		return fmt.Errorf("disk conversion: %s", err)
	}

	fmt.Printf("%s → creating VM...\n", vmName)
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

	fmt.Printf("%s → migrated to VMware Workstation\n", vmName)
	return nil
}

// ---------------------------------------------------------------------------
// VMware → VirtualBox
// ---------------------------------------------------------------------------

func migrateVMwareToVBox(vmName string) {
	requireSettings()

	// 1. Find the VMware VM.
	vms, err := hv.GetPowerState()
	if err != nil {
		internal.LogError(internal.ErrSourceNotFound, vmName, "%s", err)
		os.Exit(1)
	}
	var sourceVM internal.VM
	found := false
	for _, vm := range vms {
		if strings.EqualFold(vm.Name, vmName) {
			sourceVM = vm
			found = true
			break
		}
	}
	if !found {
		internal.LogError(internal.ErrSourceNotFound, vmName, "VM not found in VMware inventory")
		os.Exit(1)
	}
	if sourceVM.Running {
		fmt.Fprintf(os.Stderr, "error: VM must be powered off before migration\n")
		os.Exit(1)
	}

	fmt.Printf("%s → reading VMware config...\n", vmName)

	// 2. Read VM specs.
	specs, err := internal.ParseVMXSpecs(sourceVM.Path)
	if err != nil {
		internal.LogError(internal.ErrSourceNotFound, vmName, "reading specs: %s", err)
		os.Exit(1)
	}
	vmxData, err := internal.ParseVMXKeys(sourceVM.Path)
	if err != nil {
		internal.LogError(internal.ErrSourceNotFound, vmName, "reading VMX: %s", err)
		os.Exit(1)
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

	// 3. Find disk.
	disks, err := internal.ParseVMXDisks(sourceVM.Path)
	if err != nil || len(disks) == 0 {
		internal.LogError(internal.ErrSourceNotFound, vmName, "no disks found")
		os.Exit(1)
	}
	vmdkPath := disks[0].FileName
	if !filepath.IsAbs(vmdkPath) {
		vmdkPath = filepath.Join(filepath.Dir(sourceVM.Path), vmdkPath)
	}

	// 4. Find qemu-img.
	qemuImg, err := findQemuImg()
	if err != nil {
		internal.LogError(internal.ErrQemuImgNotFound, vmName, "%s", err)
		os.Exit(1)
	}

	// 5. Convert disk.
	vdiPath := filepath.Join(filepath.Dir(vmdkPath), vmName+".vdi")
	fmt.Printf("%s → converting disk (vmdk → vdi)...\n", vmName)
	if err := convertDisk(qemuImg, vmdkPath, "vmdk", vdiPath, "vdi"); err != nil {
		internal.LogError(internal.ErrDiskConvertMig, vmName, "%s", err)
		os.Exit(1)
	}

	// 6. Create VBox VM.
	fmt.Printf("%s → creating VirtualBox VM...\n", vmName)
	vbox, err := internal.NewVBoxBackend()
	if err != nil {
		internal.LogError(internal.ErrTargetHypervisor, vmName, "%s", err)
		os.Exit(1)
	}
	vboxOS := vmwareToVBoxOS(guestOS)
	if err := vbox.CreateVM(vmName, vboxOS, cpus, ramMB); err != nil {
		internal.LogError(internal.ErrTargetHypervisor, vmName, "creating VM: %s", err)
		os.Exit(1)
	}

	// 7. Add SATA controller and attach disk.
	if err := vbox.AddSATAController(vmName, "SATA"); err != nil {
		internal.LogError(internal.ErrTargetHypervisor, vmName, "adding SATA controller: %s", err)
		os.Exit(1)
	}
	if err := vbox.AttachDisk(vmName, vdiPath, "SATA"); err != nil {
		internal.LogError(internal.ErrTargetHypervisor, vmName, "attaching disk: %s", err)
		os.Exit(1)
	}

	// 8. Configure NICs (connection type + virtual device).
	nics, _ := internal.ParseVMXNetworking(sourceVM.Path, nil)
	for _, nic := range nics {
		vboxConn := nicConnVMwareToVBox(nic.Type)
		vboxDev := nicDevVMwareToVBox(nic.VirtualDev)
		idx := "1"
		if nic.Index != "" {
			n, _ := strconv.Atoi(nic.Index)
			idx = strconv.Itoa(n + 1)
		}
		exec.Command(vbox.VBoxManagePath(), "modifyvm", vmName,
			"--nic"+idx, vboxConn,
			"--nictype"+idx, vboxDev).Run()
	}

	fmt.Printf("%s → migrated to VirtualBox\n", vmName)
}

// ---------------------------------------------------------------------------
// VirtualBox → VMware
// ---------------------------------------------------------------------------

func migrateVBoxToVMware(vmName string) {
	requireSettings()

	// 1. Get VBox VM info.
	vbox, err := internal.NewVBoxBackend()
	if err != nil {
		internal.LogError(internal.ErrSourceNotFound, vmName, "%s", err)
		os.Exit(1)
	}
	info, err := vbox.GetVMInfo(vmName)
	if err != nil {
		internal.LogError(internal.ErrSourceNotFound, vmName, "%s", err)
		os.Exit(1)
	}
	if info.State == "running" {
		fmt.Fprintf(os.Stderr, "error: VM must be powered off before migration\n")
		os.Exit(1)
	}

	fmt.Printf("%s → reading VirtualBox config...\n", vmName)

	if len(info.Disks) == 0 {
		internal.LogError(internal.ErrSourceNotFound, vmName, "no disks found")
		os.Exit(1)
	}

	// 2. Find qemu-img.
	qemuImg, err := findQemuImg()
	if err != nil {
		internal.LogError(internal.ErrQemuImgNotFound, vmName, "%s", err)
		os.Exit(1)
	}

	// 3. Create destination directory under VM_DIRECTORY.
	destDir := filepath.Join(settings.VmDirectory, vmName)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		internal.LogError(internal.ErrTargetHypervisor, vmName, "creating directory: %s", err)
		os.Exit(1)
	}

	// 4. Convert disk.
	srcDisk := info.Disks[0]
	srcFmt := "vdi"
	if strings.HasSuffix(srcDisk, ".vmdk") {
		srcFmt = "vmdk"
	} else if strings.HasSuffix(srcDisk, ".vhd") {
		srcFmt = "vpc"
	}
	vmdkPath := filepath.Join(destDir, vmName+".vmdk")
	fmt.Printf("%s → converting disk (%s → vmdk)...\n", vmName, srcFmt)
	if err := convertDisk(qemuImg, srcDisk, srcFmt, vmdkPath, "vmdk"); err != nil {
		internal.LogError(internal.ErrDiskConvertMig, vmName, "%s", err)
		os.Exit(1)
	}

	// 5. Generate VMX file.
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

	fmt.Printf("%s → generating VMX file...\n", vmName)
	vmxContent := generateVMX(vmName, guestOS, cpus, ramMB, vmName+".vmdk", info.NICs)
	if err := os.WriteFile(vmxPath, []byte(vmxContent), 0644); err != nil {
		internal.LogError(internal.ErrTargetHypervisor, vmName, "writing VMX: %s", err)
		os.Exit(1)
	}

	fmt.Printf("%s → migrated to VMware Workstation\n", vmName)
	fmt.Printf("VMX file: %s\n", vmxPath)
}

// generateVMX creates a minimal VMX file for a migrated VM.
func generateVMX(name, guestOS string, cpus, ramMB int, vmdkFile string, nics []internal.VBoxNIC) string {
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
