package vmware

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/J0sh0909/rift/internal/core"
)

var (
	_ core.GuestExecutor   = (*WorkstationBackend)(nil)
	_ core.GuestIPReporter = (*WorkstationBackend)(nil)
	_ core.GuestOSReporter = (*WorkstationBackend)(nil)
)

// ExecInGuest runs a shell command in the guest. vmrun cannot return guest
// output directly, so the output is redirected to a uniquely named temp file
// in the guest, copied back to the host, and deleted. The output is collected
// even when the command fails, so errors are visible.
func (w *WorkstationBackend) ExecInGuest(vm core.VM, req core.ExecRequest) (core.ExecResult, error) {
	ctx := context.Background()
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	token := core.RandomToken()
	var (
		guestOut string
		runOut   []byte
		runErr   error
	)
	if req.Windows {
		guestOut = `C:\Windows\Temp\rift-exec-` + token + `.txt`
		runOut, runErr = w.guestOp(ctx, vm.Path, req.User, req.Pass, "runProgramInGuest",
			core.WindowsShell, "/c "+req.Script+" > "+guestOut+" 2>&1")
	} else {
		interp := req.Interpreter
		if interp == "" {
			interp = "/bin/sh"
		}
		guestOut = "/tmp/rift-exec-" + token + ".txt"
		runOut, runErr = w.guestOp(ctx, vm.Path, req.User, req.Pass, "runScriptInGuest",
			interp, core.WrapShellCapture(req.Script, guestOut))
	}
	if runErr != nil && (ctx.Err() != nil || isInvalidCredentials(runOut)) {
		return core.ExecResult{}, runErr // nothing was written to collect
	}

	output, fetchErr := w.fetchGuestFile(ctx, vm.Path, req.User, req.Pass, guestOut)
	if fetchErr != nil {
		if runErr != nil {
			return core.ExecResult{}, runErr
		}
		return core.ExecResult{}, fetchErr
	}

	res := core.ExecResult{Output: output}
	if out, code, ok := core.SplitExitTrailer(output); ok && !req.Windows {
		res.Output, res.ExitCode = out, code
	} else if runErr != nil {
		res.ExitCode = core.ExitCodeFromText(string(runOut))
	}
	return res, nil
}

// guestOp runs a vmrun guest operation with guest credentials.
func (w *WorkstationBackend) guestOp(ctx context.Context, vmxPath, user, pass, op string, args ...string) ([]byte, error) {
	full := append(w.vpArgsForVM(vmxPath), "-T", "ws", "-gu", user, "-gp", pass, op, vmxPath)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, w.s.VmrunPath, full...)
	cmd.WaitDelay = 2 * time.Second // don't hang on pipes inherited by children after a timeout
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("vmrun %s timed out", op)
	}
	if err != nil {
		return out, fmt.Errorf("vmrun %s failed: %w\nOutput: %s", op, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// fetchGuestFile copies guestPath to the host, deletes it from the guest and
// returns its contents.
func (w *WorkstationBackend) fetchGuestFile(ctx context.Context, vmxPath, user, pass, guestPath string) (string, error) {
	tmp, err := os.CreateTemp("", "rift-exec-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	hostPath := tmp.Name()
	tmp.Close()
	defer os.Remove(hostPath)

	_, copyErr := w.guestOp(ctx, vmxPath, user, pass, "copyFileFromGuestToHost", guestPath, hostPath)
	_, _ = w.guestOp(context.Background(), vmxPath, user, pass, "deleteFileInGuest", guestPath)
	if copyErr != nil {
		return "", fmt.Errorf("collecting output: %w", copyErr)
	}
	data, err := os.ReadFile(hostPath)
	if err != nil {
		return "", fmt.Errorf("reading output: %w", err)
	}
	return string(data), nil
}

// GuestIPs returns the guest's IP address as reported by VMware Tools.
func (w *WorkstationBackend) GuestIPs(vm core.VM) ([]string, error) {
	args := append(w.vpArgsForVM(vm.Path), "-T", "ws", "getGuestIPAddress", vm.Path)
	out, err := exec.Command(w.s.VmrunPath, args...).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimPrefix(firstLine(text), "Error: "))
	}
	if net.ParseIP(text) == nil {
		return nil, fmt.Errorf("no IP reported (%s)", firstLine(text))
	}
	return []string{text}, nil
}

// GuestOS returns the guestOS key from the VMX file.
func (w *WorkstationBackend) GuestOS(vm core.VM) string {
	return core.GetGuestOS(vm.Path)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
