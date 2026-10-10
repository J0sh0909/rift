package core

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// WindowsShell is the interpreter used for Windows guests.
const WindowsShell = `C:\Windows\System32\cmd.exe`

// Guest OS families returned by GuestFamily.
const (
	FamilyWindows = "windows"
	FamilyUnix    = "unix"
)

// ExecRequest describes a shell command to run inside a guest.
type ExecRequest struct {
	User        string
	Pass        string
	Interpreter string // shell for non-Windows guests, e.g. /bin/bash
	Script      string
	Windows     bool          // run through cmd.exe instead of Interpreter
	Timeout     time.Duration // 0 = no limit
}

// ExecResult is the combined stdout/stderr of a guest command and its exit
// code. ExitCode is -1 when the backend could not determine it.
type ExecResult struct {
	Output   string
	ExitCode int
}

// GuestExecutor is implemented by backends that can run a shell command in a
// guest and return its output (vmrun, VBoxManage guestcontrol, PowerShell
// Direct / SSH). The returned error is reserved for transport failures such as
// bad credentials; a command that ran and exited non-zero is reported through
// ExecResult.ExitCode.
type GuestExecutor interface {
	ExecInGuest(vm VM, req ExecRequest) (ExecResult, error)
}

// GuestIPReporter is implemented by backends that can report a guest's IP
// addresses through the hypervisor (VMware Tools, VirtualBox Guest Additions,
// Hyper-V integration services), without guest credentials.
type GuestIPReporter interface {
	GuestIPs(vm VM) ([]string, error)
}

// GuestOSReporter is implemented by backends that can look up the guest OS
// identifier on demand when VM.OS was not filled in while listing.
type GuestOSReporter interface {
	GuestOS(vm VM) string
}

// GuestFamily classifies a guest OS identifier from any backend (VMware
// guestOS, VirtualBox ostype, Hyper-V KVP OSName) as FamilyWindows,
// FamilyUnix, or "" when unknown.
func GuestFamily(osID string) string {
	lower := strings.ToLower(strings.TrimSpace(osID))
	switch {
	case lower == "":
		return ""
	case strings.HasPrefix(lower, "win"):
		return FamilyWindows
	case strings.HasPrefix(lower, "darwin"), strings.HasPrefix(lower, "mac"):
		return FamilyUnix
	}
	if interp, ok := DefaultInterpreter(lower); ok {
		if strings.Contains(interp, `\`) {
			return FamilyWindows
		}
		return FamilyUnix
	}
	for _, hint := range []string{"linux", "bsd", "unix", "solaris"} {
		if strings.Contains(lower, hint) {
			return FamilyUnix
		}
	}
	return ""
}

// FamilyFromName infers the guest family from rift's VM naming convention
// ("-w-" for Windows, "-u-" for Unix/Linux), used when the hypervisor does not
// expose the guest OS.
func FamilyFromName(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "-w-"):
		return FamilyWindows
	case strings.Contains(lower, "-u-"):
		return FamilyUnix
	}
	return ""
}

// InterpreterFor returns the default interpreter for a guest OS identifier,
// falling back to cmd.exe or /bin/sh by family when the exact OS is unknown.
func InterpreterFor(osID, family string) (string, bool) {
	if interp, ok := DefaultInterpreter(osID); ok {
		return interp, true
	}
	switch family {
	case FamilyWindows:
		return WindowsShell, true
	case FamilyUnix:
		return "/bin/sh", true
	}
	return "", false
}

// ShellQuote quotes s for a POSIX shell.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// exitTrailer marks the exit code that WrapShellCapture appends to the output.
const exitTrailer = "\n__RIFT_EXIT__="

// WrapShellCapture wraps a POSIX shell script so that all of its output
// (stdout and stderr of every command, not just the last one) goes to outPath,
// followed by a trailer recording the exit status. Running the script in a
// subshell means an "exit" inside it still lets the trailer be written, and
// the wrapper itself always exits 0 so the transport doesn't report the
// command's own failure as a transport error.
func WrapShellCapture(script, outPath string) string {
	q := ShellQuote(outPath)
	return "(\n" + script + "\n) > " + q + " 2>&1\n" +
		"printf '" + strings.ReplaceAll(exitTrailer, "\n", `\n`) + "%d\\n' \"$?\" >> " + q + "\n"
}

// SplitExitTrailer removes the WrapShellCapture trailer from captured output
// and returns the exit code it recorded (ok=false when there is none).
func SplitExitTrailer(out string) (string, int, bool) {
	idx := strings.LastIndex(out, exitTrailer)
	if idx < 0 {
		return out, -1, false
	}
	rest := strings.TrimSpace(out[idx+len(exitTrailer):])
	code, err := strconv.Atoi(rest)
	if err != nil {
		return out, -1, false
	}
	return out[:idx], code, true
}

var exitCodeRe = regexp.MustCompile(`(?i)exit(?:ed)?(?: with)?(?: a)?(?: non-zero)? (?:exit )?code:?\s*(-?\d+)`)

// ExitCodeFromText extracts an exit code from tool output such as vmrun's
// "Guest program exited with non-zero exit code: 1". Returns -1 if absent.
func ExitCodeFromText(s string) int {
	if m := exitCodeRe.FindStringSubmatch(s); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	return -1
}

// RandomToken returns a short random hex string for unique temp file names.
func RandomToken() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

var ipv4Re = regexp.MustCompile(`\b(\d{1,3}(?:\.\d{1,3}){3})\b`)

// FirstIPv4 returns the first plausible IPv4 address in s, or "".
func FirstIPv4(s string) string {
	for _, m := range ipv4Re.FindAllString(s, -1) {
		if IsUsableIPv4(m) {
			return m
		}
	}
	return ""
}

// IsUsableIPv4 reports whether s is a dotted IPv4 address that is not
// loopback, unspecified or link-local.
func IsUsableIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	var oct [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return false
		}
		oct[i] = n
	}
	switch {
	case oct[0] == 0, oct[0] == 127:
		return false
	case oct[0] == 169 && oct[1] == 254:
		return false
	}
	return true
}
