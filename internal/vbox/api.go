package vbox

import "fmt"

// VBoxAPIBackend is a placeholder for a future VirtualBox API-based backend.
// All methods return an error indicating the API backend is not yet implemented.
type VBoxAPIBackend struct{}

var errVBoxAPI = fmt.Errorf("vbox API backend not implemented")

func (v *VBoxAPIBackend) ListVMs() ([]VBoxVM, error)                      { return nil, errVBoxAPI }
func (v *VBoxAPIBackend) DirectStartVM(nameOrUUID string) error           { return errVBoxAPI }
func (v *VBoxAPIBackend) DirectStopVM(nameOrUUID string, hard bool) error { return errVBoxAPI }
func (v *VBoxAPIBackend) GetVMInfo(nameOrUUID string) (VBoxVMInfo, error) {
	return VBoxVMInfo{}, errVBoxAPI
}
func (v *VBoxAPIBackend) ImportOVF(ovfPath string) error                      { return errVBoxAPI }
func (v *VBoxAPIBackend) ExportOVF(nameOrUUID, outputPath string) error       { return errVBoxAPI }
func (v *VBoxAPIBackend) CreateVM(name, osType string, cpus, ramMB int) error { return errVBoxAPI }
func (v *VBoxAPIBackend) AddSATAController(vmName, ctlName string) error      { return errVBoxAPI }
func (v *VBoxAPIBackend) AttachDisk(vmName, diskPath, controller string) error {
	return errVBoxAPI
}
func (v *VBoxAPIBackend) SnapshotCreate(vmName, snapName string) error { return errVBoxAPI }
func (v *VBoxAPIBackend) SnapshotList(vmName string) ([]string, error) { return nil, errVBoxAPI }
func (v *VBoxAPIBackend) SnapshotRevert(vmName, snapName string) error { return errVBoxAPI }
func (v *VBoxAPIBackend) SnapshotDelete(vmName, snapName string) error { return errVBoxAPI }
func (v *VBoxAPIBackend) DeleteVM(nameOrUUID string) error             { return errVBoxAPI }
