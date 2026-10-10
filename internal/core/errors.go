package core

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Error code constants
// ---------------------------------------------------------------------------

const (
	// VM1xx - power operations
	ErrPower          = "VM100"
	ErrStartFailed    = "VM101"
	ErrStopFailed     = "VM102"
	ErrSuspendFailed  = "VM103"
	ErrResetFailed    = "VM104"
	ErrAlreadyRunning = "VM105"
	ErrAlreadyStopped = "VM106"

	// VM2xx - config operations
	ErrConfig        = "VM200"
	ErrCPUConfig     = "VM201"
	ErrRAMConfig     = "VM202"
	ErrDiskConfig    = "VM203"
	ErrNetConfig     = "VM204"
	ErrDisplayConfig = "VM205"
	ErrOSConfig      = "VM206"
	ErrCDVDConfig    = "VM207"
	ErrTPMConfig     = "VM208"

	// VM3xx - exec/guest operations
	ErrExec             = "VM300"
	ErrGuestOSNotDet    = "VM301"
	ErrGuestCmd         = "VM302"
	ErrOutputCapture    = "VM303"
	ErrInterpreter      = "VM304"
	ErrNotRunning       = "VM305"
	ErrBootstrapLinux   = "VM306"
	ErrBootstrapWindows = "VM307"
	ErrGuestNoIP        = "VM308"

	// VM4xx - snapshot operations
	ErrSnapshot     = "VM400"
	ErrSnapCreate   = "VM401"
	ErrSnapRevert   = "VM402"
	ErrSnapDelete   = "VM403"
	ErrSnapNotFound = "VM404"
	ErrSnapExists   = "VM405"

	// VM5xx - archive operations
	ErrArchive         = "VM500"
	ErrExportFailed    = "VM501"
	ErrImportFailed    = "VM502"
	ErrOvftoolNotFound = "VM503"
	ErrDiskConvert     = "VM504"

	// VM6xx - environment/settings
	ErrEnvNotFound    = "VM600"
	ErrMissingSetting = "VM601"
	ErrInvalidPath    = "VM602"
	ErrVmrunNotFound  = "VM603"
	ErrUnsupported    = "VM604"
	ErrNotFound       = "VM605"
	ErrAmbiguous      = "VM606"

	// VM7xx - file/VMX operations
	ErrVMXRead        = "VM700"
	ErrVMXWrite       = "VM701"
	ErrVMXKeyNotFound = "VM702"
	ErrFileNotFound   = "VM703"
	ErrPermDenied     = "VM704"

	// VM8xx - AWS operations
	ErrAWS             = "VM800"
	ErrAWSNotFound     = "VM801"
	ErrAWSCreateFailed = "VM802"
	ErrAWSStartFailed  = "VM803"
	ErrAWSStopFailed   = "VM804"
	ErrAWSTermFailed   = "VM805"
	ErrHypervisorInit  = "VM899"

	// VM9xx - migration operations
	ErrMigration        = "VM900"
	ErrDiskConvertMig   = "VM901"
	ErrSourceNotFound   = "VM902"
	ErrQemuImgNotFound  = "VM903"
	ErrTargetHypervisor = "VM904"
)

// ---------------------------------------------------------------------------
// Error code reference table
// ---------------------------------------------------------------------------

// ErrorRef holds a code and its human-readable description.
type ErrorRef struct {
	Code string
	Desc string
}

// ErrorCodes is the ordered list of all error codes and descriptions.
var ErrorCodes = []ErrorRef{
	{ErrPower, "generic power operation error"},
	{ErrStartFailed, "VM start failed"},
	{ErrStopFailed, "VM stop failed"},
	{ErrSuspendFailed, "VM suspend failed"},
	{ErrResetFailed, "VM reset failed"},
	{ErrAlreadyRunning, "VM is already running"},
	{ErrAlreadyStopped, "VM is already stopped"},

	{ErrConfig, "generic config operation error"},
	{ErrCPUConfig, "CPU config failed"},
	{ErrRAMConfig, "RAM config failed"},
	{ErrDiskConfig, "disk config failed"},
	{ErrNetConfig, "network config failed"},
	{ErrDisplayConfig, "display config failed"},
	{ErrOSConfig, "OS config failed"},
	{ErrCDVDConfig, "CD/DVD config failed"},
	{ErrTPMConfig, "TPM config failed"},

	{ErrExec, "generic exec/guest operation error"},
	{ErrGuestOSNotDet, "guest OS not detected"},
	{ErrGuestCmd, "guest command failed"},
	{ErrOutputCapture, "output capture failed"},
	{ErrInterpreter, "interpreter not found"},
	{ErrNotRunning, "VM is not running (exec requires running VM)"},
	{ErrBootstrapLinux, "bootstrap Linux operation failed"},
	{ErrBootstrapWindows, "bootstrap Windows operation failed"},
	{ErrGuestNoIP, "guest IP not reported (tools / guest additions / integration services not running)"},

	{ErrSnapshot, "generic snapshot operation error"},
	{ErrSnapCreate, "snapshot create failed"},
	{ErrSnapRevert, "snapshot revert failed"},
	{ErrSnapDelete, "snapshot delete failed"},
	{ErrSnapNotFound, "snapshot not found"},
	{ErrSnapExists, "snapshot already exists"},

	{ErrArchive, "generic archive operation error"},
	{ErrExportFailed, "export failed"},
	{ErrImportFailed, "import failed"},
	{ErrOvftoolNotFound, "ovftool not found"},
	{ErrDiskConvert, "disk conversion failed"},

	{ErrEnvNotFound, ".env file not found"},
	{ErrMissingSetting, "missing required setting"},
	{ErrInvalidPath, "invalid path"},
	{ErrVmrunNotFound, "vmrun not found"},
	{ErrUnsupported, "operation not supported by this hypervisor backend"},
	{ErrNotFound, "VM or folder not found on this node"},
	{ErrAmbiguous, "VM name exists on several backends - use --hv"},

	{ErrVMXRead, "VMX read failed"},
	{ErrVMXWrite, "VMX write failed"},
	{ErrVMXKeyNotFound, "VMX key not found"},
	{ErrFileNotFound, "file not found"},
	{ErrPermDenied, "permission denied"},

	{ErrAWS, "generic AWS error"},
	{ErrAWSNotFound, "EC2 instance not found"},
	{ErrAWSCreateFailed, "EC2 create failed"},
	{ErrAWSStartFailed, "EC2 start failed"},
	{ErrAWSStopFailed, "EC2 stop failed"},
	{ErrAWSTermFailed, "EC2 terminate failed"},
	{ErrHypervisorInit, "hypervisor backend failed to initialize or list VMs"},

	{ErrMigration, "generic migration error"},
	{ErrDiskConvertMig, "disk conversion failed"},
	{ErrSourceNotFound, "source VM not found"},
	{ErrQemuImgNotFound, "qemu-img not found"},
	{ErrTargetHypervisor, "target hypervisor error"},
}

// ---------------------------------------------------------------------------
// Logging
// ---------------------------------------------------------------------------

var (
	logMu   sync.Mutex
	logFile *os.File
	logOnce sync.Once
)

// InitLogging opens the log file at path for appending.
// No-op if path is empty. Safe to call multiple times; opens the file only once.
func InitLogging(path string) error {
	if path == "" {
		return nil
	}
	var initErr error
	logOnce.Do(func() {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			initErr = fmt.Errorf("opening log file: %w", err)
			return
		}
		logFile = f
	})
	return initErr
}

// LogError prints a formatted error to stderr and optionally to the log file.
// Format: [VM301] H74-RT: message
func LogError(code string, vm string, msg string, args ...any) {
	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	var line string
	if vm != "" {
		line = fmt.Sprintf("[%s] %s: %s", code, vm, msg)
	} else {
		line = fmt.Sprintf("[%s] %s", code, msg)
	}
	fmt.Fprintln(os.Stderr, line)
	writeLog("ERROR", line)
}

// LogInfo prints a success/info message to stdout and optionally to the log file.
// Format: H74-RT → message
func LogInfo(vm string, msg string, args ...any) {
	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	var line string
	if vm != "" {
		line = fmt.Sprintf("%s → %s", vm, msg)
	} else {
		line = msg
	}
	fmt.Println(line)
	writeLog("INFO", line)
}

func writeLog(level, line string) {
	logMu.Lock()
	defer logMu.Unlock()
	if logFile == nil {
		return
	}
	ts := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(logFile, "%s [%s] %s\n", ts, level, line)
}
