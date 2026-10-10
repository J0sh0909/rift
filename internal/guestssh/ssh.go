// Package guestssh runs commands in guests over SSH. It is the exec transport
// for guests the hypervisor gives no native command channel to (Hyper-V Linux
// guests: PowerShell Direct only supports Windows).
package guestssh

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Config describes how to reach a guest.
type Config struct {
	Host     string
	Port     int // default 22
	User     string
	Password string

	// KnownHostsPath is the known_hosts file used for trust-on-first-use host
	// key checking: an unknown host's key is recorded on first connect and
	// must match afterwards. Default: ~/.rift/known_hosts.
	KnownHostsPath string

	DialTimeout time.Duration // default 10s
	Timeout     time.Duration // whole-command limit; 0 = none
}

// Run executes script with interpreter (default /bin/sh) on the guest and
// returns its combined output and exit code. err is reserved for connection,
// authentication and host key failures; a command that ran and failed is
// reported through exitCode (-1 when the guest did not report one).
func Run(cfg Config, interpreter, script string) (output string, exitCode int, err error) {
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if interpreter == "" {
		interpreter = "/bin/sh"
	}
	hostKeyCallback, err := tofuCallback(cfg.KnownHostsPath)
	if err != nil {
		return "", -1, err
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User: cfg.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(cfg.Password),
			// Many distros only enable keyboard-interactive for passwords.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = cfg.Password
				}
				return answers, nil
			}),
		},
		HostKeyCallback: hostKeyCallback,
		Timeout:         cfg.DialTimeout,
	})
	if err != nil {
		return "", -1, fmt.Errorf("ssh %s@%s: %w", cfg.User, addr, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", -1, fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var out lockedBuffer
	session.Stdout = &out
	session.Stderr = &out

	// The script travels as a single quoted -c argument, so the guest's stdin
	// stays empty and commands in the script cannot swallow later lines.
	command := shellQuote(interpreter) + " -c " + shellQuote(script)
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	var runErr error
	if cfg.Timeout > 0 {
		select {
		case runErr = <-done:
		case <-time.After(cfg.Timeout):
			_ = session.Signal(ssh.SIGKILL)
			client.Close()
			return out.String(), -1, fmt.Errorf("command timed out after %s", cfg.Timeout)
		}
	} else {
		runErr = <-done
	}

	var exitErr *ssh.ExitError
	var missing *ssh.ExitMissingError
	switch {
	case runErr == nil:
		return out.String(), 0, nil
	case errors.As(runErr, &exitErr):
		return out.String(), exitErr.ExitStatus(), nil
	case errors.As(runErr, &missing):
		return out.String(), -1, nil
	default:
		return out.String(), -1, fmt.Errorf("ssh run: %w", runErr)
	}
}

var knownHostsMu sync.Mutex

// DefaultKnownHostsPath returns ~/.rift/known_hosts.
func DefaultKnownHostsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".rift", "known_hosts")
	}
	return filepath.Join(home, ".rift", "known_hosts")
}

// tofuCallback verifies host keys against path, recording unknown hosts.
func tofuCallback(path string) (ssh.HostKeyCallback, error) {
	if path == "" {
		path = DefaultKnownHostsPath()
	}
	knownHostsMu.Lock()
	defer knownHostsMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating known_hosts dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening known_hosts: %w", err)
	}
	f.Close()

	check, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := check(hostname, remote, key)
		var keyErr *knownhosts.KeyError
		if err == nil || !errors.As(err, &keyErr) {
			return err
		}
		if len(keyErr.Want) > 0 {
			return fmt.Errorf("host key for %s changed - if the VM was rebuilt, remove its line from %s: %w", hostname, path, err)
		}
		// Unknown host: trust on first use.
		knownHostsMu.Lock()
		defer knownHostsMu.Unlock()
		af, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("recording host key: %w", err)
		}
		defer af.Close()
		_, err = fmt.Fprintln(af, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
		return err
	}, nil
}

func shellQuote(s string) string {
	return "'" + string(bytes.ReplaceAll([]byte(s), []byte("'"), []byte(`'\''`))) + "'"
}

// lockedBuffer is a bytes.Buffer safe for the concurrent stdout/stderr
// writers of an ssh.Session.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
