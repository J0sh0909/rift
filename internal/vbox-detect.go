package internal

import (
	"os/exec"
	"regexp"
	"strings"
)

// VBoxVM holds minimal info about a VirtualBox VM.
type VBoxVM struct {
	Name  string
	State string
}

// DetectVBoxVMs returns VirtualBox VMs if VBoxManage is available.
// Returns nil, nil if VBoxManage is not found (not an error).
func DetectVBoxVMs() ([]VBoxVM, error) {
	vboxPath, err := exec.LookPath("VBoxManage")
	if err != nil {
		return nil, nil // not installed — skip silently
	}

	out, err := exec.Command(vboxPath, "list", "vms").Output()
	if err != nil {
		return nil, nil
	}

	// Output format: "VM Name" {uuid}
	re := regexp.MustCompile(`"(.+?)"\s+\{(.+?)\}`)
	matches := re.FindAllStringSubmatch(string(out), -1)

	var vms []VBoxVM
	for _, m := range matches {
		name := m[1]
		uuid := m[2]
		state := vboxGetState(vboxPath, uuid)
		vms = append(vms, VBoxVM{Name: name, State: state})
	}
	return vms, nil
}

func vboxGetState(vboxPath, uuid string) string {
	out, err := exec.Command(vboxPath, "showvminfo", uuid, "--machinereadable").Output()
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "VMState=") {
			val := strings.TrimPrefix(line, "VMState=")
			val = strings.Trim(strings.TrimSpace(val), "\"")
			return val
		}
	}
	return "unknown"
}
