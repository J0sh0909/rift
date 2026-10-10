package cmd

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/J0sh0909/rift/internal/core"
	"github.com/spf13/cobra"
)

var (
	bootstrapUserFlag       string
	bootstrapPassFlag       string
	bootstrapRunnerUserFlag string
	bootstrapRunnerPassFlag string
)

// ---------------------------------------------------------------------------
// Windows helpers (cmd.exe through the backend's guest program channel)
// ---------------------------------------------------------------------------

// winCmd runs a single cmd.exe /c command in a Windows guest and returns any error.
// The caller's user/pass are assumed to be admin-level; no hostname retry fallback.
func winCmd(t core.Target, user, pass, arg string) error {
	output, err := t.HV.RunGuestProgram(t.Path, user, pass, "", "", core.WindowsShell, "/c "+arg)
	if err != nil {
		text := strings.ToLower(output + " " + err.Error())
		// English and French Windows messages (accents may be mangled by
		// the console code page, hence the prefixes).
		if strings.Contains(text, "password does not meet complexity requirements") ||
			strings.Contains(text, "le mot de passe ne respecte pas") ||
			strings.Contains(text, "complexit") {
			return fmt.Errorf("password rejected by Windows GPO: %w", err)
		}
		if strings.Contains(text, "access is denied") || strings.Contains(text, "refus") {
			return fmt.Errorf("access denied (check UAC/privileges): %w", err)
		}
	}
	return err
}

// validateWindowsPassword checks if a password meets basic Windows complexity requirements
func validateWindowsPassword(password string) error {
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, ch := range password {
		switch {
		case 'A' <= ch && ch <= 'Z':
			hasUpper = true
		case 'a' <= ch && ch <= 'z':
			hasLower = true
		case '0' <= ch && ch <= '9':
			hasDigit = true
		case ch < 128:
			hasSpecial = true
		}
	}
	switch {
	case !hasUpper:
		return fmt.Errorf("password must contain at least one uppercase letter")
	case !hasLower:
		return fmt.Errorf("password must contain at least one lowercase letter")
	case !hasDigit:
		return fmt.Errorf("password must contain at least one digit")
	case !hasSpecial:
		return fmt.Errorf("password must contain at least one special character")
	}
	lowerPass := strings.ToLower(password)
	for _, weak := range []string{"password", "123456", "qwerty", "admin", "welcome"} {
		if strings.Contains(lowerPass, weak) {
			return fmt.Errorf("password contains common weak pattern")
		}
	}
	return nil
}

// bootstrapWindowsCreate provisions the runner user on a Windows guest using
// pure cmd.exe commands - no PowerShell or script download required.
func bootstrapWindowsCreate(t core.Target, user, pass, ru, rp string) result {
	if err := validateWindowsPassword(rp); err != nil {
		return failed(t, core.ErrBootstrapWindows, "password validation failed: %s. Use a stronger password (14+ chars, upper/lower/digit/special)", err)
	}

	// Create the user, or reset its password if it exists.
	if err := winCmd(t, user, pass, `net user `+ru+` >nul 2>&1`); err != nil {
		if err := winCmd(t, user, pass, `net user `+ru+` "`+rp+`" /add`); err != nil {
			return failed(t, core.ErrBootstrapWindows, "create user failed (check Windows GPO password complexity requirements): %s", err)
		}
	} else if err := winCmd(t, user, pass, `net user `+ru+` "`+rp+`"`); err != nil {
		return failed(t, core.ErrBootstrapWindows, "reset password failed (check Windows GPO password complexity requirements): %s", err)
	}

	// Administrators group (English, then French locale name).
	if err := winCmd(t, user, pass, `net localgroup Administrators | findstr /c:"`+ru+`" >nul 2>&1`); err != nil {
		if err := winCmd(t, user, pass, `net localgroup Administrators `+ru+` /add`); err != nil {
			if err2 := winCmd(t, user, pass, `net localgroup Administrateurs `+ru+` /add`); err2 != nil {
				return failed(t, core.ErrBootstrapWindows, "add to admin group failed: %s", err)
			}
		}
	}

	// Disable UAC (EnableLUA = 0) if not already.
	if err := winCmd(t, user, pass, `reg query HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System /v EnableLUA 2>&1 | findstr /c:"0x0" >nul`); err != nil {
		if err := winCmd(t, user, pass, `reg add HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System /v EnableLUA /t REG_DWORD /d 0 /f >nul 2>&1`); err != nil {
			return failed(t, core.ErrBootstrapWindows, "disable UAC failed: %s", err)
		}
	}

	// Disable remote UAC token filtering (allows elevated execution via vmrun without full UAC disable/reboot)
	if err := winCmd(t, user, pass, `reg query HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System /v LocalAccountTokenFilterPolicy 2>&1 | findstr /c:"0x1" >nul`); err != nil {
		if err := winCmd(t, user, pass, `reg add HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System /v LocalAccountTokenFilterPolicy /t REG_DWORD /d 1 /f >nul 2>&1`); err != nil {
			return failed(t, core.ErrBootstrapWindows, "disable remote UAC token filtering failed: %s", err)
		}
	}

	// Disable password expiry if not already.
	if err := winCmd(t, user, pass, `wmic useraccount where name='`+ru+`' get PasswordExpires 2>&1 | findstr /c:"FALSE" >nul`); err != nil {
		if err := winCmd(t, user, pass, `wmic useraccount where name='`+ru+`' set PasswordExpires=FALSE >nul 2>&1`); err != nil {
			return failed(t, core.ErrBootstrapWindows, "disable password expiry failed: %s", err)
		}
	}

	return success(t, "bootstrap complete")
}

// bootstrapWindowsVerify checks the runner user on a Windows guest. The
// per-check lines are returned as the result's output.
func bootstrapWindowsVerify(t core.Target, user, pass, ru string) result {
	var out strings.Builder
	allOK := true
	check := func(passed bool, okMsg, failMsg string) {
		if passed {
			fmt.Fprintf(&out, "[OK]   %s\n", okMsg)
		} else {
			fmt.Fprintf(&out, "[FAIL] %s\n", failMsg)
			allOK = false
		}
	}

	// Windows locks accounts after failed auth attempts: unlock first.
	// >nul 2>&1 is required - runProgramInGuest returns bogus exit codes
	// when commands produce console output.
	if winCmd(t, user, pass, `net user `+ru+` | findstr /R "Compte.*: actif.*Non\|Account active.*No\|Account active.*Locked" >nul 2>&1`) == nil {
		_ = winCmd(t, user, pass, `net user `+ru+` /active:yes >nul 2>&1`)
		out.WriteString("[UNLOCK] unlocked locked account\n")
	}

	check(winCmd(t, user, pass, `net user `+ru+` >nul 2>&1`) == nil,
		fmt.Sprintf("user '%s' exists", ru), fmt.Sprintf("user '%s' does not exist", ru))

	// "Administ" is the common prefix across locales (EN/FR/DE/ES); fall
	// back to listing the group by its English or French name.
	inAdmin := winCmd(t, user, pass, `net user `+ru+` | findstr /I Administ >nul 2>&1`) == nil ||
		winCmd(t, user, pass, `net localgroup Administrators | findstr /I "`+ru+`" >nul 2>&1`) == nil ||
		winCmd(t, user, pass, `net localgroup Administrateurs | findstr /I "`+ru+`" >nul 2>&1`) == nil
	check(inAdmin, fmt.Sprintf("user '%s' is in Administrators", ru), fmt.Sprintf("user '%s' is not in Administrators", ru))

	check(winCmd(t, user, pass, `reg query HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System /v EnableLUA | findstr 0x0 >nul 2>&1`) == nil,
		"UAC is disabled", "UAC is not disabled")

	// "net user <name>" shows "Jamais" (FR) or "Never" (EN) when expiry is off.
	noExpiry := winCmd(t, user, pass, `net user `+ru+` | findstr /I Jamais >nul 2>&1`) == nil ||
		winCmd(t, user, pass, `net user `+ru+` | findstr /I Never >nul 2>&1`) == nil
	check(noExpiry, "password expiry disabled", "password expiry is not disabled")

	r := success(t, "verify passed")
	if !allOK {
		r = failed(t, core.ErrBootstrapWindows, "verify failed")
	}
	r.output = out.String()
	return r
}

// bootstrapWindowsReset sets a new password for the runner user on a Windows guest.
func bootstrapWindowsReset(t core.Target, user, pass, ru, rp string) result {
	if err := winCmd(t, user, pass, `net user `+ru+` "`+rp+`"`); err != nil {
		return failed(t, core.ErrBootstrapWindows, "password reset failed: %s", err)
	}
	return success(t, "password reset complete")
}

// bootstrapWindowsRevoke removes the runner user from a Windows guest.
func bootstrapWindowsRevoke(t core.Target, user, pass, ru string) result {
	if err := winCmd(t, user, pass, `net user `+ru+` /delete`); err != nil {
		return failed(t, core.ErrBootstrapWindows, "revoke failed: %s", err)
	}
	return success(t, "revoke complete")
}

// ---------------------------------------------------------------------------
// Linux helpers
// ---------------------------------------------------------------------------

// linuxSudoScript wraps inner so it runs as root. inner is written to a
// private temp file and the password is piped to "sudo -S". (Feeding the
// script through a heredoc doesn't work: the heredoc replaces sudo's stdin,
// so sudo reads the script's first line as the password.) Both travel
// base64-encoded, so no quoting can break them.
func linuxSudoScript(pass, inner string) string {
	script := base64.StdEncoding.EncodeToString([]byte(inner))
	pw := base64.StdEncoding.EncodeToString([]byte(pass + "\n"))
	return `f=$(mktemp /tmp/rift-bootstrap.XXXXXX) || exit 1
printf '%s' '` + script + `' | base64 -d > "$f"
if [ "$(id -u)" = 0 ]; then
  bash "$f"
else
  printf '%s' '` + pw + `' | base64 -d | sudo -S -p '' bash "$f"
fi
rc=$?
rm -f "$f"
exit $rc
`
}

// Linux bootstrap scripts. They run as root through linuxSudoScript; values
// are injected with core.ShellQuote. "sudo" is used where it exists,
// "wheel" otherwise (RHEL family).
func linuxCreateScript(ru, rp string) string {
	return `set -e
RUNNER_USER=` + core.ShellQuote(ru) + `
RUNNER_PASS=` + core.ShellQuote(rp) + `
if id "$RUNNER_USER" >/dev/null 2>&1; then
  echo "User $RUNNER_USER already exists"
else
  useradd -m -s /bin/bash "$RUNNER_USER"
  echo "Created user $RUNNER_USER"
fi
printf '%s:%s\n' "$RUNNER_USER" "$RUNNER_PASS" | chpasswd
if getent group sudo >/dev/null 2>&1; then ADMIN_GROUP=sudo; else ADMIN_GROUP=wheel; fi
if ! id -nG "$RUNNER_USER" | tr ' ' '\n' | grep -qx "$ADMIN_GROUP"; then
  usermod -aG "$ADMIN_GROUP" "$RUNNER_USER"
  echo "Added $RUNNER_USER to $ADMIN_GROUP"
fi
mkdir -p /etc/sudoers.d
printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$RUNNER_USER" > "/etc/sudoers.d/99-$RUNNER_USER"
chmod 440 "/etc/sudoers.d/99-$RUNNER_USER"
chage -m 0 -M 99999 -I -1 -E -1 "$RUNNER_USER"
echo "Bootstrap complete for $RUNNER_USER"
`
}

func linuxVerifyScript(ru string) string {
	return `RUNNER_USER=` + core.ShellQuote(ru) + `
if ! id "$RUNNER_USER" >/dev/null 2>&1; then echo "[FAIL] user '$RUNNER_USER' does not exist"; exit 1; fi
echo "[OK]   user '$RUNNER_USER' exists"
if ! id -nG "$RUNNER_USER" | tr ' ' '\n' | grep -qxE 'sudo|wheel'; then echo "[FAIL] user '$RUNNER_USER' is not in sudo/wheel"; exit 1; fi
echo "[OK]   user '$RUNNER_USER' is in sudo/wheel"
if [ ! -f "/etc/sudoers.d/99-$RUNNER_USER" ]; then echo "[FAIL] sudoers file not configured"; exit 1; fi
echo "[OK]   sudoers file configured"
`
}

func linuxResetScript(ru, rp string) string {
	return `set -e
RUNNER_USER=` + core.ShellQuote(ru) + `
RUNNER_PASS=` + core.ShellQuote(rp) + `
if ! id "$RUNNER_USER" >/dev/null 2>&1; then echo "User $RUNNER_USER does not exist"; exit 1; fi
printf '%s:%s\n' "$RUNNER_USER" "$RUNNER_PASS" | chpasswd
echo "Password reset for $RUNNER_USER"
`
}

func linuxRevokeScript(ru string) string {
	return `RUNNER_USER=` + core.ShellQuote(ru) + `
if id "$RUNNER_USER" >/dev/null 2>&1; then
  userdel -r "$RUNNER_USER" 2>/dev/null || userdel "$RUNNER_USER"
  rm -f "/etc/sudoers.d/99-$RUNNER_USER"
  echo "User $RUNNER_USER revoked"
else
  echo "User $RUNNER_USER does not exist"
fi
`
}

// bootstrapRunLinux runs inner as root on a Linux guest.
func bootstrapRunLinux(t core.Target, user, pass, inner, successMsg string) result {
	if _, err := t.HV.RunGuestCommand(t.Path, user, pass, "/bin/bash", linuxSudoScript(pass, inner), "", ""); err != nil {
		return failed(t, core.ErrBootstrapLinux, "failed (check sudo access): %s", err)
	}
	return success(t, "%s", successMsg)
}

// ---------------------------------------------------------------------------
// Credentials
// ---------------------------------------------------------------------------

// bootstrapAdminAuth resolves guest admin credentials from flags only - no
// .env fallback, since create/revoke must run as an admin.
func bootstrapAdminAuth() (user, pass string, ok bool) {
	if bootstrapUserFlag == "" || bootstrapPassFlag == "" {
		fail(core.ErrConfig, "--user and --pass are required (admin credentials needed)")
		return "", "", false
	}
	return bootstrapUserFlag, bootstrapPassFlag, true
}

// bootstrapRunnerUser returns the runner username, defaulting to "runner".
func bootstrapRunnerUser() string {
	if bootstrapRunnerUserFlag != "" {
		return bootstrapRunnerUserFlag
	}
	if settings.DefaultUser != "" {
		return settings.DefaultUser
	}
	return "runner"
}

func bootstrapRunnerPass() (string, bool) {
	if bootstrapRunnerPassFlag != "" {
		return bootstrapRunnerPassFlag, true
	}
	if settings.DefaultPass != "" {
		return settings.DefaultPass, true
	}
	fail(core.ErrConfig, "runner password required: use --runner-pass or set VM_DEFAULT_PASS in .env")
	return "", false
}

// bootstrapGuest checks that a target can be bootstrapped and returns
// whether it is a Windows guest.
func bootstrapGuest(t core.Target) (windows bool, r result, okToRun bool) {
	if !t.Running {
		return false, skipped(t, core.ErrNotRunning, "not running"), false
	}
	family := guestFamilyOf(t, guestOSOf(t))
	if family == "" {
		return false, failed(t, core.ErrGuestOSNotDet, "guest OS not set - %s", osHint(t)), false
	}
	return family == core.FamilyWindows, result{}, true
}

// workstationWindowsLimit is returned for Windows guests on VMware, where
// VMware Tools' authentication policy blocks automated provisioning.
func workstationWindowsLimit(t core.Target) result {
	return failed(t, core.ErrBootstrapWindows, "Automated provisioning on %q is limited by VMware Tools authentication policy. Please manually create the user on the guest VM if this continues to fail.", t.Backend)
}

// bootstrapPlan runs bootstrap operations in parallel; guest operations
// fail for real reasons (credentials, policy), so they are not retried.
var bootstrapPlan = plan{verb: "bootstrap"}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap <target>",
	Short: "Provision and manage the automation user on guest VMs",
	Run: func(cmd *cobra.Command, args []string) {
		bootstrapCreateCmd.Run(cmd, args)
	},
}

var bootstrapCreateCmd = &cobra.Command{
	Use:   "create <target>",
	Short: "Provision the automation user on a guest VM",
	Run: func(cmd *cobra.Command, args []string) {
		loadSettings()
		user, pass, ok := bootstrapAdminAuth()
		if !ok {
			return
		}
		ru := bootstrapRunnerUser()
		rp, ok := bootstrapRunnerPass()
		if !ok {
			return
		}
		targets := resolveTargets(args, needTargets)
		report(bootstrapPlan, run(targets, bootstrapPlan, func(t core.Target) result {
			windows, r, ok := bootstrapGuest(t)
			if !ok {
				return r
			}
			if windows {
				if t.Backend == "workstation" {
					return workstationWindowsLimit(t)
				}
				return bootstrapWindowsCreate(t, user, pass, ru, rp)
			}
			return bootstrapRunLinux(t, user, pass, linuxCreateScript(ru, rp), "bootstrap complete")
		}))
	},
}

var bootstrapVerifyCmd = &cobra.Command{
	Use:   "verify <target>",
	Short: "Verify the automation user is correctly provisioned on a guest VM",
	Run: func(cmd *cobra.Command, args []string) {
		s := loadSettings()
		adminUser, adminPass, ok := bootstrapAdminAuth()
		if !ok {
			return
		}
		// Runner credentials for the final auth test always come from .env.
		runnerUser, runnerPass := s.DefaultUser, s.DefaultPass
		ru := bootstrapRunnerUser()
		targets := resolveTargets(args, needTargets)
		report(bootstrapPlan, run(targets, bootstrapPlan, func(t core.Target) result {
			windows, r, ok := bootstrapGuest(t)
			if !ok {
				return r
			}
			if windows {
				r = bootstrapWindowsVerify(t, adminUser, adminPass, ru)
			} else {
				r = bootstrapRunLinux(t, adminUser, adminPass, linuxVerifyScript(ru), "verify passed")
			}
			if r.status != statusOK {
				return r
			}
			output := r.output
			if runnerUser == "" || runnerPass == "" {
				r = success(t, "verify passed (runner auth skipped - VM_DEFAULT_USER/VM_DEFAULT_PASS not set)")
				r.output = output
				return r
			}
			var authErr error
			switch {
			case windows && t.Backend == "workstation":
				// runProgramInGuest rejects valid credentials for non-original
				// users on Windows 10. listProcessesInGuest works reliably.
				authErr = t.HV.ListGuestProcesses(t.Path, runnerUser, runnerPass, adminUser, adminPass)
			case windows:
				_, authErr = t.HV.RunGuestProgram(t.Path, runnerUser, runnerPass, adminUser, adminPass, core.WindowsShell, "/c exit 0")
			default:
				_, authErr = t.HV.RunGuestCommand(t.Path, runnerUser, runnerPass, "/bin/bash", "whoami", adminUser, adminPass)
			}
			if authErr == nil {
				r = success(t, "verify passed (runner auth OK)")
			} else {
				r = success(t, "verify passed (runner auth failed - check password)")
			}
			r.output = output
			return r
		}))
	},
}

var bootstrapResetCmd = &cobra.Command{
	Use:   "reset <target>",
	Short: "Reset the automation user password on a guest VM",
	Run: func(cmd *cobra.Command, args []string) {
		loadSettings()
		user, pass, ok := bootstrapAdminAuth()
		if !ok {
			return
		}
		ru := bootstrapRunnerUser()
		rp, ok := bootstrapRunnerPass()
		if !ok {
			return
		}
		targets := resolveTargets(args, needTargets)
		report(bootstrapPlan, run(targets, bootstrapPlan, func(t core.Target) result {
			windows, r, ok := bootstrapGuest(t)
			if !ok {
				return r
			}
			if windows {
				if t.Backend == "workstation" {
					return workstationWindowsLimit(t)
				}
				return bootstrapWindowsReset(t, user, pass, ru, rp)
			}
			return bootstrapRunLinux(t, user, pass, linuxResetScript(ru, rp), "password reset complete")
		}))
	},
}

var bootstrapRevokeCmd = &cobra.Command{
	Use:   "revoke <target>",
	Short: "Remove the automation user from a guest VM",
	Run: func(cmd *cobra.Command, args []string) {
		loadSettings()
		user, pass, ok := bootstrapAdminAuth()
		if !ok {
			return
		}
		ru := bootstrapRunnerUser()
		targets := resolveTargets(args, needTargets)
		report(bootstrapPlan, run(targets, bootstrapPlan, func(t core.Target) result {
			windows, r, ok := bootstrapGuest(t)
			if !ok {
				return r
			}
			if windows {
				if t.Backend == "workstation" {
					return workstationWindowsLimit(t)
				}
				return bootstrapWindowsRevoke(t, user, pass, ru)
			}
			return bootstrapRunLinux(t, user, pass, linuxRevokeScript(ru), "revoke complete")
		}))
	},
}

func init() {
	rootCmd.AddCommand(bootstrapCmd)
	bootstrapCmd.AddCommand(bootstrapCreateCmd, bootstrapVerifyCmd, bootstrapResetCmd, bootstrapRevokeCmd)
	addFolderFlag(bootstrapCmd, bootstrapCreateCmd, bootstrapVerifyCmd, bootstrapResetCmd, bootstrapRevokeCmd)

	// Persistent flags shared by all subcommands.
	bootstrapCmd.PersistentFlags().StringVarP(&bootstrapUserFlag, "user", "u", "", "Guest OS username with admin/root privileges")
	bootstrapCmd.PersistentFlags().StringVarP(&bootstrapPassFlag, "pass", "p", "", "Guest OS password")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapRunnerUserFlag, "runner-user", "", "Automation username to act on (default: runner)")
	// --runner-pass on the parent (alias for create) and on subcommands that set it.
	bootstrapCmd.Flags().StringVar(&bootstrapRunnerPassFlag, "runner-pass", "", "Password to set for the automation user (required)")
	bootstrapCreateCmd.Flags().StringVar(&bootstrapRunnerPassFlag, "runner-pass", "", "Password to set for the automation user (required)")
	bootstrapResetCmd.Flags().StringVar(&bootstrapRunnerPassFlag, "runner-pass", "", "New password for the automation user (required)")
}
