package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestGuestFamily(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"windows10-64":        FamilyWindows, // VMware
		"winXPPro":            FamilyWindows, // VMware legacy
		"Windows11_64":        FamilyWindows, // VirtualBox
		"Windows 10 Pro":      FamilyWindows, // Hyper-V KVP
		"ubuntu-64":           FamilyUnix,
		"Ubuntu_64":           FamilyUnix,
		"Ubuntu 22.04.3 LTS":  FamilyUnix,
		"RedHat_64":           FamilyUnix,
		"Linux26_64":          FamilyUnix,
		"other5xlinux-64":     FamilyUnix,
		"Debian GNU/Linux 12": FamilyUnix,
		"darwin21-64":         FamilyUnix,
		"Other_64":            "",
		"other-64":            "",
	}
	for in, want := range cases {
		if got := GuestFamily(in); got != want {
			t.Errorf("GuestFamily(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFamilyFromName(t *testing.T) {
	cases := map[string]string{
		"H26-W-DC":  FamilyWindows,
		"h26-u-web": FamilyUnix,
		"router":    "",
	}
	for in, want := range cases {
		if got := FamilyFromName(in); got != want {
			t.Errorf("FamilyFromName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInterpreterFor(t *testing.T) {
	cases := []struct {
		os, family, want string
		ok               bool
	}{
		{"ubuntu-64", "", "/bin/bash", true},
		{"Ubuntu_64", FamilyUnix, "/bin/bash", true},
		{"Linux26_64", FamilyUnix, "/bin/sh", true},
		{"", FamilyWindows, WindowsShell, true},
		{"", "", "", false},
	}
	for _, c := range cases {
		got, ok := InterpreterFor(c.os, c.family)
		if got != c.want || ok != c.ok {
			t.Errorf("InterpreterFor(%q,%q) = %q,%v want %q,%v", c.os, c.family, got, ok, c.want, c.ok)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got := ShellQuote(`it's "x"`); got != `'it'\''s "x"'` {
		t.Errorf("ShellQuote = %s", got)
	}
}

// TestWrapShellCapture runs the wrapper through a real POSIX shell to prove it
// captures every command's output (the old redirect only captured the last
// command), survives an explicit exit, and records the exit status.
func TestWrapShellCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	cases := []struct {
		script string
		want   string
		code   int
	}{
		{"echo one; echo two", "one\ntwo\n", 0},
		{"echo out; echo err >&2; false", "out\nerr\n", 1},
		{"printf partial; exit 7", "partial", 7},
		{"case x in x) echo matched;; esac", "matched\n", 0},
	}
	for _, c := range cases {
		out := filepath.Join(t.TempDir(), "o'ut.txt")
		wrapped := WrapShellCapture(c.script, out)
		cmd := exec.Command(sh, "-c", wrapped)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("wrapper itself failed for %q: %v %s", c.script, err, b)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		got, code, ok := SplitExitTrailer(string(raw))
		if !ok || got != c.want || code != c.code {
			t.Errorf("%q: got (%q, %d, %v), want (%q, %d)", c.script, got, code, ok, c.want, c.code)
		}
	}
}

func TestSplitExitTrailerAbsent(t *testing.T) {
	out, code, ok := SplitExitTrailer("plain output\n")
	if ok || code != -1 || out != "plain output\n" {
		t.Errorf("got (%q, %d, %v)", out, code, ok)
	}
}

func TestExitCodeFromText(t *testing.T) {
	cases := map[string]int{
		"Error: Guest program exited with non-zero exit code: 3": 3,
		"The program exited with code: 1":                        1,
		"Error: Invalid user name or password":                   -1,
	}
	for in, want := range cases {
		if got := ExitCodeFromText(in); got != want {
			t.Errorf("ExitCodeFromText(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestFirstIPv4(t *testing.T) {
	cases := map[string]string{
		"/VirtualBox/GuestInfo/Net/0/V4/IP = '10.0.2.15' @ 2024-01-01T00:00:00Z":                                 "10.0.2.15",
		"Name: /VirtualBox/GuestInfo/Net/1/V4/IP, value: 192.168.56.101, timestamp: 1700000000000000000, flags:": "192.168.56.101",
		"127.0.0.1 169.254.3.4 172.16.0.9": "172.16.0.9",
		"unknown":                          "",
		"999.1.1.1":                        "",
	}
	for in, want := range cases {
		if got := FirstIPv4(in); got != want {
			t.Errorf("FirstIPv4(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitCommandLine(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"start -f DEV", []string{"start", "-f", "DEV"}},
		{"  exec   -f DEV  ip a ", []string{"exec", "-f", "DEV", "ip", "a"}},
		{`snapshot create vm -n "before upgrade"`, []string{"snapshot", "create", "vm", "-n", "before upgrade"}},
		{`exec vm 'echo "$HOME"'`, []string{"exec", "vm", `echo "$HOME"`}},
		{`exec -f WIN dir C:\Users\runner`, []string{"exec", "-f", "WIN", "dir", `C:\Users\runner`}},
		{`--name="a b"c`, []string{"--name=a bc"}},
		{`x "say \"hi\" \\ \n"`, []string{"x", `say "hi" \ \n`}},
		{`x ""`, []string{"x", ""}},
		{"", nil},
	}
	for _, c := range cases {
		got, err := SplitCommandLine(c.in)
		if err != nil {
			t.Errorf("SplitCommandLine(%q) error: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitCommandLine(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{`a "b`, `a 'b`} {
		if _, err := SplitCommandLine(bad); err == nil {
			t.Errorf("SplitCommandLine(%q) should fail", bad)
		}
	}
}
