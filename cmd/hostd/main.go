// Command hostd is the CLI that runs on the host machine and serves
// remote terminal/file/monitor/custom-API access to the mobile app.
//
// Commands:
//
//	hostd start   --bind <addr> --port <port>
//	              Loads the device identity, builds the WebSocket server, prints
//	              a pairing QR code, then blocks until SIGINT.
//
//	hostd pair    Loads the device identity and prints a fresh pairing QR code
//	              without starting a server.
//
//	hostd status  Prints the device ID and the path to the config directory.
//
//	hostd revoke  Revokes all outstanding tokens; the next client connection must
//	              re-pair via QR.
//
// See docs/PLAN.md for the full architecture and milestones (M1–M5).
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/famgia/remote-host/internal/auth"
	"github.com/famgia/remote-host/internal/channel"
	"github.com/famgia/remote-host/internal/customapi"
	"github.com/famgia/remote-host/internal/dashboard"
	fs "github.com/famgia/remote-host/internal/fs"
	"github.com/famgia/remote-host/internal/launcher"
	"github.com/famgia/remote-host/internal/qr"
	"github.com/famgia/remote-host/internal/server"
	"github.com/famgia/remote-host/internal/sys"
	"github.com/famgia/remote-host/internal/term"
)

// version is the hostd build version. Overridable at build time with
//
//	-ldflags "-X main.version=v1.2.3"
var version = "0.1.0-dev"

// flagConfigDir is the --config-dir persistent flag: it selects the identity/
// token directory. Distinct dirs ⇒ distinct hosts (run several hostd instances).
var flagConfigDir string

func main() {
	if err := rootCmd().Execute(); err != nil {
		// cobra already prints the error; just exit non-zero.
		os.Exit(1)
	}
}

// rootCmd builds the top-level cobra command with all sub-commands attached.
func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "hostd",
		Short: "hostd – remote-access host daemon",
		Long: `hostd runs on the machine you want to control remotely.

Start the daemon, scan the QR code with the mobile app, and you get a
terminal, file browser, system monitor, and custom API over WebSocket.`,
		// No default action; require an explicit sub-command.
		SilenceUsage: true,
		Version:      version, // enables `hostd --version`
	}

	root.PersistentFlags().StringVar(&flagConfigDir, "config-dir", "",
		"identity/token directory (default ~/.config/hostd or platform equivalent); use distinct dirs to run multiple independent hosts")
	root.AddCommand(startCmd(), pairCmd(), statusCmd(), revokeCmd(), devicesCmd(), restoreCmd(), versionCmd(), installCmd(), uninstallCmd())
	return root
}

// versionCmd prints the hostd version (also available as `hostd --version`).
func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the hostd version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("hostd %s\n", version)
			return nil
		},
	}
}

// ---- helpers -----------------------------------------------------------------

// pairingBlock prints a fresh pairing QR code (and raw payload) for mgr, valid
// for ttl. Shared by both the "start" and "pair" sub-commands.
func pairingBlock(mgr *auth.Manager, wsURL string, ttl time.Duration) {
	code := mgr.StartPairing(ttl)
	payload := qr.PairingPayload(wsURL, mgr.DeviceID(), code)

	fmt.Println()
	if err := qr.Render(payload); err != nil {
		fmt.Fprintf(os.Stderr, "hostd: render QR: %v\n", err)
	}
	fmt.Printf("\nPairing payload : %s\n", payload)
	fmt.Printf("Device ID       : %s\n", mgr.DeviceID())
	fmt.Printf("Pairing code    : %s  (valid %s)\n\n", code, ttl)
}

// makeHandlersFunc returns a factory that builds a FRESH slice of
// channel.Handler values per accepted connection. Per-connection handlers keep
// connection-scoped state (metric tickers, which sessions this connection is
// attached to), but the terminal session Manager is SHARED across all
// connections so terminal sessions outlive any single connection (tmux-like).
func makeHandlersFunc(sessions *term.Manager) func() []channel.Handler {
	return func() []channel.Handler {
		return []channel.Handler{
			term.New(sessions),
			fs.New(),
			sys.New(),
			customapi.New(),
		}
	}
}

// ---- start -------------------------------------------------------------------

func startCmd() *cobra.Command {
	var bind string
	var port string
	var pairTTL time.Duration
	var doRestore bool
	var advertise string
	var doOpen bool
	var dashPort string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the hostd WebSocket server",
		Long: `Start the WebSocket server, print a pairing QR code, then serve until SIGINT.

The mobile app scans the QR to pair and receives a long-lived token.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := auth.LoadFrom(flagConfigDir)
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}
			defer mgr.Close()

			addr := bind + ":" + port
			// The URL embedded in the QR must be reachable by the client. When
			// binding 0.0.0.0 (all interfaces) the bind address is useless to a
			// phone, so let the user advertise the real LAN/relay URL.
			wsURL := "ws://" + addr
			if advertise != "" {
				wsURL = advertise
			}

			// Shared terminal session manager: one per daemon. When dtach is
			// available, sessions are backed by dtach sockets under the config
			// dir, so they survive a hostd restart and can be attached locally
			// with `dtach -a <socket>` (native scrolling). Otherwise sessions are
			// in-process and live only for the daemon's lifetime.
			cfgDir := flagConfigDir
			if cfgDir == "" {
				cfgDir, _ = auth.DefaultConfigDir()
			}
			sessDir := filepath.Join(cfgDir, "sessions")
			useDtach := false
			if _, lerr := exec.LookPath("dtach"); lerr == nil {
				useDtach = true
			}
			if useDtach {
				fmt.Printf("Sessions        : dtach-backed (survive restart; local: dtach -a %s/<id>.sock)\n", sessDir)
			} else {
				fmt.Println("Sessions        : in-process (install dtach for restart-persistent + local attach)")
			}
			sessions := term.NewManager(launcher.NewDirect(), sessDir, useDtach, mgr.Store())

			// Auto-restore saved sessions on startup (e.g. after a reboot). Only
			// re-opens sessions whose dtach master is gone, so it's a no-op on a
			// plain daemon relaunch.
			if doRestore && useDtach {
				if restored, rerr := term.RestoreFromStore(sessDir, os.Getenv("SHELL"), mgr.Store()); rerr == nil && len(restored) > 0 {
					fmt.Printf("Restored        : %d saved session(s)\n", len(restored))
				}
			}

			// Build the WebSocket server. The handler factory runs fresh per
			// connection; only the terminal Manager is shared.
			srv := server.New(mgr, makeHandlersFunc(sessions))

			// Print the pairing QR before blocking.
			pairingBlock(mgr, wsURL, pairTTL)

			// Run until SIGINT / SIGTERM.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				fmt.Fprintln(os.Stderr, "\nhostd: shutting down…")
				cancel()
			}()

			// Optional local web dashboard (always 127.0.0.1 — it hands out
			// pairing QRs so it must never be exposed on the LAN).
			if doOpen {
				if dashPort == "" {
					if p, perr := strconv.Atoi(port); perr == nil {
						dashPort = strconv.Itoa(p + 1000) // 8722 → 9722, distinct per host
					} else {
						dashPort = "9720"
					}
				}
				dash := buildDashboard(mgr, sessions, wsURL, pairTTL)
				go func() { _ = dash.ListenAndServe(ctx, dashPort) }()
				dashURL := "http://127.0.0.1:" + dashPort
				fmt.Printf("Dashboard       : %s\n", dashURL)
				go openBrowser(dashURL)
			}

			if err := srv.ListenAndServe(ctx, addr); err != nil {
				return fmt.Errorf("serve: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "bind address")
	cmd.Flags().StringVar(&port, "port", "8722", "listen port")
	cmd.Flags().DurationVar(&pairTTL, "pair-ttl", 2*time.Minute, "how long the pairing code stays valid (e.g. 30m for slow/manual pairing)")
	cmd.Flags().BoolVar(&doRestore, "restore", false, "on startup, re-open saved sessions whose dtach master is gone (e.g. after a reboot)")
	cmd.Flags().StringVar(&advertise, "advertise", "", "URL to embed in the pairing QR (default ws://<bind>:<port>); set this to the LAN/relay URL clients use, e.g. ws://192.168.1.28:8722")
	cmd.Flags().BoolVar(&doOpen, "open", false, "start a local web dashboard (QR + devices + sessions) and open it in the browser")
	cmd.Flags().StringVar(&dashPort, "dashboard-port", "", "dashboard port on 127.0.0.1 (default: listen port + 1000)")
	return cmd
}

// buildDashboard wires the dashboard's data closures to the auth + session
// managers (kept here so the dashboard package stays decoupled).
func buildDashboard(mgr *auth.Manager, sessions *term.Manager, wsURL string, pairTTL time.Duration) *dashboard.Server {
	return &dashboard.Server{
		DeviceID:     mgr.DeviceID(),
		AdvertiseURL: wsURL,
		NewCode:      func() string { return mgr.StartPairing(pairTTL) },
		Devices: func() ([]dashboard.DeviceInfo, error) {
			ds, err := mgr.Devices()
			if err != nil {
				return nil, err
			}
			out := make([]dashboard.DeviceInfo, 0, len(ds))
			for _, d := range ds {
				status := "active"
				if d.Revoked {
					status = "revoked"
				}
				out = append(out, dashboard.DeviceInfo{
					ClientID: d.ClientID,
					Name:     d.Name,
					Type:     d.Type,
					PairedAt: time.Unix(d.PairedAt, 0).Format("2006-01-02 15:04"),
					LastSeen: time.Unix(d.LastSeen, 0).Format("2006-01-02 15:04"),
					Status:   status,
				})
			}
			return out, nil
		},
		Revoke: mgr.RevokeDevice,
		Rename: mgr.RenameDevice,
		Sessions: func() []dashboard.SessionInfo {
			cwd := map[string]string{}
			if rows, err := mgr.Store().ListSessions(); err == nil {
				for _, r := range rows {
					cwd[r.ID] = r.Cwd
				}
			}
			var out []dashboard.SessionInfo
			for _, s := range sessions.List() {
				out = append(out, dashboard.SessionInfo{ID: s.ID, Title: s.Title, Cwd: cwd[s.ID], Alive: s.Alive})
			}
			return out
		},
	}
}

// openBrowser opens url in the default browser (best-effort, per OS).
func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, append(args, url)...).Start()
}

// ---- pair --------------------------------------------------------------------

func pairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pair",
		Short: "Print a fresh pairing QR code (without starting the server)",
		Long: `Generate and display a new short-lived pairing code as a QR code.

Use this when you want to pair a new device without restarting the daemon,
or to inspect the pairing payload for debugging.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := auth.LoadFrom(flagConfigDir)
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}
			// Use a placeholder URL; the user is expected to edit the relay/tunnel
			// URL in their config before sharing with a remote device.
			wsURL := "ws://127.0.0.1:8722"
			pairingBlock(mgr, wsURL, 2*time.Minute)
			return nil
		},
	}
}

// ---- status ------------------------------------------------------------------

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print device ID and config directory path",
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := auth.LoadFrom(flagConfigDir)
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}
			dir := flagConfigDir
			if dir == "" {
				if d, derr := auth.DefaultConfigDir(); derr == nil {
					dir = d
				}
			}
			fmt.Printf("hostd version  : %s\n", version)
			fmt.Printf("Device ID      : %s\n", mgr.DeviceID())
			fmt.Printf("Config dir     : %s\n", dir)
			fmt.Printf("Token gen      : %d", mgr.Generation())
			if mgr.Generation() == 0 {
				fmt.Print("  (no revocations; tokens from any pairing are valid)")
			} else {
				fmt.Print("  (revoked; clients before this gen must re-pair)")
			}
			fmt.Println()
			return nil
		},
	}
}

// ---- revoke ------------------------------------------------------------------

func revokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke [clientId]",
		Short: "Revoke one device (by client id) or all tokens (no arg)",
		Long: `Revoke access.

With a client id (see "hostd devices"), revokes just that device — others stay
connected. With no argument, revokes ALL tokens (global): every client must
re-pair. Use this when a paired device is lost or compromised.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := auth.LoadFrom(flagConfigDir)
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}
			defer mgr.Close()
			if len(args) == 1 {
				if err := mgr.RevokeDevice(args[0]); err != nil {
					return fmt.Errorf("revoke device: %w", err)
				}
				fmt.Printf("Device %s revoked. It must re-pair; other devices are unaffected.\n", args[0])
				return nil
			}
			if err := mgr.Revoke(); err != nil {
				return fmt.Errorf("revoke: %w", err)
			}
			fmt.Println("All tokens revoked. Every device must re-pair.")
			return nil
		},
	}
}

// ---- devices -----------------------------------------------------------------

func devicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: "List paired client devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := auth.LoadFrom(flagConfigDir)
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}
			defer mgr.Close()
			devices, err := mgr.Devices()
			if err != nil {
				return fmt.Errorf("list devices: %w", err)
			}
			if len(devices) == 0 {
				fmt.Println("No paired devices yet.")
				return nil
			}
			fmt.Printf("%-18s  %-20s  %-22s  %-16s  %s\n", "CLIENT ID", "NAME", "TYPE", "LAST SEEN", "STATUS")
			for _, d := range devices {
				status := "active"
				if d.Revoked {
					status = "revoked"
				}
				name := d.Name
				if name == "" {
					name = "(unnamed)"
				}
				fmt.Printf("%-18s  %-20s  %-22s  %-16s  %s\n",
					d.ClientID, name, d.Type,
					time.Unix(d.LastSeen, 0).Format("2006-01-02 15:04"),
					status)
			}
			return nil
		},
	}
}

// ---- restore -----------------------------------------------------------------

func restoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore",
		Short: "Re-open saved terminal sessions (e.g. after a reboot) in their folders",
		Long: `Re-create persisted terminal sessions whose dtach master is gone (after a
reboot) — each as a fresh shell in the working directory it was last seen in.

A reboot ends the original running processes; this restores the session list and
working directories, not live programs. Run it once after boot (the daemon then
lists the restored sessions and the app can attach to them).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, lerr := exec.LookPath("dtach"); lerr != nil {
				return fmt.Errorf("restore requires dtach (brew install dtach)")
			}
			mgr, err := auth.LoadFrom(flagConfigDir)
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}
			defer mgr.Close()

			cfgDir := flagConfigDir
			if cfgDir == "" {
				cfgDir, _ = auth.DefaultConfigDir()
			}
			sessDir := filepath.Join(cfgDir, "sessions")

			restored, err := term.RestoreFromStore(sessDir, os.Getenv("SHELL"), mgr.Store())
			if err != nil {
				return fmt.Errorf("restore: %w", err)
			}
			if len(restored) == 0 {
				fmt.Println("No sessions to restore (none saved, or all already running).")
				return nil
			}
			fmt.Printf("Restored %d session(s):\n", len(restored))
			for _, r := range restored {
				fmt.Printf("  %-12s  %-12s  %s\n", r.ID, r.Title, r.Cwd)
			}
			return nil
		},
	}
}
