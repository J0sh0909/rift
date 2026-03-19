//go:build !windows

package internal

// wsStartDetached on non-Windows delegates to wsVmrun. Process detachment is
// only needed on Windows CI runners where the runner kills child processes.
func wsStartDetached(vmrunPath string, args ...string) (string, error) {
	return wsVmrun(vmrunPath, args...)
}

// EnsureVMwareRunning is a no-op on non-Windows platforms.
func (w *WorkstationBackend) EnsureVMwareRunning() error {
	return nil
}
