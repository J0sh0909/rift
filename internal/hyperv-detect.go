package internal

import (
	"encoding/json"
	"os/exec"
	"runtime"
)

// HyperVVM holds minimal info about a Hyper-V VM.
type HyperVVM struct {
	Name  string `json:"Name"`
	State int    `json:"State"`
}

// HyperVStateName maps Hyper-V integer state to a human-readable string.
func HyperVStateName(state int) string {
	switch state {
	case 2:
		return "running"
	case 3:
		return "off"
	case 6:
		return "saved"
	case 9:
		return "paused"
	case 10:
		return "starting"
	case 11:
		return "resetting"
	case 4:
		return "shutting down"
	default:
		return "unknown"
	}
}

// DetectHyperVVMs returns Hyper-V VMs if available on Windows.
// Returns nil, nil if not Windows or Hyper-V is not enabled.
func DetectHyperVVMs() ([]HyperVVM, error) {
	if runtime.GOOS != "windows" {
		return nil, nil
	}

	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`Get-VM | Select-Object Name,State | ConvertTo-Json`).Output()
	if err != nil {
		return nil, nil // Hyper-V not enabled — skip silently
	}

	trimmed := string(out)
	if len(trimmed) == 0 {
		return nil, nil
	}

	// PowerShell returns a single object (not array) when there's one VM.
	var vms []HyperVVM
	if err := json.Unmarshal(out, &vms); err != nil {
		var single HyperVVM
		if err2 := json.Unmarshal(out, &single); err2 != nil {
			return nil, nil
		}
		vms = []HyperVVM{single}
	}
	return vms, nil
}
