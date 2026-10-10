package guestssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// startServer runs a minimal SSH server that executes "exec" requests with
// the local sh and returns host:port.
func startServer(t *testing.T, password string) (string, int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test server shells out to sh")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if c.User() == "runner" && string(p) == password {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConn(conn, cfg)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func serveConn(conn net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, requests, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range requests {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				ssh.Unmarshal(req.Payload, &payload)
				req.Reply(true, nil)
				cmd := exec.Command("sh", "-c", payload.Command)
				cmd.Stdout = ch
				cmd.Stderr = ch.Stderr()
				code := 0
				if err := cmd.Run(); err != nil {
					if ee, ok := err.(*exec.ExitError); ok {
						code = ee.ExitCode()
					} else {
						code = 255
					}
				}
				status := make([]byte, 4)
				binary.BigEndian.PutUint32(status, uint32(code))
				ch.SendRequest("exit-status", false, status)
				return
			}
		}()
	}
}

func TestRun(t *testing.T) {
	host, port := startServer(t, "s3cret")
	kh := filepath.Join(t.TempDir(), "sub", "known_hosts")
	cfg := Config{Host: host, Port: port, User: "runner", Password: "s3cret", KnownHostsPath: kh}

	out, code, err := Run(cfg, "/bin/sh", "echo one; echo 'it'\"'\"'s' >&2; exit 3")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 3 || !strings.Contains(out, "one") || !strings.Contains(out, "it's") {
		t.Fatalf("got code %d output %q", code, out)
	}

	data, err := os.ReadFile(kh)
	if err != nil || !strings.Contains(string(data), "ssh-ed25519") {
		t.Fatalf("host key not recorded on first use: %q %v", data, err)
	}

	// Second connection verifies against the recorded key.
	if _, code, err := Run(cfg, "", "true"); err != nil || code != 0 {
		t.Fatalf("second run: code %d err %v", code, err)
	}
}

func TestRunWrongPassword(t *testing.T) {
	host, port := startServer(t, "s3cret")
	cfg := Config{Host: host, Port: port, User: "runner", Password: "nope",
		KnownHostsPath: filepath.Join(t.TempDir(), "known_hosts")}
	if _, _, err := Run(cfg, "", "true"); err == nil {
		t.Fatal("expected auth failure")
	}
}

func TestRunHostKeyChanged(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	host, port := startServer(t, "pw")
	cfg := Config{Host: host, Port: port, User: "runner", Password: "pw", KnownHostsPath: kh}
	if _, _, err := Run(cfg, "", "true"); err != nil {
		t.Fatal(err)
	}
	// Point the recorded entry at a different key to simulate a rebuilt VM.
	data, _ := os.ReadFile(kh)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	fields := strings.Fields(string(data))
	forged := fields[0] + " " + string(ssh.MarshalAuthorizedKey(otherSigner.PublicKey()))
	os.WriteFile(kh, []byte(forged), 0o600)

	_, _, err := Run(cfg, "", "true")
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("expected host key mismatch, got %v", err)
	}
}
