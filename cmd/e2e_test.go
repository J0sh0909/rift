package cmd

// End-to-end tests: the test binary re-executes itself as the rift CLI
// (RIFT_TEST_CLI=1) against fake vmrun and VBoxManage scripts. The fakes keep
// power state in files and run "guest" commands on the host, so the whole
// stack is exercised: .env loading, hypervisor detection, the node inventory,
// the engine, both backends, output, exit codes and dispatch mode.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("RIFT_TEST_CLI") == "1" {
		retryBackoff = 0
		Execute()
		finish()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const fakeVmrun = `#!/bin/sh
STATE="$RIFT_FAKE_STATE"
while [ $# -gt 0 ]; do
  case "$1" in
    -vp|-T|-gu|-gp) shift 2 ;;
    *) break ;;
  esac
done
op="$1"; shift
case "$op" in
list)
  echo "Total running VMs: $(grep -c . "$STATE/running")"
  cat "$STATE/running"
  ;;
start)
  if [ -f "$1.flaky" ]; then rm -f "$1.flaky"; echo "Error: Unknown error"; exit 255; fi
  echo "$1" >> "$STATE/running"
  ;;
stop|suspend)
  grep -vxF "$1" "$STATE/running" > "$STATE/running.tmp"; mv "$STATE/running.tmp" "$STATE/running"
  ;;
reset) ;;
runScriptInGuest)
  VM_NAME=$(basename "$1" .vmx) "$2" -c "$3"
  ;;
copyFileFromGuestToHost) cp "$2" "$3" ;;
deleteFileInGuest) rm -f "$2" ;;
getGuestIPAddress)
  if [ -f "$1.ip" ]; then cat "$1.ip"; else echo "Error: Unable to get the IP address"; exit 255; fi
  ;;
listSnapshots) echo "Total snapshots: 0" ;;
*) echo "Error: unsupported $op"; exit 1 ;;
esac
`

const fakeVBoxManage = `#!/bin/sh
STATE="$RIFT_FAKE_STATE"
meta() { grep "^$1|" "$STATE/vbox-meta"; }
case "$1" in
list)
  if [ "$2" = vms ]; then cat "$STATE/vbox-vms"; fi
  ;;
showvminfo)
  IFS='|' read -r uuid name group ostype <<EOF
$(meta "$2")
EOF
  state=poweroff
  grep -qx "$2" "$STATE/vbox-running" && state=running
  printf 'name="%s"\ngroups="%s"\nostype="%s"\nVMState="%s"\n' "$name" "$group" "$ostype" "$state"
  ;;
startvm) echo "$2" >> "$STATE/vbox-running" ;;
controlvm)
  grep -vxF "$2" "$STATE/vbox-running" > "$STATE/vbox-running.tmp"; mv "$STATE/vbox-running.tmp" "$STATE/vbox-running"
  ;;
guestcontrol)
  uuid="$2"
  name=$(meta "$uuid" | cut -d'|' -f2)
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do shift; done
  shift
  VM_NAME="$name" "$@"
  ;;
guestproperty)
  echo "/VirtualBox/GuestInfo/Net/0/V4/IP = '10.0.2.$(meta "$3" | cut -d'|' -f2 | tr -cd 0-9)' @ 2024-01-01T00:00:00Z"
  ;;
*) echo "VBoxManage: error: unsupported $1" >&2; exit 1 ;;
esac
`

// lab is a fake node: VMware VMs web-1 (DEV, running), web-2 (DEV, off),
// db-1 (PROD, running), shared (TEST) and VirtualBox VMs vb-dev-7 (group
// /LAB/DEV, running), vb-off-8 (/DEV, off), shared (/TEST).
type lab struct {
	dir, env, state, vms string
}

func newLab(t *testing.T, extraEnv ...string) *lab {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake hypervisor CLIs are shell scripts")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	l := &lab{dir: t.TempDir()}
	l.state = filepath.Join(l.dir, "state")
	l.vms = filepath.Join(l.dir, "vms")
	must(t, os.MkdirAll(l.state, 0o755))
	must(t, os.MkdirAll(l.vms, 0o755))
	write := func(path, content string, mode os.FileMode) {
		must(t, os.WriteFile(path, []byte(content), mode))
	}
	write(filepath.Join(l.dir, "vmrun"), fakeVmrun, 0o755)
	write(filepath.Join(l.dir, "VBoxManage"), fakeVBoxManage, 0o755)

	vmx := func(name string) string { return filepath.Join(l.vms, name+".vmx") }
	for _, name := range []string{"web-1", "web-2", "db-1", "shared"} {
		write(vmx(name), fmt.Sprintf("displayName = %q\nguestOS = \"ubuntu-64\"\nnumvcpus = \"1\"\nmemsize = \"1024\"\n", name), 0o644)
	}
	write(vmx("web-1")+".ip", "192.168.50.11\n", 0o644)
	inv := fmt.Sprintf(`vmlist1.config = "f-dev"
vmlist1.Type = "2"
vmlist1.DisplayName = "DEV"
vmlist1.ItemID = "f-dev"
vmlist2.config = "f-prod"
vmlist2.Type = "2"
vmlist2.DisplayName = "PROD"
vmlist2.ItemID = "f-prod"
vmlist3.config = "f-test"
vmlist3.Type = "2"
vmlist3.DisplayName = "TEST"
vmlist3.ItemID = "f-test"
vmlist4.config = %q
vmlist4.DisplayName = "web-1"
vmlist4.ParentID = "f-dev"
vmlist5.config = %q
vmlist5.DisplayName = "web-2"
vmlist5.ParentID = "f-dev"
vmlist6.config = %q
vmlist6.DisplayName = "db-1"
vmlist6.ParentID = "f-prod"
vmlist7.config = %q
vmlist7.DisplayName = "shared"
vmlist7.ParentID = "f-test"
`, vmx("web-1"), vmx("web-2"), vmx("db-1"), vmx("shared"))
	write(filepath.Join(l.dir, "inventory.vmls"), inv, 0o644)
	write(filepath.Join(l.state, "running"), vmx("web-1")+"\n"+vmx("db-1")+"\n", 0o644)

	write(filepath.Join(l.state, "vbox-vms"), `"vb-dev-7" {77777777-7777-7777-7777-777777777777}
"vb-off-8" {88888888-8888-8888-8888-888888888888}
"shared" {99999999-9999-9999-9999-999999999999}
`, 0o644)
	write(filepath.Join(l.state, "vbox-meta"), `77777777-7777-7777-7777-777777777777|vb-dev-7|/LAB/DEV|Ubuntu_64
88888888-8888-8888-8888-888888888888|vb-off-8|/DEV|Ubuntu_64
99999999-9999-9999-9999-999999999999|shared|/TEST|Ubuntu_64
`, 0o644)
	write(filepath.Join(l.state, "vbox-running"), "77777777-7777-7777-7777-777777777777\n", 0o644)

	l.env = filepath.Join(l.dir, ".env")
	env := fmt.Sprintf("VMRUN_PATH=%s\nINVENTORY_PATH=%s\nVBOX_MANAGE_PATH=%s\nVM_DEFAULT_USER=runner\nVM_DEFAULT_PASS=pw\nRIFT_NODE=testnode\n",
		filepath.Join(l.dir, "vmrun"), filepath.Join(l.dir, "inventory.vmls"), filepath.Join(l.dir, "VBoxManage"))
	for _, e := range extraEnv {
		env += e + "\n"
	}
	write(l.env, env, 0o644)
	return l
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// rift runs the CLI and returns stdout, stderr and the exit code.
func (l *lab) rift(t *testing.T, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = l.dir
	cmd.Env = append(os.Environ(), "RIFT_TEST_CLI=1", "ENV_PATH="+l.env, "RIFT_FAKE_STATE="+l.state,
		"GITHUB_STEP_SUMMARY=", "HOME="+l.dir)
	cmd.Env = append(cmd.Env, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

func expect(t *testing.T, label, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			t.Errorf("%s: missing %q in:\n%s", label, n, haystack)
		}
	}
}

func reject(t *testing.T, label, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			t.Errorf("%s: unexpected %q in:\n%s", label, n, haystack)
		}
	}
}

func TestE2EList(t *testing.T) {
	l := newLab(t)
	out, errOut, code := l.rift(t, nil, "list")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	expect(t, "list", out, "VMware Workstation", "VirtualBox", "web-1 [ON]", "web-2 [OFF]", "vb-dev-7 [ON]", "── DEV")
	// Folder filter.
	out, _, _ = l.rift(t, nil, "list", "-f", "prod")
	expect(t, "list -f prod", out, "db-1")
	reject(t, "list -f prod", out, "web-1", "vb-dev-7")
}

func TestE2EStartFolderAcrossHypervisors(t *testing.T) {
	l := newLab(t)
	out, errOut, code := l.rift(t, nil, "start", "-f", "DEV")
	if code != 0 {
		t.Fatalf("exit %d\nstdout:%s\nstderr:%s", code, out, errOut)
	}
	expect(t, "stdout", out, "web-2 [workstation] → started", "vb-off-8 [vbox] → started", "start: 2 ok, 2 skipped, 0 failed")
	expect(t, "stderr", errOut, "web-1 [workstation]: already running", "vb-dev-7 [vbox]: already running")
	reject(t, "stdout", out, "db-1", "shared")

	running, _ := os.ReadFile(filepath.Join(l.state, "running"))
	expect(t, "vmware state", string(running), "web-2.vmx")
	vbRunning, _ := os.ReadFile(filepath.Join(l.state, "vbox-running"))
	expect(t, "vbox state", string(vbRunning), "88888888-8888-8888-8888-888888888888")

	// Second run: everything already running -> skipped, still exit 0.
	_, _, code = l.rift(t, nil, "start", "-f", "dev")
	if code != 0 {
		t.Errorf("idempotent start should exit 0, got %d", code)
	}
}

func TestE2ETransientFailureIsRetried(t *testing.T) {
	l := newLab(t)
	must(t, os.WriteFile(filepath.Join(l.vms, "web-2.vmx.flaky"), nil, 0o644))
	out, errOut, code := l.rift(t, nil, "start", "web-2")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	expect(t, "stdout", out, "web-2 → started")
}

func TestE2EMissingTargets(t *testing.T) {
	l := newLab(t)
	_, errOut, code := l.rift(t, nil, "start", "-f", "NOPE")
	if code != 1 {
		t.Errorf("missing folder should exit 1 when run by hand, got %d", code)
	}
	expect(t, "stderr", errOut, "[VM605]", "no VMs in folder 'NOPE' on node testnode")

	_, errOut, code = l.rift(t, nil, "stop", "ghost")
	if code != 1 {
		t.Errorf("missing VM should exit 1, got %d", code)
	}
	expect(t, "stderr", errOut, "VM 'ghost' not found on node testnode")

	_, errOut, code = l.rift(t, nil, "start")
	if code != 1 {
		t.Errorf("no targets should exit 1, got %d", code)
	}
	expect(t, "stderr", errOut, "specify one or more VM names or --folder")
}

func TestE2EAmbiguousName(t *testing.T) {
	l := newLab(t)
	_, errOut, code := l.rift(t, nil, "stop", "shared")
	if code != 1 {
		t.Errorf("ambiguous name should exit 1, got %d", code)
	}
	expect(t, "stderr", errOut, "[VM606]", "workstation, vbox", "--hv")

	// --hv narrows to one backend; so does HYPERVISOR in .env.
	out, _, code := l.rift(t, nil, "--hv", "vbox", "start", "shared")
	if code != 0 {
		t.Errorf("--hv vbox start shared: exit %d", code)
	}
	expect(t, "--hv", out, "shared → started")

	l2 := newLab(t, "HYPERVISOR=workstation")
	out, _, code = l2.rift(t, nil, "start", "shared")
	if code != 0 {
		t.Errorf("HYPERVISOR=workstation start shared: exit %d", code)
	}
	expect(t, "HYPERVISOR", out, "shared → started")
	running, _ := os.ReadFile(filepath.Join(l2.state, "running"))
	expect(t, "vmware state", string(running), "shared.vmx")
}

func TestE2EExecFolder(t *testing.T) {
	l := newLab(t)
	out, errOut, code := l.rift(t, nil, "exec", "-f", "DEV", `echo "hello from $VM_NAME"; echo to-stderr >&2`)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:%s\nstderr:%s", code, out, errOut)
	}
	// VirtualBox delivers stdout and stderr on separate pipes, so only the
	// VMware capture (one redirected file) has a guaranteed order.
	expect(t, "stdout", out,
		"── vb-dev-7 [vbox] ──\n", "hello from vb-dev-7\n",
		"── web-1 [workstation] ──\nhello from web-1\nto-stderr",
		"exec: 2 ok, 2 skipped, 0 failed")
	if strings.Count(out, "to-stderr") != 2 {
		t.Errorf("stderr of both guests should be captured:\n%s", out)
	}
	expect(t, "stderr", errOut, "web-2 [workstation]: not running", "vb-off-8 [vbox]: not running")

	// Guest flags after the VM name belong to the guest command.
	out, errOut, code = l.rift(t, nil, "exec", "web-1", "ls", "-a", l.vms)
	if code != 0 {
		t.Fatalf("exec with guest flags: exit %d %s", code, errOut)
	}
	expect(t, "single exec prints raw output", out, "web-1.vmx")
	reject(t, "single exec has no header", out, "──")
}

func TestE2EExecFailure(t *testing.T) {
	l := newLab(t)
	out, errOut, code := l.rift(t, nil, "exec", "-f", "DEV", "echo partial; exit 3")
	if code != 1 {
		t.Errorf("guest failure should exit 1, got %d", code)
	}
	expect(t, "stdout keeps output", out, "partial")
	expect(t, "stderr", errOut, "web-1 [workstation]: exit status 3", "vb-dev-7 [vbox]: exit status 3")
}

func TestE2EIP(t *testing.T) {
	l := newLab(t)
	out, errOut, code := l.rift(t, nil, "ip", "-f", "DEV")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	expect(t, "ip", out, "VM", "IP", "web-1", "192.168.50.11", "vb-dev-7", "10.0.2.7", "not running")
}

func TestE2EConfigRespectsPowerStateAndBackend(t *testing.T) {
	l := newLab(t)
	out, errOut, code := l.rift(t, nil, "config", "cpu", "-f", "DEV", "--cores", "1", "--sockets", "1")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	// web-1 is running: the VMX must not be touched (the old gate never fired).
	expect(t, "stderr", errOut, "web-1 [workstation]: must be powered off to change CPU",
		"vb-dev-7 [vbox]: not supported on VirtualBox")
	expect(t, "stdout", out, "web-2 [workstation] → CPU: 1 vCPUs (1 sockets x 1 cores)")
	data, _ := os.ReadFile(filepath.Join(l.vms, "web-1.vmx"))
	reject(t, "running VM's VMX", string(data), "coresPerSocket")
}

func TestE2EDispatch(t *testing.T) {
	l := newLab(t)
	summary := filepath.Join(l.dir, "summary.md")
	env := []string{"GITHUB_STEP_SUMMARY=" + summary, "RIFT_COMMAND=exec -f DEV echo \"node says $VM_NAME\""}
	out, errOut, code := l.rift(t, env, "dispatch", "--env", "RIFT_COMMAND")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	expect(t, "banner", out, "node testnode", "$ rift exec -f DEV echo \"node says $VM_NAME\"",
		"node testnode: VMware Workstation (4 VMs), VirtualBox (3 VMs)", "node says web-1")
	md, err := os.ReadFile(summary)
	must(t, err)
	expect(t, "summary", string(md), "### `rift exec -f DEV", "| web-1 | DEV | VMware Workstation | ✅ ok |",
		"| web-2 | DEV | VMware Workstation | ➖ skipped |", "node says vb-dev-7", "**exec:** 2 ok, 2 skipped, 0 failed")
}

func TestE2EDispatchMissingFolderIsNotice(t *testing.T) {
	l := newLab(t)
	summary := filepath.Join(l.dir, "summary.md")
	out, errOut, code := l.rift(t, []string{"GITHUB_STEP_SUMMARY=" + summary}, "dispatch", "start -f NOPE")
	if code != 0 {
		t.Fatalf("missing folder in pipeline mode should exit 0, got %d\n%s\n%s", code, out, errOut)
	}
	expect(t, "stdout", out, "notice: no VMs in folder 'NOPE' on node testnode")
	md, _ := os.ReadFile(summary)
	expect(t, "summary", string(md), "no VMs in folder 'NOPE'")
}

func TestE2EDispatchCapturesPlainOutput(t *testing.T) {
	l := newLab(t)
	summary := filepath.Join(l.dir, "summary.md")
	_, _, code := l.rift(t, []string{"GITHUB_STEP_SUMMARY=" + summary}, "dispatch", "rift list")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	md, _ := os.ReadFile(summary)
	expect(t, "summary", string(md), "```text", "web-1 [ON]")
}

func TestE2EDispatchErrors(t *testing.T) {
	l := newLab(t)
	for _, args := range [][]string{
		{"dispatch"},
		{"dispatch", "dispatch list"},
		{"dispatch", `list "unterminated`},
		{"dispatch", "--env", "RIFT_UNSET_VAR"},
		{"dispatch", "no-such-command"},
	} {
		if _, _, code := l.rift(t, nil, args...); code == 0 {
			t.Errorf("%v should fail", args)
		}
	}
}

func TestE2ERestart(t *testing.T) {
	l := newLab(t)
	out, errOut, code := l.rift(t, nil, "restart", "-f", "DEV")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	expect(t, "stdout", out, "web-1 [workstation] → restarted", "vb-dev-7 [vbox] → restarted")
	expect(t, "stderr", errOut, "web-2 [workstation]: already stopped")
	running, _ := os.ReadFile(filepath.Join(l.state, "running"))
	expect(t, "web-1 running again", string(running), "web-1.vmx")
}

// Regression: LoadSettings used to create ARCHIVE_PATH\OVF on every command
// and fail when it couldn't (the runner's NETWORK SERVICE account can't read
// C:\Users\<name>), which broke list/overview/start on the nodes.
func TestE2EUnreachableArchivePathDoesNotBreakCommands(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	must(t, os.WriteFile(blocker, nil, 0o644))
	l := newLab(t, "ARCHIVE_PATH="+filepath.Join(blocker, "vm-storage"))
	out, errOut, code := l.rift(t, nil, "list")
	if code != 0 {
		t.Fatalf("list should not depend on ARCHIVE_PATH: exit %d\n%s%s", code, out, errOut)
	}
	expect(t, "list", out, "web-1 [ON]")
}

// Regression: overview printed "No VMs detected" and exited 0 when settings
// or a backend failed. It must report the error and exit 1, and still show
// the backends that worked.
func TestE2EOverviewReportsErrors(t *testing.T) {
	l := newLab(t)
	must(t, os.WriteFile(filepath.Join(l.dir, "vmrun"), []byte("#!/bin/sh\necho 'Error: vmrun is broken' >&2\nexit 1\n"), 0o755))
	out, errOut, code := l.rift(t, nil, "overview")
	if code != 1 {
		t.Errorf("a failing backend should make overview exit 1, got %d", code)
	}
	expect(t, "stderr", errOut, "skipping VMware Workstation", "vmrun is broken")
	expect(t, "stdout", out, "VirtualBox", "vb-dev-7")

	_, errOut, code = l.rift(t, []string{"ENV_PATH=" + filepath.Join(l.dir, "missing.env")}, "overview")
	if code != 1 {
		t.Errorf("a missing .env should make overview exit 1, got %d", code)
	}
	expect(t, "stderr", errOut, "loading .env")
}

// The GitHub runner pre-loads <runner dir>\.env verbatim, so a quoted value
// arrives in the environment with its quotes. The parsed .env value must win.
func TestE2ERunnerPreloadedEnvIsReparsed(t *testing.T) {
	l := newLab(t)
	vbox := filepath.Join(l.dir, "VBoxManage")
	must(t, os.WriteFile(l.env, []byte(strings.Replace(mustRead(t, l.env),
		"VBOX_MANAGE_PATH="+vbox, `VBOX_MANAGE_PATH="`+vbox+`"   # quoted, with a comment`, 1)), 0o644))
	raw := `VBOX_MANAGE_PATH="` + vbox + `"   # quoted, with a comment`
	out, errOut, code := l.rift(t, []string{raw}, "list")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	expect(t, "list", out, "VirtualBox", "vb-dev-7")
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	return string(data)
}
