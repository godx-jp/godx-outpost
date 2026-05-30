package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// serviceLabel is the launchd/systemd service identifier.
const serviceLabel = "jp.godx.outpost"

// installCmd registers outpost as a per-user service that starts at login (and so
// comes back after a reboot). launchd on macOS, systemd --user on Linux.
func installCmd() *cobra.Command {
	var bind, port string
	var pairTTL time.Duration
	var restore bool
	var advertise, dashPort string
	var doOpen bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install outpost as a login service (launchd on macOS, systemd --user on Linux)",
		Long: `Install outpost so it runs in the background and restarts at login.

This makes outpost a long-lived daemon: combined with dtach-backed sessions, your
terminal sessions survive reboots of the daemon (and you only re-pair when a
token is revoked).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate outpost binary: %w", err)
			}
			exe, _ = filepath.Abs(exe)

			runArgs := []string{"start", "--bind", bind, "--port", port, "--pair-ttl", pairTTL.String()}
			if restore {
				runArgs = append(runArgs, "--restore") // auto re-open saved sessions after a reboot
			}
			// Bake the advertised URL into the service so the pairing QR is
			// reachable from the phone (essential for a remote machine — the bind
			// address alone is useless to the client).
			if advertise != "" {
				runArgs = append(runArgs, "--advertise", advertise)
			}
			if doOpen {
				runArgs = append(runArgs, "--open") // run the local dashboard (QR + devices)
			}
			if dashPort != "" {
				runArgs = append(runArgs, "--dashboard-port", dashPort)
			}
			if flagConfigDir != "" {
				runArgs = append(runArgs, "--config-dir", flagConfigDir)
			}

			switch runtime.GOOS {
			case "darwin":
				return installLaunchd(exe, runArgs)
			case "linux":
				return installSystemd(exe, runArgs)
			default:
				return fmt.Errorf("install is not supported on %s; run `outpost start` manually", runtime.GOOS)
			}
		},
	}
	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "bind address the service listens on")
	cmd.Flags().StringVar(&port, "port", "8722", "listen port")
	cmd.Flags().DurationVar(&pairTTL, "pair-ttl", 2*time.Minute, "pairing code lifetime")
	cmd.Flags().BoolVar(&restore, "restore", true, "auto re-open saved sessions on each startup (after a reboot); --restore=false to disable")
	cmd.Flags().StringVar(&advertise, "advertise", "", "URL embedded in the pairing QR that the phone connects to (e.g. ws://<tailscale-ip>:8722); required for a remote machine")
	cmd.Flags().BoolVar(&doOpen, "open", false, "run the local web dashboard (QR + devices) as part of the service")
	cmd.Flags().StringVar(&dashPort, "dashboard-port", "", "dashboard port on 127.0.0.1 (default: listen port + 1000)")
	return cmd
}

func uninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the outpost login service",
		RunE: func(cmd *cobra.Command, args []string) error {
			switch runtime.GOOS {
			case "darwin":
				return uninstallLaunchd()
			case "linux":
				return uninstallSystemd()
			default:
				return fmt.Errorf("uninstall is not supported on %s", runtime.GOOS)
			}
		},
	}
}

// ---- macOS (launchd) ---------------------------------------------------------

func launchdPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"), nil
}

func installLaunchd(exe string, runArgs []string) error {
	plistPath, err := launchdPlistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	logPath := filepath.Join(home, "Library", "Logs", "outpost.log")

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0"><dict>` + "\n")
	b.WriteString("  <key>Label</key><string>" + serviceLabel + "</string>\n")
	b.WriteString("  <key>ProgramArguments</key><array>\n")
	b.WriteString("    <string>" + xmlEscape(exe) + "</string>\n")
	for _, a := range runArgs {
		b.WriteString("    <string>" + xmlEscape(a) + "</string>\n")
	}
	b.WriteString("  </array>\n")
	b.WriteString("  <key>RunAtLoad</key><true/>\n")
	b.WriteString("  <key>KeepAlive</key><true/>\n")
	b.WriteString("  <key>StandardOutPath</key><string>" + xmlEscape(logPath) + "</string>\n")
	b.WriteString("  <key>StandardErrorPath</key><string>" + xmlEscape(logPath) + "</string>\n")
	b.WriteString("</dict></plist>\n")

	if err := os.WriteFile(plistPath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// Reload: unload first (ignore error), then load.
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load: %v: %s", err, out)
	}

	fmt.Printf("Installed launchd service %s\n", serviceLabel)
	fmt.Printf("  plist : %s\n", plistPath)
	fmt.Printf("  logs  : %s\n", logPath)
	fmt.Printf("outpost now runs in the background and starts at login.\n")
	fmt.Printf("Get a pairing code anytime with: outpost pair   (or read the log)\n")
	return nil
}

func uninstallLaunchd() error {
	plistPath, err := launchdPlistPath()
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	fmt.Printf("Removed launchd service %s\n", serviceLabel)
	return nil
}

// ---- Linux (systemd --user) --------------------------------------------------

func systemdUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", "outpost.service"), nil
}

func installSystemd(exe string, runArgs []string) error {
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return err
	}
	unit := "[Unit]\nDescription=outpost remote-access daemon\nAfter=network.target\n\n" +
		"[Service]\nExecStart=" + exe + " " + strings.Join(runArgs, " ") + "\nRestart=always\n\n" +
		"[Install]\nWantedBy=default.target\n"
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", "outpost.service").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable: %v: %s", err, out)
	}
	fmt.Printf("Installed systemd --user service: %s\n", unitPath)
	fmt.Printf("outpost now runs in the background. Enable lingering to start before login:\n  loginctl enable-linger %s\n", os.Getenv("USER"))
	return nil
}

func uninstallSystemd() error {
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	_ = exec.Command("systemctl", "--user", "disable", "--now", "outpost.service").Run()
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	fmt.Println("Removed systemd --user service outpost.service")
	return nil
}

// xmlEscape escapes the few characters that matter inside plist <string>.
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
